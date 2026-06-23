// Package token implements project-scoped API tokens. A token grants a fixed
// RBAC role on a single project and authenticates API calls via the
// Authorization: Bearer header, independently of the human who minted it.
//
// Token string layout (returned to the caller exactly once, never stored):
//
//	supat_<id><secret>
//	      \__/\______/
//	       16   48 hex chars
//
// The id is a public, indexable handle (the storage key and the value shown in
// listings / used for revocation). The secret is the high-entropy part; only
// its SHA-256 hash is persisted. Because the secret carries full random
// entropy, a fast hash is sufficient — unlike low-entropy passwords, it is not
// brute-forceable, so bcrypt's deliberate slowness buys nothing here.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Prefix marks suparship API tokens. It aids recognisability and lets secret
// scanners match leaked credentials.
const Prefix = "supat_"

const (
	idHexLen     = 16 // 8 random bytes
	secretHexLen = 48 // 24 random bytes
)

// Metadata is the non-sensitive record describing a token. It is safe to return
// in API responses and listings; it never contains the secret or its hash.
type Metadata struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Project   string     `json:"project"`
	Role      string     `json:"role"`
	CreatedBy string     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// Expired reports whether the token has a deadline that now is past.
func (m Metadata) Expired(now time.Time) bool {
	return m.ExpiresAt != nil && now.After(*m.ExpiresAt)
}

// generate mints a new token, returning the public id, the raw secret, and the
// full plaintext string to hand to the caller. The plaintext is never stored.
func generate() (id, secret, plaintext string, err error) {
	idBuf := make([]byte, idHexLen/2)
	if _, err = rand.Read(idBuf); err != nil {
		return "", "", "", fmt.Errorf("generating token id: %w", err)
	}
	secBuf := make([]byte, secretHexLen/2)
	if _, err = rand.Read(secBuf); err != nil {
		return "", "", "", fmt.Errorf("generating token secret: %w", err)
	}
	id = hex.EncodeToString(idBuf)
	secret = hex.EncodeToString(secBuf)
	return id, secret, Prefix + id + secret, nil
}

// parse splits a plaintext token into its id and secret. ok is false for any
// string that is not a structurally valid suparship token.
func parse(plaintext string) (id, secret string, ok bool) {
	body, found := strings.CutPrefix(plaintext, Prefix)
	if !found || len(body) != idHexLen+secretHexLen {
		return "", "", false
	}
	return body[:idHexLen], body[idHexLen:], true
}

// hashSecret returns the hex-encoded SHA-256 of a token's secret part.
func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// secretMatches compares a presented secret against a stored hash in constant
// time, guarding against timing side channels.
func secretMatches(secret, storedHash string) bool {
	return subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(storedHash)) == 1
}
