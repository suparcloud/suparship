package secrets

import "fmt"

// VaultIDForScope returns the 1Password vault UUID configured for the scope.
// It is the resolver the 1Password VaultStore uses to locate the vault a
// scope's items live in. Returns an error when no vault is provisioned.
//
// The k8s backend ignores this (its "vault" is a namespace derived from the
// scope, see VaultName); only the 1Password backend needs vault UUIDs.
func (c BackendConfig) VaultIDForScope(scope Scope) (string, error) {
	ref := c.FindVault(scope)
	if ref == nil || ref.VaultID == "" {
		return "", fmt.Errorf("secrets: no vault provisioned for %s", VaultName(scope))
	}
	return ref.VaultID, nil
}
