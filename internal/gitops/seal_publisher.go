package gitops

import (
	"context"
	"crypto/rsa"
	"fmt"
	"os"
	"path/filepath"

	"github.com/suparcloud/suparship/internal/seal"
	"github.com/suparcloud/suparship/internal/secrets"
)

// SealedReadTokenPublishParams captures the inputs needed to publish a
// per-environment 1Password Connect token to gitops-output.
type SealedReadTokenPublishParams struct {
	Env     string // e.g. "staging"
	VaultID string // 1Password vault UUID for this env
	OrgName string // for ClusterSecretStore naming
	Token   []byte // plaintext Connect token (sealed; never written to Git)
	Cert    *rsa.PublicKey
	// ArgoCDDestination is the ArgoCD destination server URL for the target
	// cluster (https://...). Required — no fallback to in-cluster default.
	ArgoCDDestination string
	// ClusterName is the registered suparship name of the target cluster
	// (e.g. "staging-aks-02-scus"). Used for the ArgoCD Application name so
	// the name reflects the physical target rather than the logical env.
	ClusterName string
	// ESONamespace is the namespace where External Secrets Operator is
	// installed on the target cluster. Defaults to "external-secrets".
	ESONamespace string
}

// DeleteSealedReadTokenParams captures the inputs needed to remove a
// per-environment secret-store from gitops-output.
type DeleteSealedReadTokenParams struct {
	// Env is the environment name (e.g. "staging"). Used for the store path.
	Env string
	// ClusterName is the cluster name used in the ArgoCD Application name.
	ClusterName string
}

// PublishSealedReadToken seals the provided Connect token using the target
// cluster's public certificate and commits three files to gitops-output:
//
//	gitops-output/_secret-stores/{env}/sealed-token.yaml  -- SealedSecret
//	gitops-output/_secret-stores/{env}/store.yaml         -- ClusterSecretStore
//	gitops-output/_infra/secrets-{cluster}-app.yaml       -- ArgoCD Application (discovered by root app)
//
// Secret-store manifests are placed under _secret-stores/ (outside _infra/) so
// the root app does not double-sync them alongside the dedicated secrets-{cluster}
// Application, avoiding SharedResourceWarning.
func (p *Publisher) PublishSealedReadToken(ctx context.Context, params SealedReadTokenPublishParams) error {
	if params.Env == "" || params.VaultID == "" {
		return fmt.Errorf("PublishSealedReadToken: env and vaultID are required")
	}
	if params.ClusterName == "" {
		return fmt.Errorf("PublishSealedReadToken: clusterName is required")
	}
	if params.Cert == nil {
		return fmt.Errorf("PublishSealedReadToken: cert is required")
	}
	if len(params.Token) == 0 {
		return fmt.Errorf("PublishSealedReadToken: token is empty")
	}

	esoNS := params.ESONamespace
	if esoNS == "" {
		esoNS = "external-secrets"
	}

	tokenSecretName := secrets.ConnectTokenSecretName(params.Env)

	sealedYAML, err := seal.BuildSealedSecret(params.Cert, seal.SealedSecretInput{
		Name:      tokenSecretName,
		Namespace: esoNS,
		Scope:     seal.ScopeNamespaceWide,
		Data: map[string][]byte{
			secrets.SATokenSecretKey: params.Token,
		},
		Type: "Opaque",
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "suparship",
			"suparship.io/env":             params.Env,
		},
	})
	if err != nil {
		return fmt.Errorf("seal token: %w", err)
	}

	naming := secrets.ResourceNaming{}
	storeName := naming.RenderClusterSecretStore(secrets.NamingParams{
		Provider: string(secrets.Backend1Password),
		Env:      params.Env,
		Org:      params.OrgName,
	})

	storeYAML := BuildClusterSecretStoreYAML(ESOSecretStoreConfig{
		Name:         storeName,
		BackendType:  secrets.Backend1Password,
		Binding:      secrets.EnvBinding{Env: params.Env, VaultID: params.VaultID},
		ESONamespace: esoNS,
	})

	destServer := params.ArgoCDDestination
	if destServer == "" {
		return fmt.Errorf("PublishSealedReadToken: ArgoCDDestination is required (resolved to empty for env %q)", params.Env)
	}

	return p.withClonedRepo(ctx, func(repoDir string) error {
		// Manifests live outside _infra/ to avoid root-app double-sync.
		storesDir := filepath.Join(repoDir, "gitops-output", "_secret-stores", params.Env)
		infraDir := filepath.Join(repoDir, "gitops-output", "_infra")

		// Clean up legacy paths from older suparship versions (env-named app +
		// _infra/secret-stores/ dir) before writing the current layout, so stale
		// ArgoCD Applications don't keep deploying wrong resources to the cluster.
		legacyStoresDir := filepath.Join(infraDir, "secret-stores", params.Env)
		legacyAppFile := filepath.Join(infraDir, "secrets-"+params.Env+"-app.yaml")
		if _, err := os.Stat(legacyStoresDir); err == nil {
			_ = os.RemoveAll(legacyStoresDir)
		}
		if _, err := os.Stat(legacyAppFile); err == nil {
			_ = os.Remove(legacyAppFile)
		}

		if err := p.writeFile(filepath.Join(storesDir, "sealed-token.yaml"), []byte(sealedYAML)); err != nil {
			return err
		}
		if err := p.writeFile(filepath.Join(storesDir, "store.yaml"), []byte(storeYAML)); err != nil {
			return err
		}

		appYAML := buildSecretStoreArgoApp(params.Env, params.ClusterName, p.argoCDRepoURL(), p.cfg.Branch, destServer, esoNS)
		if err := p.writeFile(filepath.Join(infraDir, "secrets-"+params.ClusterName+"-app.yaml"), []byte(appYAML)); err != nil {
			return err
		}

		return p.commitAndPush(ctx, repoDir, fmt.Sprintf("feat(secrets): provision env=%s cluster=%s", params.Env, params.ClusterName))
	})
}

