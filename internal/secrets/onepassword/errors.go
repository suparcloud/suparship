package onepassword

import "fmt"

// UserMessage returns a user-friendly message for known error types.
// This is used by the API handlers to translate internal errors into
// plain-English responses suitable for non-technical org admins.
func UserMessage(err error) string {
	if err == nil {
		return ""
	}

	switch err {
	case ErrTokenInvalid:
		return "The Service Account token is invalid or expired. Please paste a fresh token from 1Password.com."
	case ErrTokenScope:
		return "The token can see vaults outside the configured group. Create a Service Account with access only to the Suparship group vaults."
	case ErrVaultNotFound:
		return "The vault was not found in 1Password. It may have been deleted externally."
	case ErrItemNotFound:
		return "The secret item was not found in the vault."
	case ErrPermissionDeny:
		return "The Service Account token does not have sufficient permissions for this operation."
	case ErrConnectNotReady:
		return "The Connect server is not available for token issuance. Ensure Connect is installed and healthy."
	default:
		return fmt.Sprintf("An unexpected error occurred: %v", err)
	}
}

// PreflightError represents a failure in a provision preflight check.
type PreflightError struct {
	Check   string // e.g., "sa_token", "cluster_registered", "sealed_secrets", "eso_installed", "gitops_repo"
	Message string
}

func (e *PreflightError) Error() string {
	return fmt.Sprintf("preflight check %q failed: %s", e.Check, e.Message)
}

// NewPreflightError creates a typed preflight error.
func NewPreflightError(check, message string) *PreflightError {
	return &PreflightError{Check: check, Message: message}
}
