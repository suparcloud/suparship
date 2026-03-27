package auth

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/client-go/kubernetes"
)

// Sentinel errors for authentication outcomes.
var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrNotConfigured      = errors.New("admin credentials not configured")
)

// Authenticator validates user credentials.
type Authenticator interface {
	Authenticate(ctx context.Context, username, password string) (*Credentials, error)
}

// K8sAuthenticator validates credentials against the admin Secret stored in
// Kubernetes.
type K8sAuthenticator struct {
	client kubernetes.Interface
}

// NewK8sAuthenticator creates an Authenticator backed by the admin K8s Secret.
func NewK8sAuthenticator(client kubernetes.Interface) *K8sAuthenticator {
	return &K8sAuthenticator{client: client}
}

// Authenticate verifies the username and password against the admin Secret.
func (a *K8sAuthenticator) Authenticate(ctx context.Context, username, password string) (*Credentials, error) {
	creds, err := GetAdminSecret(ctx, a.client)
	if err != nil {
		return nil, fmt.Errorf("reading admin credentials: %w", err)
	}
	if creds == nil {
		return nil, ErrNotConfigured
	}
	if creds.Username != username {
		return nil, ErrInvalidCredentials
	}
	if err := creds.Verify(password); err != nil {
		return nil, ErrInvalidCredentials
	}
	return creds, nil
}
