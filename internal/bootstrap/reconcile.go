// Package bootstrap reconciles configuration on server startup.
//
// On first boot the reconciler seeds ConfigMaps from legacy environment
// variables (BootstrapEnv) when no ConfigMap exists yet. On subsequent
// boots the existing ConfigMap is the sole source of truth and env vars
// are ignored. This makes Helm-installed ConfigMaps and UI-saved
// ConfigMaps equally authoritative while preserving a migration path
// for setups that previously relied on SUPARSHIP_GITOPS_* env vars.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/config"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/registry"
)

// Result holds the outcome of the bootstrap reconciliation.
type Result struct {
	GitOpsConfigured    bool
	GitOpsSeededFromEnv bool
	RegistryConfigured  bool
	GitOpsProvider      string
	GitOpsRepoURL       string
	RegistryURL         string
	Warnings            []string
}

// Reconcile checks ConfigMaps for GitOps and registry configuration.
// When a ConfigMap is missing but legacy env vars are set, it seeds the
// ConfigMap from those env vars (one-time bootstrap). Returns a summary.
func Reconcile(ctx context.Context, client kubernetes.Interface, logger *slog.Logger) Result {
	result := Result{}
	env := config.LoadBootstrapEnv()

	reconcileGitOps(ctx, client, logger, &env, &result)
	reconcileRegistry(ctx, client, logger, &env, &result)

	if len(result.Warnings) > 0 {
		for _, w := range result.Warnings {
			logger.Warn("bootstrap warning", "msg", w)
		}
	}

	return result
}

func reconcileGitOps(ctx context.Context, client kubernetes.Interface, logger *slog.Logger, env *config.BootstrapEnv, result *Result) {
	store := gitops.NewConfigStore(client)
	cfg, err := store.Get(ctx)
	if err != nil {
		if err == gitops.ErrConfigNotFound {
			// ConfigMap does not exist. Seed from env vars if available.
			if env.HasGitOps() {
				if seedErr := seedGitOpsFromEnv(ctx, store, env, logger); seedErr != nil {
					logger.Error("bootstrap: failed to seed gitops config from env vars", "error", seedErr)
					result.Warnings = append(result.Warnings, fmt.Sprintf("gitops env seed error: %v", seedErr))
					return
				}
				result.GitOpsSeededFromEnv = true
				// Re-read the just-created ConfigMap so the rest of the flow sees it.
				cfg, err = store.Get(ctx)
				if err != nil {
					logger.Error("bootstrap: failed to re-read seeded gitops config", "error", err)
					result.Warnings = append(result.Warnings, fmt.Sprintf("gitops re-read error: %v", err))
					return
				}
			} else {
				logger.Info("bootstrap: gitops config not found — configure via Helm values or settings UI")
				result.Warnings = append(result.Warnings, "GitOps repository not configured")
				return
			}
		} else {
			logger.Error("bootstrap: failed to read gitops config", "error", err)
			result.Warnings = append(result.Warnings, fmt.Sprintf("gitops config read error: %v", err))
			return
		}
	} else if env.HasGitOps() {
		logger.Info("bootstrap: gitops config found in ConfigMap, ignoring environment variables")
	}

	result.GitOpsConfigured = true
	result.GitOpsProvider = cfg.Provider
	result.GitOpsRepoURL = cfg.RepoURL

	logger.Info("bootstrap: gitops config found",
		"provider", cfg.Provider,
		"repoURL", cfg.RepoURL,
		"branch", cfg.Branch,
		"authSecretRef", cfg.AuthSecretRef,
		"initialized", cfg.Initialized,
	)

	if cfg.RepoURL == "" {
		result.Warnings = append(result.Warnings, "GitOps ConfigMap exists but repoURL is empty")
	}
	if cfg.AuthSecretRef != "" {
		_, _, err := store.GetCredentials(ctx, cfg)
		if err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("GitOps auth secret %q: %v", cfg.AuthSecretRef, err))
		} else {
			logger.Info("bootstrap: gitops credentials verified", "secretRef", cfg.AuthSecretRef)
		}
	}
}

