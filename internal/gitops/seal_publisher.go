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
	Env     string // e.g. "prod"
	VaultID string // 1Password vault UUID for this env
	OrgName string // for ClusterSecretStore naming
	Token   []byte // plaintext Connect token (sealed; never written to Git)
	Cert    *rsa.PublicKey
	// ArgoCDDestination is the ArgoCD destination server URL for the target
	// cluster (https://...). Falls back to https://kubernetes.default.svc
	// when empty (single-cluster setup).
	ArgoCDDestination string
}

// PublishSealedReadToken seals the provided Connect token using the target
// cluster's public certificate and commits three files to gitops-output:
//
//	gitops-output/_infra/secret-stores/{env}/sealed-token.yaml  -- SealedSecret
//	gitops-output/_infra/secret-stores/{env}/store.yaml         -- ClusterSecretStore
//	gitops-output/_infra/secrets-{env}-app.yaml                 -- ArgoCD Application (discovered by root app)
func (p *Publisher) PublishSealedReadToken(ctx context.Context, params SealedReadTokenPublishParams) error {
	if params.Env == "" || params.VaultID == "" {
		return fmt.Errorf("PublishSealedReadToken: env and vaultID are required")
	}
	if params.Cert == nil {
		return fmt.Errorf("PublishSealedReadToken: cert is required")
	}
	if len(params.Token) == 0 {
		return fmt.Errorf("PublishSealedReadToken: token is empty")
	}

	tokenSecretName := secrets.ConnectTokenSecretName(params.Env)

	sealedYAML, err := seal.BuildSealedSecret(params.Cert, seal.SealedSecretInput{
		Name:      tokenSecretName,
		Namespace: secrets.OnePasswordRemoteNamespace,
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
		Name:        storeName,
		BackendType: secrets.Backend1Password,
		Binding:     secrets.EnvBinding{Env: params.Env, VaultID: params.VaultID},
	})

	destServer := params.ArgoCDDestination
	if destServer == "" {
		destServer = "https://kubernetes.default.svc"
	}

	return p.withClonedRepo(ctx, func(repoDir string) error {
		storesDir := filepath.Join(repoDir, "gitops-output", "_infra", "secret-stores", params.Env)
		infraDir := filepath.Join(repoDir, "gitops-output", "_infra")

		if err := p.writeFile(filepath.Join(storesDir, "sealed-token.yaml"), []byte(sealedYAML)); err != nil {
			return err
		}
		if err := p.writeFile(filepath.Join(storesDir, "store.yaml"), []byte(storeYAML)); err != nil {
			return err
		}

		appYAML := buildSecretStoreArgoApp(params.Env, p.argoCDRepoURL(), p.cfg.Branch, destServer)
		if err := p.writeFile(filepath.Join(infraDir, "secrets-"+params.Env+"-app.yaml"), []byte(appYAML)); err != nil {
			return err
		}

		return p.commitAndPush(ctx, repoDir, fmt.Sprintf("feat(secrets): provision env=%s", params.Env))
	})
}

// DeleteSealedReadToken removes the per-environment secret-store directory
// and the ArgoCD Application from the GitOps repo and commits the deletion.
// It is a no-op if neither exists.
func (p *Publisher) DeleteSealedReadToken(ctx context.Context, env string) error {
	if env == "" {
		return fmt.Errorf("DeleteSealedReadToken: env is required")
	}
	return p.withClonedRepo(ctx, func(repoDir string) error {
		storesDir := filepath.Join(repoDir, "gitops-output", "_infra", "secret-stores", env)
		appFile := filepath.Join(repoDir, "gitops-output", "_infra", "secrets-"+env+"-app.yaml")

		removedAny := false

		if _, err := os.Stat(storesDir); err == nil {
			if err := os.RemoveAll(storesDir); err != nil {
				return fmt.Errorf("removing secret-store dir for env %s: %w", env, err)
			}
			removedAny = true
		}

		if _, err := os.Stat(appFile); err == nil {
			if err := os.Remove(appFile); err != nil {
				return fmt.Errorf("removing secrets app for env %s: %w", env, err)
			}
			removedAny = true
		}

		if !removedAny {
			return nil
		}
		return p.commitAndPush(ctx, repoDir, fmt.Sprintf("feat(secrets): remove secret-store for env=%s", env))
	})
}

// buildSecretStoreArgoApp returns a minimal ArgoCD Application that syncs
// the per-env secret-stores directory to the given target cluster.
func buildSecretStoreArgoApp(env, repoURL, branch, destServer string) string {
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: secrets-%s
  namespace: argocd
  labels:
    app.kubernetes.io/managed-by: suparship
    suparship.io/env: %s
spec:
  project: default
  source:
    repoURL: %s
    targetRevision: %s
    path: gitops-output/_infra/secret-stores/%s
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
`, env, env, repoURL, branch, env, destServer, secrets.OnePasswordRemoteNamespace)
}
