package token

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Sentinel errors returned by Store implementations.
var (
	// ErrInvalid means the presented bearer token is malformed or unknown.
	ErrInvalid = errors.New("invalid api token")
	// ErrExpired means the token resolved but is past its expiry.
	ErrExpired = errors.New("api token expired")
	// ErrNotFound means no token with the given id exists in the project.
	ErrNotFound = errors.New("api token not found")
)

// record is the persisted form: token metadata plus the secret hash. It is
// internal to the package — handlers only ever see Metadata.
type record struct {
	Metadata
	SecretHash string `json:"secretHash"`
}

// Store persists and validates project API tokens.
type Store interface {
	// Issue mints a token for meta (which must carry Name, Project, Role,
	// CreatedBy, CreatedAt and optional ExpiresAt). It generates and sets the
	// ID, persists the secret hash, and returns the one-time plaintext token
	// alongside the stored metadata.
	Issue(ctx context.Context, meta Metadata) (plaintext string, out Metadata, err error)
	// Resolve validates a raw bearer token, returning its metadata. It returns
	// ErrInvalid for unknown/malformed tokens and ErrExpired for expired ones.
	Resolve(ctx context.Context, plaintext string) (Metadata, error)
	// List returns metadata for every token scoped to project.
	List(ctx context.Context, project string) ([]Metadata, error)
	// Delete revokes the token with the given id within project. It returns
	// ErrNotFound when no such token exists.
	Delete(ctx context.Context, project, id string) error
}

// MemStore is an in-memory Store for tests.
type MemStore struct {
	mu      sync.RWMutex
	records map[string]record // keyed by token id
}

// NewMemStore returns an empty in-memory token store.
func NewMemStore() *MemStore {
	return &MemStore{records: make(map[string]record)}
}

func (s *MemStore) Issue(_ context.Context, meta Metadata) (string, Metadata, error) {
	id, secret, plaintext, err := generate()
	if err != nil {
		return "", Metadata{}, err
	}
	meta.ID = id
	s.mu.Lock()
	s.records[id] = record{Metadata: meta, SecretHash: hashSecret(secret)}
	s.mu.Unlock()
	return plaintext, meta, nil
}

func (s *MemStore) Resolve(_ context.Context, plaintext string) (Metadata, error) {
	id, secret, ok := parse(plaintext)
	if !ok {
		return Metadata{}, ErrInvalid
	}
	s.mu.RLock()
	rec, found := s.records[id]
	s.mu.RUnlock()
	if !found || !secretMatches(secret, rec.SecretHash) {
		return Metadata{}, ErrInvalid
	}
	if rec.Expired(time.Now()) {
		return Metadata{}, ErrExpired
	}
	return rec.Metadata, nil
}

func (s *MemStore) List(_ context.Context, project string) ([]Metadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Metadata, 0)
	for _, rec := range s.records {
		if rec.Project == project {
			out = append(out, rec.Metadata)
		}
	}
	return out, nil
}

func (s *MemStore) Delete(_ context.Context, project, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, found := s.records[id]
	if !found || rec.Project != project {
		return ErrNotFound
	}
	delete(s.records, id)
	return nil
}
