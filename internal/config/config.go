// Package config loads and exposes suparship startup configuration from
// environment variables.
//
// This package handles process-level configuration only: runtime mode and
// server infrastructure flags. Domain configuration (GitOps, registry, org)
// lives in Kubernetes ConfigMaps and is read via store interfaces at runtime.
//
// Legacy SUPARSHIP_GITOPS_* environment variables are read by
// LoadBootstrapEnv and used exclusively for one-time seeding of the
// GitOps ConfigMap when it does not yet exist (bootstrap path).
package config

import "os"

// RuntimeMode describes how the server connects to (or simulates) a cluster.
type RuntimeMode string

const (
	// ModeFake runs entirely in-memory with seeded demo data.  No Kubernetes
	// cluster is required.  This is the default mode for local contributor
	// work on UI and API features.
	//
	// Activated when SUPARSHIP_DEV_MODE=local OR SUPARSHIP_CLUSTER_MODE=fake.
	ModeFake RuntimeMode = "fake"

	// ModeKubernetes connects to a real Kubernetes cluster via kubeconfig.
	// This mode is used when neither dev-mode env var is set.
	ModeKubernetes RuntimeMode = "kubernetes"
)

// Config holds the resolved startup configuration for the suparship server.
// It contains only process-level settings; domain config (GitOps, registry,
// org) is read from Kubernetes ConfigMaps at runtime.
type Config struct {
	// RuntimeMode indicates whether the server uses fake in-memory data or a
	// real Kubernetes cluster.
	RuntimeMode RuntimeMode

	// RuntimeModeTrigger is the environment variable that caused RuntimeMode
	// to be set to ModeFake.  Empty when RuntimeMode is ModeKubernetes.
	// Used only for the startup log message.
	RuntimeModeTrigger string
}

// Load reads environment variables and returns a resolved Config.
//
// Mode selection rules (first match wins):
//  1. SUPARSHIP_DEV_MODE=local    → ModeFake (primary local-dev switch)
//  2. SUPARSHIP_CLUSTER_MODE=fake → ModeFake (explicit cluster override)
//  3. otherwise                   → ModeKubernetes
func Load() Config {
	if os.Getenv("SUPARSHIP_DEV_MODE") == "local" {
		return Config{
			RuntimeMode:        ModeFake,
			RuntimeModeTrigger: "SUPARSHIP_DEV_MODE=local",
		}
	}
	if os.Getenv("SUPARSHIP_CLUSTER_MODE") == "fake" {
		return Config{
			RuntimeMode:        ModeFake,
			RuntimeModeTrigger: "SUPARSHIP_CLUSTER_MODE=fake",
		}
	}
	return Config{
		RuntimeMode: ModeKubernetes,
	}
}

// BootstrapEnv holds legacy GitOps environment variables used exclusively
// for one-time seeding of the GitOps ConfigMap and credential Secret.
// After the ConfigMap exists, these values are ignored on subsequent boots.
type BootstrapEnv struct {
	GitOpsRepoURL      string
	GitOpsRepoUser     string
	GitOpsRepoPassword string
	ArgoCDRepoURL      string
	KargoGitRepoURL    string
	InsecureRegistry   bool
}

// HasGitOps reports whether the bootstrap env contains a GitOps repo URL.
func (b *BootstrapEnv) HasGitOps() bool {
	return b.GitOpsRepoURL != ""
}

// LoadBootstrapEnv reads legacy SUPARSHIP_GITOPS_* environment variables.
// These are used only by the bootstrap reconciler to seed the GitOps
// ConfigMap when it does not yet exist. In steady state, the ConfigMap
// (managed via Helm install or the settings UI) is the sole source of truth.
func LoadBootstrapEnv() BootstrapEnv {
	return BootstrapEnv{
		GitOpsRepoURL:      os.Getenv("SUPARSHIP_GITOPS_REPO_URL"),
		GitOpsRepoUser:     os.Getenv("SUPARSHIP_GITOPS_REPO_USER"),
		GitOpsRepoPassword: os.Getenv("SUPARSHIP_GITOPS_REPO_PASSWORD"),
		ArgoCDRepoURL:      os.Getenv("SUPARSHIP_ARGOCD_REPO_URL"),
		KargoGitRepoURL:    os.Getenv("SUPARSHIP_KARGO_GIT_REPO_URL"),
		InsecureRegistry:   os.Getenv("SUPARSHIP_INSECURE_REGISTRY") == "true",
	}
}
