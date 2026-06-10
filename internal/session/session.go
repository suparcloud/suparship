// Package session provides an in-memory session store for suparship.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const idBytes = 32

// Session represents an authenticated user session.
type Session struct {
	ID       string
	Username string
	Role     string
	// Groups holds the IdP group claims from an SSO login, matched against
	// RoleBinding.Group for group-aware authorization. Nil for local
	// (password) logins, which authorize by team membership.
	Groups    []string
	ExpiresAt time.Time
}

// Store is a concurrency-safe in-memory session store.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	ttl      time.Duration
}

// NewStore creates a Store with the given session lifetime.
func NewStore(ttl time.Duration) *Store {
	return &Store{
		sessions: make(map[string]*Session),
		ttl:      ttl,
	}
}

// Create generates a new session for the given user and role (no IdP groups —
// used by local password login).
func (s *Store) Create(username, role string) (*Session, error) {
	return s.CreateWithGroups(username, role, nil)
}

// CreateWithGroups generates a new session carrying the user's IdP group claims
// (from an SSO login), used for group-aware authorization.
func (s *Store) CreateWithGroups(username, role string, groups []string) (*Session, error) {
	id, err := generateID()
	if err != nil {
		return nil, fmt.Errorf("generating session ID: %w", err)
	}

	sess := &Session{
		ID:        id,
		Username:  username,
		Role:      role,
		Groups:    groups,
		ExpiresAt: time.Now().Add(s.ttl),
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	return sess, nil
}

// Get retrieves a session by ID. Returns nil and false if not found or expired.
func (s *Store) Get(id string) (*Session, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok {
		return nil, false
	}

	if time.Now().After(sess.ExpiresAt) {
		s.Delete(id)
		return nil, false
	}

	return sess, true
}

// Delete removes a session by ID.
func (s *Store) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func generateID() (string, error) {
	buf := make([]byte, idBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
