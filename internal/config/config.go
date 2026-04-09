// Package config loads and exposes suparship startup configuration from
// environment variables.
//
// Keep this package simple and explicit — no config frameworks, no reflection,
// no magic defaults that change with context.  Each public symbol maps
// directly to a documented environment variable.
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

// GitOpsConfig holds the configuration for the GitOps repository integration.
// All fields are optional; when RepoURL is empty the gitops publisher is
// disabled and app creation only persists to the store (no git commit).
type GitOpsConfig struct {
	// RepoURL is the host-accessible Git URL used for cloning and pushing.
	// Read from SUPARSHIP_GITOPS_REPO_URL.
	RepoURL string
	// RepoUser is the Git username for HTTP basic auth.
	// Read from SUPARSHIP_GITOPS_REPO_USER.
	RepoUser string
	// RepoPassword is the Git password or token.
	// Read from SUPARSHIP_GITOPS_REPO_PASSWORD.
	RepoPassword string
	// ArgoCDRepoURL is the URL ArgoCD uses to sync from the gitops repo
	// (may be an internal cluster URL). Falls back to RepoURL when empty.
	// Read from SUPARSHIP_ARGOCD_REPO_URL.
	ArgoCDRepoURL string
}

// Config holds the resolved startup configuration for the suparship server.
type Config struct {
	// RuntimeMode indicates whether the server uses fake in-memory data or a
	// real Kubernetes cluster.
	RuntimeMode RuntimeMode

	// RuntimeModeTrigger is the environment variable that caused RuntimeMode
	// to be set to ModeFake.  Empty when RuntimeMode is ModeKubernetes.
	// Used only for the startup log message.
	RuntimeModeTrigger string

	// GitOps holds the gitops repository connection details. When
	// GitOps.RepoURL is empty the publisher is disabled.
	GitOps GitOpsConfig
}

// Load reads environment variables and returns a resolved Config.
//
// Mode selection rules (first match wins):
//  1. SUPARSHIP_DEV_MODE=local  → ModeFake (primary local-dev switch)
//  2. SUPARSHIP_CLUSTER_MODE=fake → ModeFake (explicit cluster override)
//  3. otherwise                 → ModeKubernetes
//
// GitOps configuration is always read from environment variables regardless of
// runtime mode:
//   - SUPARSHIP_GITOPS_REPO_URL
//   - SUPARSHIP_GITOPS_REPO_USER
//   - SUPARSHIP_GITOPS_REPO_PASSWORD
//   - SUPARSHIP_ARGOCD_REPO_URL
func Load() Config {
	gitops := GitOpsConfig{
		RepoURL:       os.Getenv("SUPARSHIP_GITOPS_REPO_URL"),
		RepoUser:      os.Getenv("SUPARSHIP_GITOPS_REPO_USER"),
		RepoPassword:  os.Getenv("SUPARSHIP_GITOPS_REPO_PASSWORD"),
		ArgoCDRepoURL: os.Getenv("SUPARSHIP_ARGOCD_REPO_URL"),
	}

	if os.Getenv("SUPARSHIP_DEV_MODE") == "local" {
		return Config{
			RuntimeMode:        ModeFake,
			RuntimeModeTrigger: "SUPARSHIP_DEV_MODE=local",
			GitOps:             gitops,
		}
	}
	if os.Getenv("SUPARSHIP_CLUSTER_MODE") == "fake" {
		return Config{
			RuntimeMode:        ModeFake,
			RuntimeModeTrigger: "SUPARSHIP_CLUSTER_MODE=fake",
			GitOps:             gitops,
		}
	}
	return Config{
		RuntimeMode: ModeKubernetes,
		GitOps:      gitops,
	}
}
