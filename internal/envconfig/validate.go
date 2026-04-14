package envconfig

import (
	"fmt"
	"regexp"
)

// envKeyRE is the regex that valid environment variable names must match.
var envKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateEnvConfig returns an error if c contains invalid data.
// It checks:
//   - All Vars keys are valid env var names
//   - All SecretRefs have all required fields set
//   - SecretRef.Provider is one of the known providers
//   - SecretRef.EnvKey is a valid env var name
func ValidateEnvConfig(c EnvConfig) error {
	for k := range c.Vars {
		if !envKeyRE.MatchString(k) {
			return fmt.Errorf("invalid env var name %q: must match ^[A-Za-z_][A-Za-z0-9_]*$", k)
		}
	}
	for i, ref := range c.SecretRefs {
		if err := ValidateSecretRef(ref); err != nil {
			return fmt.Errorf("secretRefs[%d]: %w", i, err)
		}
	}
	return nil
}

// ValidateSecretRef returns an error if ref is missing required fields or
// uses an unknown provider.
func ValidateSecretRef(ref SecretRef) error {
	if ref.Provider == "" {
		return fmt.Errorf("provider must not be empty")
	}
	if !KnownProviders[ref.Provider] {
		return fmt.Errorf("unknown provider %q: must be one of k8s, vault, aws-sm", ref.Provider)
	}
	if ref.Name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if ref.Key == "" {
		return fmt.Errorf("key must not be empty")
	}
	if ref.EnvKey == "" {
		return fmt.Errorf("envKey must not be empty")
	}
	if !envKeyRE.MatchString(ref.EnvKey) {
		return fmt.Errorf("invalid envKey %q: must match ^[A-Za-z_][A-Za-z0-9_]*$", ref.EnvKey)
	}
	return nil
}
