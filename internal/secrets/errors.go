package secrets

import "errors"

var (
	ErrInvalidBackendType = errors.New("secrets: unsupported backend type")
	ErrStaleVersion       = errors.New("secrets: item version conflict (stale write)")
	// ErrVaultAddressRequired is returned when the Vault backend is selected
	// without a server address. Only enforced for the ACTIVE backend, so a
	// retained partial config never blocks switching away from it.
	ErrVaultAddressRequired = errors.New("secrets: vault backend requires a server address")
)
