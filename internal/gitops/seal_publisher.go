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

// ClusterSealParams describes the single 1Password Connect token + unified
// ClusterSecretStore to publish for ONE workload cluster. A cluster carries
// ONE token with access to every vault it reads — the org-wide global vault
// plus the env vault(s) of the environments deployed to it (which also hold
// its cluster-override items) — and ONE store listing those vaults.
type ClusterSealParams struct {
	// ClusterName is the registered suparship cluster name (used for the
	// per-cluster ArgoCD Application name and the _secret-stores/{cluster}/ dir).
	ClusterName string
	// ArgoCDDestination is the target cluster API server URL (https://...).
	ArgoCDDestination string
	// ESONamespace is where ESO + the sealed Connect-token Secret live on the
	// target cluster. Defaults to "external-secrets".
	ESONamespace string
	// Cert is the target cluster's sealed-secrets controller public cert (PEM).
	Cert []byte
	// Token is the cluster's single plaintext Connect token. It is sealed with
	// the target cluster's cert and never written to Git in plaintext.
	Token []byte
	// VaultIDs are the 1Password vault UUIDs this cluster reads (global first,
	// then env vaults). Rendered into the unified store's vaults map in order.
	VaultIDs []string
	// ConnectEndpoint overrides the in-cluster 1Password Connect URL.
	ConnectEndpoint string
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
	if len(params.VaultIDs) == 0 {
		return fmt.Errorf("PublishClusterSecretStore: no vaults registered for cluster %q", params.ClusterName)
	}
	esoNS := params.ESONamespace
	if esoNS == "" {
		esoNS = secrets.OnePasswordRemoteNamespace
	}

	// Render both files first so a sealing error aborts before any write.
	sealedYAML, err := seal.BuildSealedSecret(params.Cert, seal.SealedSecretInput{
		Name:      secrets.ConnectTokenSecretName,
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
	storeYAML := BuildUnifiedClusterSecretStoreYAML(UnifiedStoreConfig{
		VaultIDs:        params.VaultIDs,
		ESONamespace:    esoNS,
		ConnectEndpoint: params.ConnectEndpoint,
		Branding:        p.cfg.Branding,
	})

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
