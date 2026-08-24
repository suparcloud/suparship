package auth

import (
	"context"
	"errors"
)

// Chain tries each Authenticator in order, falling through on
// ErrInvalidCredentials / ErrNotConfigured so the admin-secret authenticator
// can be composed with the local-user store (and any future source) behind
// the same interface. Any other error aborts the chain. When every link
// falls through, the chain reports ErrInvalidCredentials — unless every link
// reported ErrNotConfigured, which is preserved so login can still show the
// "not configured" state.
type Chain []Authenticator

func (c Chain) Authenticate(ctx context.Context, username, password string) (*Credentials, error) {
	allNotConfigured := len(c) > 0
	for _, a := range c {
		if a == nil {
			continue
		}
		creds, err := a.Authenticate(ctx, username, password)
		if err == nil {
			return creds, nil
		}
		switch {
		case errors.Is(err, ErrNotConfigured):
			// fall through, still counts as not-configured
		case errors.Is(err, ErrInvalidCredentials):
			allNotConfigured = false
		default:
			return nil, err
		}
	}
	if allNotConfigured {
		return nil, ErrNotConfigured
	}
	return nil, ErrInvalidCredentials
}
