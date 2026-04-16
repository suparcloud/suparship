// Package bootstrap reconciles Helm-provided ConfigMaps on server startup.
// When suparship is installed via Helm, the chart creates ConfigMaps for
// GitOps config, registry config, org config, and cluster definitions.
// This package reads those ConfigMaps and logs their status so operators
// can verify the Helm values were applied correctly.
//
// The reconciliation is idempotent and read-only — it never overwrites
// user-modified ConfigMaps. It only logs what was found and reports any
// gaps between what was expected (from env vars) and what exists.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/registry"
)

// Result holds the outcome of the bootstrap reconciliation.
type Result struct {
	GitOpsConfigured    bool
	RegistryConfigured  bool
	GitOpsProvider      string
	GitOpsRepoURL       string
	RegistryURL         string
	Warnings            []string
}

// Reconcile reads Helm-provided ConfigMaps and logs their status.
// It returns a summary of what was found. This never writes data —
// the Helm chart and API handlers are the only writers.
func Reconcile(ctx context.Context, client kubernetes.Interface, logger *slog.Logger) Result {
	result := Result{}

	reconcileGitOps(ctx, client, logger, &result)
	reconcileRegistry(ctx, client, logger, &result)

	if len(result.Warnings) > 0 {
		for _, w := range result.Warnings {
			logger.Warn("bootstrap warning", "msg", w)
		}
	}

	return result
}

func reconcileGitOps(ctx context.Context, client kubernetes.Interface, logger *slog.Logger, result *Result) {
	store := gitops.NewConfigStore(client)
	cfg, err := store.Get(ctx)
	if err != nil {
		if err == gitops.ErrConfigNotFound {
			logger.Info("bootstrap: gitops config not found — configure via Helm values or settings UI")
			result.Warnings = append(result.Warnings, "GitOps repository not configured")
			return
		}
		logger.Error("bootstrap: failed to read gitops config", "error", err)
		result.Warnings = append(result.Warnings, fmt.Sprintf("gitops config read error: %v", err))
		return
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

func reconcileRegistry(ctx context.Context, client kubernetes.Interface, logger *slog.Logger, result *Result) {
	store := registry.NewStore(client)
	cfg, err := store.Get(ctx)
	if err != nil {
		if err == registry.ErrConfigNotFound {
			logger.Info("bootstrap: registry config not found — disabled or not configured via Helm")
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
		"environments", cfg.Environments,
	)

	if cfg.URL == "" {
		result.Warnings = append(result.Warnings, "Registry is enabled but URL is empty")
	}
}

// FormatSummary returns a human-readable summary for log output.
func FormatSummary(r Result) string {
	gitops := "not configured"
	if r.GitOpsConfigured {
		gitops = fmt.Sprintf("%s (%s)", r.GitOpsRepoURL, r.GitOpsProvider)
	}

	reg := "disabled"
	if r.RegistryConfigured {
		reg = r.RegistryURL
	}

	return fmt.Sprintf("gitops=%s registry=%s warnings=%d", gitops, reg, len(r.Warnings))
}
