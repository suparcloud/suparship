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

// Config holds the resolved startup configuration for the suparship server.
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
//  1. SUPARSHIP_DEV_MODE=local  → ModeFake (primary local-dev switch)
//  2. SUPARSHIP_CLUSTER_MODE=fake → ModeFake (explicit cluster override)
//  3. otherwise                 → ModeKubernetes
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
	return Config{RuntimeMode: ModeKubernetes}
}
