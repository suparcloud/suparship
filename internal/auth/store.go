package auth

import "fmt"

// Default Kubernetes Secret coordinates for admin credentials. These are
// fallbacks; callers may override every field via SecretRef so operators
// can point suparship at a Secret managed by ESO/SealedSecrets/etc.
const (
	DefaultSecretNamespace       = "suparship-system"
	DefaultSecretName            = "suparship-admin-auth"
	DefaultSecretKeyUser         = "username"
	DefaultSecretKeyPasswordHash = "password-hash"
)

// SecretRef locates the Kubernetes Secret that stores admin credentials
// and names the keys within it.
type SecretRef struct {
	Namespace       string
	Name            string
	UsernameKey     string
	PasswordHashKey string
}

// DefaultSecretRef returns a SecretRef populated entirely with defaults.
func DefaultSecretRef() SecretRef {
	return SecretRef{
		Namespace:       DefaultSecretNamespace,
		Name:            DefaultSecretName,
		UsernameKey:     DefaultSecretKeyUser,
		PasswordHashKey: DefaultSecretKeyPasswordHash,
	}
}

// WithDefaults returns a copy of r with empty fields filled from defaults.
func (r SecretRef) WithDefaults() SecretRef {
	if r.Namespace == "" {
		r.Namespace = DefaultSecretNamespace
	}
	if r.Name == "" {
		r.Name = DefaultSecretName
	}
	if r.UsernameKey == "" {
		r.UsernameKey = DefaultSecretKeyUser
	}
	if r.PasswordHashKey == "" {
		r.PasswordHashKey = DefaultSecretKeyPasswordHash
	}
	return r
}

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
// stringData field, using the key names from ref.
func (c *Credentials) SecretData(ref SecretRef) map[string]string {
	ref = ref.WithDefaults()
	return map[string]string{
		ref.UsernameKey:     c.Username,
		ref.PasswordHashKey: c.PasswordHash,
	}
}

// CredentialsFromSecretData reconstructs Credentials from a Kubernetes Secret's
// data map (already base64-decoded values), using the key names from ref.
func CredentialsFromSecretData(data map[string]string, ref SecretRef) (*Credentials, error) {
	ref = ref.WithDefaults()

	username, ok := data[ref.UsernameKey]
	if !ok || username == "" {
		return nil, fmt.Errorf("secret missing key %q", ref.UsernameKey)
	}

	hash, ok := data[ref.PasswordHashKey]
	if !ok || hash == "" {
		return nil, fmt.Errorf("secret missing key %q", ref.PasswordHashKey)
	}

	return &Credentials{
		Username:     username,
		PasswordHash: hash,
	}, nil
}
