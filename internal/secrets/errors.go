package secrets

import "errors"

var (
	ErrInvalidBackendType = errors.New("secrets: unsupported backend type")
	ErrStaleVersion       = errors.New("secrets: item version conflict (stale write)")
)