// DeleteSealedReadToken removes the per-environment secret-store directory
// and the ArgoCD Application from the GitOps repo and commits the deletion.
// It also removes any legacy paths written by older versions of suparship
// (which used the env name instead of the cluster name and wrote manifests
// under _infra/secret-stores/ rather than _secret-stores/).
// It is a no-op if none of the paths exist.
func (p *Publisher) DeleteSealedReadToken(ctx context.Context, params DeleteSealedReadTokenParams) error {
	if params.Env == "" {
		return fmt.Errorf("DeleteSealedReadToken: env is required")
	}
	if params.ClusterName == "" {
		return fmt.Errorf("DeleteSealedReadToken: clusterName is required")
	}
	return p.withClonedRepo(ctx, func(repoDir string) error {
		// Current paths (cluster-named app + dedicated _secret-stores/ dir).
		storesDir := filepath.Join(repoDir, "gitops-output", "_secret-stores", params.Env)
		appFile := filepath.Join(repoDir, "gitops-output", "_infra", "secrets-"+params.ClusterName+"-app.yaml")

		// Legacy paths created by older suparship versions: env-named app +
		// secrets dir nested inside _infra/.
		legacyStoresDir := filepath.Join(repoDir, "gitops-output", "_infra", "secret-stores", params.Env)
		legacyAppFile := filepath.Join(repoDir, "gitops-output", "_infra", "secrets-"+params.Env+"-app.yaml")

		removedAny := false

		for _, dir := range []string{storesDir, legacyStoresDir} {
			if _, err := os.Stat(dir); err == nil {
				if err := os.RemoveAll(dir); err != nil {
					return fmt.Errorf("removing secret-store dir %s: %w", dir, err)
				}
				removedAny = true
			}
		}

		for _, f := range []string{appFile, legacyAppFile} {
			if _, err := os.Stat(f); err == nil {
				if err := os.Remove(f); err != nil {
					return fmt.Errorf("removing secrets app file %s: %w", f, err)
				}
				removedAny = true
			}
		}

		if !removedAny {
			return nil
		}
		return p.commitAndPush(ctx, repoDir, fmt.Sprintf("feat(secrets): remove secret-store for env=%s cluster=%s", params.Env, params.ClusterName))
	})
}

// buildSecretStoreArgoApp returns a minimal ArgoCD Application that syncs
// the per-env secret-stores directory to the given target cluster.
// The app is named secrets-{clusterName} so the name reflects the physical
// target cluster rather than the logical environment.
func buildSecretStoreArgoApp(env, clusterName, repoURL, branch, destServer, esoNamespace string) string {
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: secrets-%s
  namespace: argocd
  labels:
    app.kubernetes.io/managed-by: suparship
    suparship.io/env: %s
    suparship.io/cluster: %s
spec:
  project: suparship-system
  source:
    repoURL: %s
    targetRevision: %s
    path: gitops-output/_secret-stores/%s
    directory:
      recurse: false
      include: '{sealed-token.yaml,store.yaml}'
  destination:
    server: %s
    namespace: %s
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
`, clusterName, env, clusterName, repoURL, branch, env, destServer, esoNamespace)
}
