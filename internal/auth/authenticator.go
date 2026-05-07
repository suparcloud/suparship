package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
	ref    SecretRef
	logger *slog.Logger
}

// NewK8sAuthenticator creates an Authenticator backed by the admin K8s Secret
// located by ref. Empty fields in ref fall back to defaults.
func NewK8sAuthenticator(client kubernetes.Interface, ref SecretRef) *K8sAuthenticator {
	return &K8sAuthenticator{
		client: client,
		ref:    ref.WithDefaults(),
		logger: slog.Default(),
	}
}

// WithLogger returns a copy of a wired with the given logger. nil logger
// resets to slog.Default().
func (a *K8sAuthenticator) WithLogger(logger *slog.Logger) *K8sAuthenticator {
	if logger == nil {
		logger = slog.Default()
	}
	clone := *a
	clone.logger = logger.With(
		"component", "auth",
		"secret_namespace", a.ref.Namespace,
		"secret_name", a.ref.Name,
	)
	return &clone
}

// Authenticate verifies the username and password against the admin Secret.
// Every decision branch is logged so operators can diagnose login failures
// (wrong Secret name/key, bcrypt mismatch, etc.) from pod logs.
func (a *K8sAuthenticator) Authenticate(ctx context.Context, username, password string) (*Credentials, error) {
	creds, err := GetAdminSecret(ctx, a.client, a.ref)
	if err != nil {
		a.logger.Warn("admin secret unreadable",
			"username", username,
			"error", err,
		)
		return nil, fmt.Errorf("reading admin credentials: %w", err)
	}
	if creds == nil {
		a.logger.Warn("admin secret not found",
			"username", username,
			"hint", "run `suparship admin bootstrap` or pre-create the Secret",
		)
		return nil, ErrNotConfigured
	}
	if creds.Username != username {
		a.logger.Warn("admin login: username mismatch",
			"submitted_username", username,
			"expected_username", creds.Username,
		)
		return nil, ErrInvalidCredentials
	}
	if err := creds.Verify(password); err != nil {
		a.logger.Warn("admin login: bcrypt verification failed",
			"username", username,
			"hint", "password-hash key in Secret may not be a bcrypt hash of the submitted password",
		)
		return nil, ErrInvalidCredentials
	}
	a.logger.Debug("admin login: success", "username", username)
	return creds, nil
}
