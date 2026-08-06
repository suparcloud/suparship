package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/seal"
	"github.com/suparcloud/suparship/internal/secrets"
)

// ClusterSealParams describes the single per-cluster credential + unified
// ClusterSecretStore to publish for ONE workload cluster. A cluster carries
// ONE credential granting access to everything it reads, and ONE store
// (secrets.UnifiedStoreName) resolving through it. For 1Password the
// credential is a Connect token and the store lists the cluster's vault UUIDs;
// for Vault it is a Vault token and the store carries the server address +
// KV v2 mount. The sealing and publishing mechanics are identical.
type ClusterSealParams struct {
	// ClusterName is the registered suparship cluster name (used for the
	// per-cluster ArgoCD Application name and the _secret-stores/{cluster}/ dir).
	ClusterName string
	// ArgoCDDestination is the target cluster API server URL (https://...).
	ArgoCDDestination string
	// ESONamespace is where ESO + the sealed credential Secret live on the
	// target cluster. Defaults to "external-secrets".
	ESONamespace string
	// Cert is the target cluster's sealed-secrets controller public cert (PEM).
	Cert []byte
	// Token is the cluster's single plaintext credential (Connect token or
	// Vault token, per Backend). It is sealed with the target cluster's cert
	// and never written to Git in plaintext.
	Token []byte
	// Backend selects the store shape and the sealed Secret's name.
	// "" means 1Password, the only backend that sealed before this existed.
	Backend secrets.BackendType
	// VaultIDs are the 1Password vault UUIDs this cluster reads (global first,
	// then env vaults). Rendered into the unified store's vaults map in order.
	// 1Password only.
	VaultIDs []string
	// ConnectEndpoint overrides the in-cluster 1Password Connect URL.
	// 1Password only.
	ConnectEndpoint string
	// Vault carries the HashiCorp Vault store parameters. Vault only.
	// ESONamespace and Branding are filled in from the params at render time.
	Vault VaultStoreConfig
}

// backend returns the effective backend for the seal ("" = 1Password, which
// predates the field).
func (p ClusterSealParams) backend() secrets.BackendType {
	if p.Backend == "" {
		return secrets.Backend1Password
	}
	return p.Backend
}

// PublishClusterSecretStore seals the cluster's Connect token with the target
// cluster's cert and commits, per cluster:
//
//	gitops-output/_secret-stores/{cluster}/sealed-token.yaml  -- SealedSecret (op-connect-token)
//	gitops-output/_secret-stores/{cluster}/store.yaml         -- unified ClusterSecretStore (suparship-store)
//	gitops-output/_infra/secrets-{cluster}-app.yaml           -- ArgoCD Application
//
// The ArgoCD Application syncs the whole _secret-stores/{cluster}/ directory to
// the target cluster, so its ESO can read every vault the cluster needs through
// one store. Any other files in the directory are pruned (including the legacy
// per-scope sealed-token-{scope}.yaml / store-{scope}.yaml pairs). Idempotent.
func (p *Publisher) PublishClusterSecretStore(ctx context.Context, params ClusterSealParams) error {
	if params.ClusterName == "" {
		return fmt.Errorf("PublishClusterSecretStore: clusterName is required")
	}
	if params.ArgoCDDestination == "" {
		return fmt.Errorf("PublishClusterSecretStore: ArgoCDDestination is required for cluster %q", params.ClusterName)
	}
	if len(params.Cert) == 0 {
		return fmt.Errorf("PublishClusterSecretStore: cert is required for cluster %q", params.ClusterName)
	}
	if len(params.Token) == 0 {
		return fmt.Errorf("PublishClusterSecretStore: token is required for cluster %q", params.ClusterName)
	}
	esoNS := params.ESONamespace
	if esoNS == "" {
		esoNS = secrets.OnePasswordRemoteNamespace
	}

	// Per-backend validation, sealed Secret name and store shape. The rest of
	// the pipeline — sealing, file layout, the ArgoCD Application — is
	// backend-agnostic.
	var sealedName, storeYAML string
	switch params.backend() {
	case secrets.BackendVault:
		if params.Vault.Address == "" {
			return fmt.Errorf("PublishClusterSecretStore: vault address is required for cluster %q", params.ClusterName)
		}
		sealedName = secrets.VaultTokenClusterSecretName
		vcfg := params.Vault
		vcfg.ESONamespace = esoNS
		vcfg.Branding = p.cfg.Branding
		storeYAML = BuildVaultClusterSecretStoreYAML(vcfg)
	default: // 1Password
		if len(params.VaultIDs) == 0 {
			return fmt.Errorf("PublishClusterSecretStore: no vaults registered for cluster %q", params.ClusterName)
		}
		sealedName = secrets.ConnectTokenSecretName
		storeYAML = BuildUnifiedClusterSecretStoreYAML(UnifiedStoreConfig{
			VaultIDs:        params.VaultIDs,
			ESONamespace:    esoNS,
			ConnectEndpoint: params.ConnectEndpoint,
			Branding:        p.cfg.Branding,
		})
	}

	// Render both files first so a sealing error aborts before any write.
	// Both backends use the same in-Secret key ("token").
	sealedYAML, err := seal.BuildSealedSecret(params.Cert, seal.SealedSecretInput{
		Name:      sealedName,
		Namespace: esoNS,
		Scope:     seal.ScopeNamespaceWide,
		Data:      map[string][]byte{secrets.SATokenSecretKey: params.Token},
		Type:      "Opaque",
		Labels: branding.MergeLabels(
			p.cfg.Branding.ManagedByLabels(),
			map[string]string{p.cfg.Branding.LabelKey("cluster"): params.ClusterName},
		),
	})
	if err != nil {
		return fmt.Errorf("seal token for cluster %q: %w", params.ClusterName, err)
	}

	return p.withClonedRepo(ctx, func(repoDir string) error {
		storesDir := p.outputDir(repoDir, "_secret-stores", params.ClusterName)
		infraDir := p.outputDir(repoDir, "_infra")

		wanted := map[string]bool{"sealed-token.yaml": true, "store.yaml": true}
		if err := p.writeFile(filepath.Join(storesDir, "sealed-token.yaml"), []byte(sealedYAML)); err != nil {
			return err
		}
		if err := p.writeFile(filepath.Join(storesDir, "store.yaml"), []byte(storeYAML)); err != nil {
			return err
		}

		// Prune everything else — legacy per-scope sealed-token-{scope}.yaml /
		// store-{scope}.yaml pairs from the per-vault-token model.
		if entries, err := os.ReadDir(storesDir); err == nil {
			for _, e := range entries {
				if e.IsDir() || wanted[e.Name()] {
					continue
				}
				_ = os.Remove(filepath.Join(storesDir, e.Name()))
			}
		}

		appYAML := buildSecretStoreArgoApp(params.ClusterName, p.argoCDRepoURL(), p.cfg.Branch, params.ArgoCDDestination, esoNS, p.cfg.Branding, p.cfg.SubPath)
		if err := p.writeFile(filepath.Join(infraDir, "secrets-"+params.ClusterName+"-app.yaml"), []byte(appYAML)); err != nil {
			return err
		}
		return p.commitAndPush(ctx, repoDir, fmt.Sprintf("feat(secrets): seal connect token for cluster=%s", params.ClusterName))
	})
}

