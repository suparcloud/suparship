// Package registry provides the container registry configuration model and
// persistence for suparship. When configured, suparship creates
// imagePullSecrets in app namespaces so pods can pull from private registries.
package registry

import (
	"errors"
	"fmt"
	"strings"
)

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
	// CredentialExpiresAt is an ISO 8601 timestamp for credential health warnings.
	CredentialExpiresAt string `json:"credentialExpiresAt,omitempty" yaml:"credentialExpiresAt,omitempty"`
	// Environments lists which environments need imagePullSecrets
	// from this registry. Empty means all environments.
	Environments []string `json:"environments,omitempty" yaml:"environments,omitempty"`
	// Insecure disables TLS verification for registry operations (e.g.
	// Kargo Warehouse image subscriptions). Required when using an
	// HTTP-only registry such as a local dev registry.
	Insecure bool `json:"insecure,omitempty" yaml:"insecure,omitempty"`
}

// Validate returns an error if the config is incomplete or the registry URL
// is malformed. The URL is a registry host (e.g. "ghcr.io",
// "registry.example.com:5000"), NOT a scheme'd URL — a pasted "https://..."
// or stray whitespace breaks Kargo Warehouse image subscriptions and
// imagePullSecret generation, so reject it at the door.
func (c *Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.URL == "" {
		return ErrMissingURL
	}
	if strings.TrimSpace(c.URL) != c.URL {
		return fmt.Errorf("registry: url %q has leading or trailing whitespace", c.URL)
	}
	if strings.Contains(c.URL, "://") {
		return fmt.Errorf("registry: url must be a host without a scheme (e.g. \"ghcr.io\", not %q)", c.URL)
	}
	if strings.ContainsAny(c.URL, " \t") {
		return fmt.Errorf("registry: url %q must not contain spaces", c.URL)
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
