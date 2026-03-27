// Package auth provides authentication primitives for suparship.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const (
	// bcryptCost is the work factor for bcrypt hashing. 12 balances security
	// and speed for a single-admin system.
	bcryptCost = 12

	// passwordBytes is the number of random bytes used to generate a password.
	// 24 bytes → 32 base64 characters.
	passwordBytes = 24
)

// GeneratePassword returns a cryptographically random password suitable for
// an initial bootstrap admin credential.
func GeneratePassword() (string, error) {
	buf := make([]byte, passwordBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating random password: %w", err)
	}
	return base64.URLEncoding.EncodeToString(buf), nil
}

// HashPassword returns a bcrypt hash of the given plaintext password.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword checks a plaintext password against a bcrypt hash.
// Returns nil on success, or an error if the password does not match.
func VerifyPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
