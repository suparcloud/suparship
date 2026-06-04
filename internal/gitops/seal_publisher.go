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

// ScopeToken is one vault's 1Password Connect token to seal onto a workload
// cluster, alongside the ClusterSecretStore that reads that vault.
type ScopeToken struct {
	// Scope identifies the vault (global, an env, or a cluster).
	Scope secrets.Scope
	// VaultID is the 1Password vault UUID for this scope.
	VaultID string
	// Token is the plaintext Connect token. It is sealed with the target
	// cluster's cert and never written to Git in plaintext.
	Token []byte
	// ConnectEndpoint overrides the in-cluster 1Password Connect URL.
	ConnectEndpoint string
}

// ClusterSealParams describes the full set of 1Password Connect tokens +
// ClusterSecretStores to publish for ONE workload cluster. A cluster reads its
// own cluster vault, the env vault(s) of the environments deployed to it, and
// the org-wide global vault — so Scopes carries all of those.
type ClusterSealParams struct {
	// ClusterName is the registered suparship cluster name (used for the
	// per-cluster ArgoCD Application name and the _secret-stores/{cluster}/ dir).
	ClusterName string
	// ArgoCDDestination is the target cluster API server URL (https://...).
	ArgoCDDestination string
	// ESONamespace is where ESO + the sealed Connect-token Secrets live on the
	// target cluster. Defaults to "external-secrets".
	ESONamespace string
	// Cert is the target cluster's sealed-secrets controller public cert (PEM).
	Cert []byte
	// Scopes are the vault tokens to seal onto this cluster.
	Scopes []ScopeToken
}

// PublishClusterSecretStores seals each scope's Connect token with the target
// cluster's cert and commits, per cluster:
//
//	gitops-output/_secret-stores/{cluster}/sealed-token-{scopeKey}.yaml  -- SealedSecret
//	gitops-output/_secret-stores/{cluster}/store-{scopeKey}.yaml         -- ClusterSecretStore
//	gitops-output/_infra/secrets-{cluster}-app.yaml                      -- ArgoCD Application
//
// The ArgoCD Application syncs the whole _secret-stores/{cluster}/ directory to
// the target cluster, so its ESO can read the global / env / cluster vaults.
// Scope files no longer in params.Scopes are pruned. Idempotent.
func (p *Publisher) PublishClusterSecretStores(ctx context.Context, params ClusterSealParams) error {
	if params.ClusterName == "" {
		return fmt.Errorf("PublishClusterSecretStores: clusterName is required")
	}
	if params.ArgoCDDestination == "" {
		return fmt.Errorf("PublishClusterSecretStores: ArgoCDDestination is required for cluster %q", params.ClusterName)
	}
	if len(params.Cert) == 0 {
		return fmt.Errorf("PublishClusterSecretStores: cert is required for cluster %q", params.ClusterName)
	}
	esoNS := params.ESONamespace
	if esoNS == "" {
		esoNS = secrets.OnePasswordRemoteNamespace
	}

	// Render all files first so a sealing error aborts before any write.
	type scopeFile struct{ sealedName, storeName, sealedYAML, storeYAML string }
	files := make([]scopeFile, 0, len(params.Scopes))
	for _, st := range params.Scopes {
		if len(st.Token) == 0 || st.VaultID == "" {
			return fmt.Errorf("PublishClusterSecretStores: scope %q missing token or vaultID", secrets.ScopeKey(st.Scope))
		}
		key := secrets.ScopeKey(st.Scope)
		sealedYAML, err := seal.BuildSealedSecret(params.Cert, seal.SealedSecretInput{
			Name:      secrets.ConnectTokenSecretName(st.Scope),
			Namespace: esoNS,
			Scope:     seal.ScopeNamespaceWide,
			Data:      map[string][]byte{secrets.SATokenSecretKey: st.Token},
			Type:      "Opaque",
			Labels: branding.MergeLabels(
				p.cfg.Branding.ManagedByLabels(),
				map[string]string{p.cfg.Branding.LabelKey("scope"): key},
			),
		})
		if err != nil {
			return fmt.Errorf("seal token for scope %q: %w", key, err)
		}
		storeYAML := BuildClusterSecretStoreYAML(ESOSecretStoreConfig{
			Scope:           st.Scope,
			BackendType:     secrets.Backend1Password,
			VaultID:         st.VaultID,
			ESONamespace:    esoNS,
			ConnectEndpoint: st.ConnectEndpoint,
			Branding:        p.cfg.Branding,
		})
		files = append(files, scopeFile{
			sealedName: "sealed-token-" + key + ".yaml",
			storeName:  "store-" + key + ".yaml",
			sealedYAML: sealedYAML,
			storeYAML:  storeYAML,
		})
	}

	return p.withClonedRepo(ctx, func(repoDir string) error {
		storesDir := p.outputDir(repoDir, "_secret-stores", params.ClusterName)
		infraDir := p.outputDir(repoDir, "_infra")

		wanted := map[string]bool{}
		for _, f := range files {
			wanted[f.sealedName] = true
			wanted[f.storeName] = true
			if err := p.writeFile(filepath.Join(storesDir, f.sealedName), []byte(f.sealedYAML)); err != nil {
				return err
			}
			if err := p.writeFile(filepath.Join(storesDir, f.storeName), []byte(f.storeYAML)); err != nil {
				return err
			}
		}

		// Prune scope files no longer wanted (e.g. an env was unbound from this
		// cluster, or a vault de-provisioned).
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
		return p.commitAndPush(ctx, repoDir, fmt.Sprintf("feat(secrets): seal vault tokens for cluster=%s", params.ClusterName))
	})
}

// DeleteClusterSecretStores removes a cluster's sealed tokens + stores and its
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
      include: '{sealed-token-*.yaml,store-*.yaml}'
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
