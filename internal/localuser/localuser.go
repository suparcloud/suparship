// Package localuser implements platform-local basic-auth users provisioned
// through one-time invite links. It is the escape hatch for orgs without an
// IdP: an org admin creates a user and hands them an invite link (delivery is
// manual); the user sets their password on first use; the link dies on
// redemption. Re-issuing an invite for an existing user doubles as password
// reset and invalidates any older outstanding link.
//
// Invite token layout mirrors internal/token (supin_<id:16hex><secret:48hex>):
// the id is the public storage key, only the SHA-256 of the high-entropy
// secret is persisted, and comparison is constant-time. User PASSWORDS are
// low-entropy and therefore bcrypt-hashed via internal/auth.
package localuser

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/suparcloud/suparship/internal/auth"
)

// InvitePrefix marks suparship invite tokens (recognisability + secret
// scanners), distinct from API tokens' "supat_".
const InvitePrefix = "supin_"

const (
	idHexLen     = 16 // 8 random bytes
	secretHexLen = 48 // 24 random bytes
)

// InviteTTL is how long an invite link stays redeemable.
const InviteTTL = 7 * 24 * time.Hour

// MinPasswordLen is the minimum accepted password length.
const MinPasswordLen = 8

// Sentinel errors.
var (
	// ErrExists is returned by CreateUser for a username that already has a
	// local-user record.
	ErrExists = errors.New("user already exists")
	// ErrReserved is returned by CreateUser for a username the store must not
	// manage (the admin credential — the authenticator chain tries the admin
	// Secret first, so a local user with that name could never log in).
	ErrReserved = errors.New("username is reserved")
	// ErrNotFound is returned when the named user has no record.
	ErrNotFound = errors.New("user not found")
	// ErrInvalidInvite covers every unusable invite — malformed, unknown,
	// already redeemed, or expired — so responses never reveal which.
	ErrInvalidInvite = errors.New("invite link is invalid or has expired")
	// ErrWeakPassword is returned by RedeemInvite for a too-short password.
	ErrWeakPassword = fmt.Errorf("password must be at least %d characters", MinPasswordLen)
)

// User is the non-sensitive listing view of a local user.
type User struct {
	Username  string
	CreatedAt time.Time
	Disabled  bool
	// HasPassword is false until the first invite is redeemed.
	HasPassword bool
	// InviteExpiresAt is set when an invite is outstanding for this user.
	InviteExpiresAt *time.Time
}

// Store manages local users and their invites. Implementations must be safe
// for concurrent use.
type Store interface {
	// CreateUser records a new user with no password. ErrExists / ErrReserved.
	CreateUser(ctx context.Context, username string) error
	// IssueInvite mints a one-time invite for an existing user, invalidating
	// any older outstanding invite (re-invite = password reset). Returns the
	// plaintext token (shown once, never stored) and its expiry. ErrNotFound.
	IssueInvite(ctx context.Context, username string) (plaintext string, expiresAt time.Time, err error)
	// InviteUsername resolves a presented invite to its username WITHOUT
	// consuming it — for the set-password page greeting. ErrInvalidInvite.
	InviteUsername(ctx context.Context, plaintext string) (string, error)
	// RedeemInvite atomically consumes the invite (a second redemption fails
	// even under races — the claim happens before the password write) and
	// sets the user's password. Returns the username. ErrInvalidInvite,
	// ErrWeakPassword.
	RedeemInvite(ctx context.Context, plaintext, password string) (username string, err error)
	// Authenticate verifies a username/password pair. auth.ErrInvalidCredentials
	// for unknown, disabled, password-less, or mismatched users.
	Authenticate(ctx context.Context, username, password string) error
	// List returns every local user, sorted by username.
	List(ctx context.Context) ([]User, error)
	// Delete removes the user and any outstanding invite. ErrNotFound.
	Delete(ctx context.Context, username string) error
}

// userRecord is the persisted per-user document. Username lives INSIDE the
// record because the K8s storage key is a hash of it: Secret data keys are
// restricted to [-._a-zA-Z0-9]+, which excludes '@' — and usernames are
// usually emails.
type userRecord struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	Disabled     bool      `json:"disabled,omitempty"`
}

// userKey derives the K8s-Secret-safe storage key for a username: "u-" plus
// 40 hex chars of its SHA-256. Deterministic (O(1) lookup) and always within
// the allowed key charset regardless of the username's shape.
func userKey(username string) string {
	sum := sha256.Sum256([]byte(username))
	return "u-" + hex.EncodeToString(sum[:])[:40]
}

// inviteRecord is the persisted per-invite document, keyed by the token id.
type inviteRecord struct {
	Username   string    `json:"username"`
	SecretHash string    `json:"secretHash"`
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func (r inviteRecord) expired(now time.Time) bool { return now.After(r.ExpiresAt) }

// generateInvite mints a new invite token: public id, raw secret, plaintext.
func generateInvite() (id, secret, plaintext string, err error) {
	idBuf := make([]byte, idHexLen/2)
	if _, err = rand.Read(idBuf); err != nil {
		return "", "", "", fmt.Errorf("generating invite id: %w", err)
	}
	secBuf := make([]byte, secretHexLen/2)
	if _, err = rand.Read(secBuf); err != nil {
		return "", "", "", fmt.Errorf("generating invite secret: %w", err)
	}
	id = hex.EncodeToString(idBuf)
	secret = hex.EncodeToString(secBuf)
	return id, secret, InvitePrefix + id + secret, nil
}

// parseInvite splits a plaintext invite into id and secret; ok is false for
// anything structurally invalid.
func parseInvite(plaintext string) (id, secret string, ok bool) {
	body, found := strings.CutPrefix(plaintext, InvitePrefix)
	if !found || len(body) != idHexLen+secretHexLen {
		return "", "", false
	}
	return body[:idHexLen], body[idHexLen:], true
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func secretMatches(secret, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(storedHash)) == 1
}

// validateUsername enforces the minimal shape team-membership matching relies
// on: non-empty, no whitespace (Team.Members are matched by exact string).
func validateUsername(username string) error {
	if username == "" {
		return fmt.Errorf("username must not be empty")
	}
	if strings.ContainsAny(username, " \t\n") {
		return fmt.Errorf("username must not contain whitespace")
	}
	return nil
}

// AsAuthenticator adapts a Store to the auth.Authenticator interface for use
// in an auth.Chain after the admin-secret authenticator.
func AsAuthenticator(s Store) auth.Authenticator { return storeAuthenticator{s} }

type storeAuthenticator struct{ s Store }

func (a storeAuthenticator) Authenticate(ctx context.Context, username, password string) (*auth.Credentials, error) {
	if err := a.s.Authenticate(ctx, username, password); err != nil {
		return nil, err
	}
	return &auth.Credentials{Username: username}, nil
}
