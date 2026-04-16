// Package registry provides the container registry configuration model and
// persistence for suparship. When configured, suparship creates
// imagePullSecrets in app namespaces so pods can pull from private registries.
package registry

import "errors"

var (
	ErrConfigNotFound = errors.New("registry: configuration not found")
	ErrMissingURL     = errors.New("registry: url is required when enabled")
)

// Config is the container registry configuration persisted in the
// suparship-registry-config ConfigMap.
type Config struct {
	// Enabled controls whether registry integration is active.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// URL is the registry endpoint (e.g. "ghcr.io", "registry.example.com").
	URL string `json:"url" yaml:"url"`
	// Username is the non-secret registry username.
	Username string `json:"username,omitempty" yaml:"username,omitempty"`
	// AuthSecretRef is the name of an existing K8s Secret containing
	// the registry password or .dockerconfigjson.
	AuthSecretRef string `json:"authSecretRef,omitempty" yaml:"authSecretRef,omitempty"`
	// Environments lists which environments need imagePullSecrets
	// from this registry. Empty means all environments.
	Environments []string `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// Validate returns an error if the config is incomplete.
func (c *Config) Validate() error {
	if c.Enabled && c.URL == "" {
		return ErrMissingURL
	}
	return nil
}

// AppliesToEnv returns true if the given environment should receive
// imagePullSecrets from this registry. Returns true for all envs when
// Environments is empty (wildcard).
func (c *Config) AppliesToEnv(envName string) bool {
	if !c.Enabled {
		return false
	}
	if len(c.Environments) == 0 {
		return true
	}
	for _, e := range c.Environments {
		if e == envName {
			return true
		}
	}
	return false
}