// seedGitOpsFromEnv creates the GitOps ConfigMap and credential Secret
// from legacy environment variables. Called only when the ConfigMap does
// not yet exist.
func seedGitOpsFromEnv(ctx context.Context, store *gitops.ConfigStore, env *config.BootstrapEnv, logger *slog.Logger) error {
	repoCfg := &gitops.RepoConfig{
		Provider:        "generic",
		RepoURL:         env.GitOpsRepoURL,
		Branch:          "main",
		ArgoCDRepoURL:   env.ArgoCDRepoURL,
		KargoGitRepoURL: env.KargoGitRepoURL,
	}
	if err := store.Save(ctx, repoCfg); err != nil {
		return fmt.Errorf("save gitops configmap: %w", err)
	}
	logger.Info("bootstrap: seeded gitops config from environment variables (one-time bootstrap)",
		"repoURL", env.GitOpsRepoURL,
	)

	if env.GitOpsRepoUser != "" || env.GitOpsRepoPassword != "" {
		if err := store.SaveCredentials(ctx, "generic", "", env.GitOpsRepoUser, env.GitOpsRepoPassword); err != nil {
			return fmt.Errorf("save gitops credentials: %w", err)
		}
		// Point the config at the managed secret so downstream code finds creds.
		repoCfg.AuthSecretRef = gitops.ManagedCredentialSecretName
		if err := store.Save(ctx, repoCfg); err != nil {
			return fmt.Errorf("update gitops configmap with authSecretRef: %w", err)
		}
		logger.Info("bootstrap: seeded gitops credentials from environment variables")
	}

	return nil
}

func reconcileRegistry(ctx context.Context, client kubernetes.Interface, logger *slog.Logger, env *config.BootstrapEnv, result *Result) {
	store := registry.NewStore(client)
	cfg, err := store.Get(ctx)
	if err != nil {
		if err == registry.ErrConfigNotFound {
			// If SUPARSHIP_INSECURE_REGISTRY was set but no registry ConfigMap
			// exists, seed a minimal registry config with the insecure flag.
			if env.InsecureRegistry {
				regCfg := &registry.Config{Insecure: true}
				if saveErr := store.Save(ctx, regCfg); saveErr != nil {
					logger.Warn("bootstrap: failed to seed registry insecure flag from env", "error", saveErr)
				} else {
					logger.Info("bootstrap: seeded registry insecure flag from environment variables (one-time bootstrap)")
				}
			} else {
				logger.Info("bootstrap: registry config not found — disabled or not configured via Helm")
			}
			return
		}
		logger.Error("bootstrap: failed to read registry config", "error", err)
		result.Warnings = append(result.Warnings, fmt.Sprintf("registry config read error: %v", err))
		return
	}

	if !cfg.Enabled {
		logger.Info("bootstrap: registry config found but disabled")
		return
	}

	result.RegistryConfigured = true
	result.RegistryURL = cfg.URL

	logger.Info("bootstrap: registry config found",
		"url", cfg.URL,
		"username", cfg.Username,
		"authSecretRef", cfg.AuthSecretRef,
		"insecure", cfg.Insecure,
		"environments", cfg.Environments,
	)

	if cfg.URL == "" {
		result.Warnings = append(result.Warnings, "Registry is enabled but URL is empty")
	}
}

// FormatSummary returns a human-readable summary for log output.
func FormatSummary(r Result) string {
	g := "not configured"
	if r.GitOpsConfigured {
		g = fmt.Sprintf("%s (%s)", r.GitOpsRepoURL, r.GitOpsProvider)
		if r.GitOpsSeededFromEnv {
			g += " [seeded from env]"
		}
	}

	reg := "disabled"
	if r.RegistryConfigured {
		reg = r.RegistryURL
	}

	return fmt.Sprintf("gitops=%s registry=%s warnings=%d", g, reg, len(r.Warnings))
}
