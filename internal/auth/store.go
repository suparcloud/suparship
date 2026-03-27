package auth

import "fmt"

// Kubernetes Secret coordinates for admin credentials.
const (
	SecretNamespace = "suparship-system"
	SecretName      = "suparship-admin-auth"
	SecretKeyUser   = "username"
	SecretKeyHash   = "password-hash"
)

// Credentials holds a username and its bcrypt password hash.
type Credentials struct {
	Username     string
	PasswordHash string
}

// NewBootstrapCredentials creates initial admin credentials with a generated
// random password. It returns the credentials (containing the bcrypt hash) and
// the plaintext password. The plaintext is intended to be shown to the operator
// once and never stored.
func NewBootstrapCredentials(username string) (*Credentials, string, error) {
	if username == "" {
		return nil, "", fmt.Errorf("username must not be empty")
	}

	plaintext, err := GeneratePassword()
	if err != nil {
		return nil, "", fmt.Errorf("generating bootstrap password: %w", err)
	}

	hash, err := HashPassword(plaintext)
	if err != nil {
		return nil, "", fmt.Errorf("hashing bootstrap password: %w", err)
	}

	creds := &Credentials{
		Username:     username,
		PasswordHash: hash,
	}

	return creds, plaintext, nil
}

// Verify checks whether the given plaintext password matches the stored hash.
func (c *Credentials) Verify(password string) error {
	return VerifyPassword(c.PasswordHash, password)
}

// SecretData returns the string data map suitable for a Kubernetes Secret's
// stringData field.
//
// The expected Secret manifest is:
//
//	apiVersion: v1
//	kind: Secret
//	metadata:
//	  name: suparship-admin-auth
//	  namespace: suparship-system
//	type: Opaque
//	stringData:
//	  username: <admin-username>
//	  password-hash: <bcrypt-hash>
func (c *Credentials) SecretData() map[string]string {
	return map[string]string{
		SecretKeyUser: c.Username,
		SecretKeyHash: c.PasswordHash,
	}
}

// CredentialsFromSecretData reconstructs Credentials from a Kubernetes Secret's
// data map (already base64-decoded values).
func CredentialsFromSecretData(data map[string]string) (*Credentials, error) {
	username, ok := data[SecretKeyUser]
	if !ok || username == "" {
		return nil, fmt.Errorf("secret missing key %q", SecretKeyUser)
	}

	hash, ok := data[SecretKeyHash]
	if !ok || hash == "" {
		return nil, fmt.Errorf("secret missing key %q", SecretKeyHash)
	}

	return &Credentials{
		Username:     username,
		PasswordHash: hash,
	}, nil
}