// HasClusterSecretStore reports whether a cluster's unified store (store.yaml)
// is present in the gitops repo. Used by startup self-heal to skip clusters
// that are already published — re-sealing is non-deterministic and would
// produce a noisy commit on every restart.
func (p *Publisher) HasClusterSecretStore(ctx context.Context, clusterName string) (bool, error) {
	if clusterName == "" {
		return false, fmt.Errorf("HasClusterSecretStore: clusterName is required")
	}
	var present bool
	err := p.withClonedRepo(ctx, func(repoDir string) error {
		storePath := p.outputDir(repoDir, "_secret-stores", clusterName, "store.yaml")
		if _, statErr := os.Stat(storePath); statErr == nil {
			present = true
		}
		return nil
	})
	return present, err
}

// DeleteClusterSecretStores removes a cluster's sealed token + store and its
// ArgoCD Application. No-op if absent.
func (p *Publisher) DeleteClusterSecretStores(ctx context.Context, clusterName string) error {
	if clusterName == "" {
		return fmt.Errorf("DeleteClusterSecretStores: clusterName is required")
	}
	return p.withClonedRepo(ctx, func(repoDir string) error {
		storesDir := p.outputDir(repoDir, "_secret-stores", clusterName)
		appFile := p.outputDir(repoDir, "_infra", "secrets-"+clusterName+"-app.yaml")
		removed := false
		if _, err := os.Stat(storesDir); err == nil {
			if err := os.RemoveAll(storesDir); err != nil {
				return fmt.Errorf("removing secret-store dir %s: %w", storesDir, err)
			}
			removed = true
		}
		if _, err := os.Stat(appFile); err == nil {
			if err := os.Remove(appFile); err != nil {
				return fmt.Errorf("removing secrets app file %s: %w", appFile, err)
			}
			removed = true
		}
		if !removed {
			return nil
		}
		return p.commitAndPush(ctx, repoDir, fmt.Sprintf("feat(secrets): remove secret-stores for cluster=%s", clusterName))
	})
}

// buildSecretStoreArgoApp returns an ArgoCD Application that syncs the
// per-cluster secret-stores directory to the target cluster. Named
// secrets-{clusterName} so it reflects the physical target.
func buildSecretStoreArgoApp(clusterName, repoURL, branch, destServer, esoNamespace string, brand branding.Config, subPath string) string {
	labels := branding.MergeLabels(
		brand.ManagedByLabels(),
		map[string]string{brand.LabelKey("cluster"): clusterName},
	)
	storesPath := joinSubPath(subPath, "_secret-stores", clusterName)
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: secrets-%s
  namespace: argocd
  labels:
%s
spec:
  project: suparship-system
  source:
    repoURL: %s
    targetRevision: %s
    path: %s
    directory:
      recurse: false
      include: '{sealed-token*.yaml,store*.yaml}'
  destination:
    server: %s
    namespace: %s
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
`, clusterName, branding.LabelsYAML(labels, 4), repoURL, branch, storesPath, destServer, esoNamespace)
}
