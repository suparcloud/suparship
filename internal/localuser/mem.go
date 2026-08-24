package localuser

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/suparcloud/suparship/internal/auth"
)

// MemStore is the in-memory Store used by fake/dev mode and tests. Same
// semantics as KubeStore, guarded by one mutex (which trivially gives the
// single-winner claim on redemption).
type MemStore struct {
	mu       sync.Mutex
	users    map[string]userRecord
	invites  map[string]inviteRecord
	reserved map[string]bool
	now      func() time.Time
}

// NewMemStore returns an empty MemStore; reserved lists usernames CreateUser
// refuses (the admin credential).
func NewMemStore(reserved ...string) *MemStore {
	res := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		if r != "" {
			res[r] = true
		}
	}
	return &MemStore{
		users:    map[string]userRecord{},
		invites:  map[string]inviteRecord{},
		reserved: res,
		now:      time.Now,
	}
}

func (s *MemStore) CreateUser(_ context.Context, username string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserved[username] {
		return ErrReserved
	}
	if _, ok := s.users[username]; ok {
		return ErrExists
	}
	s.users[username] = userRecord{CreatedAt: s.now().UTC()}
	return nil
}

func (s *MemStore) IssueInvite(_ context.Context, username string) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[username]; !ok {
		return "", time.Time{}, ErrNotFound
	}
	id, secret, plaintext, err := generateInvite()
	if err != nil {
		return "", time.Time{}, err
	}
	for k, rec := range s.invites {
		if rec.Username == username {
			delete(s.invites, k)
		}
	}
	expires := s.now().UTC().Add(InviteTTL)
	s.invites[id] = inviteRecord{Username: username, SecretHash: hashSecret(secret), CreatedAt: s.now().UTC(), ExpiresAt: expires}
	return plaintext, expires, nil
}

// lookupInvite must be called with the lock held.
func (s *MemStore) lookupInvite(plaintext string) (id string, rec inviteRecord, err error) {
	id, secret, ok := parseInvite(plaintext)
	if !ok {
		return "", inviteRecord{}, ErrInvalidInvite
	}
	rec, found := s.invites[id]
	if !found || !secretMatches(secret, rec.SecretHash) || rec.expired(s.now()) {
		return "", inviteRecord{}, ErrInvalidInvite
	}
	return id, rec, nil
}

func (s *MemStore) InviteUsername(_ context.Context, plaintext string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, rec, err := s.lookupInvite(plaintext)
	if err != nil {
		return "", err
	}
	return rec.Username, nil
}

func (s *MemStore) RedeemInvite(_ context.Context, plaintext, password string) (string, error) {
	if len(password) < MinPasswordLen {
		return "", ErrWeakPassword
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, rec, err := s.lookupInvite(plaintext)
	if err != nil {
		return "", err
	}
	delete(s.invites, id) // claim: spent regardless of what follows
	user, ok := s.users[rec.Username]
	if !ok {
		return "", ErrInvalidInvite
	}
	user.PasswordHash = hash
	s.users[rec.Username] = user
	return rec.Username, nil
}

func (s *MemStore) Authenticate(_ context.Context, username, password string) error {
	s.mu.Lock()
	rec, ok := s.users[username]
	s.mu.Unlock()
	if !ok || rec.Disabled || rec.PasswordHash == "" {
		return auth.ErrInvalidCredentials
	}
	if err := auth.VerifyPassword(rec.PasswordHash, password); err != nil {
		return auth.ErrInvalidCredentials
	}
	return nil
}

func (s *MemStore) List(_ context.Context) ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inviteExpiry := map[string]time.Time{}
	for _, rec := range s.invites {
		inviteExpiry[rec.Username] = rec.ExpiresAt
	}
	out := make([]User, 0, len(s.users))
	for username, rec := range s.users {
		u := User{Username: username, CreatedAt: rec.CreatedAt, Disabled: rec.Disabled, HasPassword: rec.PasswordHash != ""}
		if exp, ok := inviteExpiry[username]; ok {
			e := exp
			u.InviteExpiresAt = &e
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

func (s *MemStore) Delete(_ context.Context, username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[username]; !ok {
		return ErrNotFound
	}
	delete(s.users, username)
	for k, rec := range s.invites {
		if rec.Username == username {
			delete(s.invites, k)
		}
	}
	return nil
}
