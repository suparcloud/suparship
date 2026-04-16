package secrets

import "errors"

var (
	ErrInvalidBackendType         = errors.New("secrets: unsupported backend type")
	ErrOnePasswordMissingConfig   = errors.New("secrets: 1password backend requires onePassword configuration")
	ErrOnePasswordMissingMode     = errors.New("secrets: 1password mode is required (connect or service-account)")
	ErrOnePasswordInvalidMode     = errors.New("secrets: 1password mode must be 'connect' or 'service-account'")
	ErrOnePasswordMissingConnectHost = errors.New("secrets: 1password connect mode requires connectHost")
	ErrOnePasswordMissingSecret   = errors.New("secrets: 1password requires existingSecret for the token")
)
