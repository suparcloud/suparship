package localuser

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/auth"
)

// Secret coordinates. Two Secrets keep credentials and invites separately
// mutable: user records are keyed by username, invite records by token id
// (O(1) resolve — the presented token carries its own id).
const (
	SecretNamespace   = "suparship-system"
	UsersSecretName   = "suparship-local-users"
	InvitesSecretName = "suparship-user-invites"
)

const mutateRetries = 5

// KubeStore persists local users and invites in two K8s Secrets. Every write
// is a resourceVersion-pinned read-modify-write with conflict retries, so
// invite consumption is an atomic claim: exactly one redeemer wins.
type KubeStore struct {
	client kubernetes.Interface
	ns     string
	// reserved usernames CreateUser refuses (the admin credential).
	reserved map[string]bool
	now      func() time.Time
}

// NewKubeStore returns a KubeStore. reserved lists usernames that must never
// become local users (pass the admin username).
func NewKubeStore(client kubernetes.Interface, reserved ...string) *KubeStore {
	res := make(map[string]bool, len(reserved))
	for _, r := range reserved {
		if r != "" {
			res[r] = true
		}
	}
	return &KubeStore{client: client, ns: SecretNamespace, reserved: res, now: time.Now}
}

// mutateSecret runs fn against the named Secret's Data under optimistic
// concurrency: fresh read (creating an empty Secret on first use), mutate,
// update pinned to the read resourceVersion, retry on conflict.
func (s *KubeStore) mutateSecret(ctx context.Context, name string, fn func(data map[string][]byte) error) error {
	for range mutateRetries {
		sec, err := s.client.CoreV1().Secrets(s.ns).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			sec = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: s.ns,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "suparship",
						"app.kubernetes.io/component":  "local-users",
					},
				},
				Type: corev1.SecretTypeOpaque,
			}
			if sec.Data == nil {
				sec.Data = map[string][]byte{}
			}
			if err := fn(sec.Data); err != nil {
				return err
			}
			if _, cerr := s.client.CoreV1().Secrets(s.ns).Create(ctx, sec, metav1.CreateOptions{}); cerr != nil {
				if apierrors.IsAlreadyExists(cerr) {
					continue // raced another first write — retry as update
				}
				return fmt.Errorf("creating secret %s/%s: %w", s.ns, name, cerr)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading secret %s/%s: %w", s.ns, name, err)
		}
		if sec.Data == nil {
			sec.Data = map[string][]byte{}
		}
		if err := fn(sec.Data); err != nil {
			return err
		}
		if _, uerr := s.client.CoreV1().Secrets(s.ns).Update(ctx, sec, metav1.UpdateOptions{}); uerr != nil {
			if apierrors.IsConflict(uerr) {
				continue
			}
			return fmt.Errorf("updating secret %s/%s: %w", s.ns, name, uerr)
		}
		return nil
	}
	return fmt.Errorf("updating secret %s/%s: too many conflicts", s.ns, name)
}

// readSecretData returns the named Secret's Data ({} when absent).
func (s *KubeStore) readSecretData(ctx context.Context, name string) (map[string][]byte, error) {
	sec, err := s.client.CoreV1().Secrets(s.ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading secret %s/%s: %w", s.ns, name, err)
	}
	if sec.Data == nil {
		return map[string][]byte{}, nil
	}
	return sec.Data, nil
}

func (s *KubeStore) CreateUser(ctx context.Context, username string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	if s.reserved[username] {
		return ErrReserved
	}
	rec := userRecord{CreatedAt: s.now().UTC()}
	blob, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshalling user record: %w", err)
	}
	return s.mutateSecret(ctx, UsersSecretName, func(data map[string][]byte) error {
		if _, ok := data[username]; ok {
			return ErrExists
		}
		data[username] = blob
		return nil
	})
}

func (s *KubeStore) IssueInvite(ctx context.Context, username string) (string, time.Time, error) {
	users, err := s.readSecretData(ctx, UsersSecretName)
	if err != nil {
		return "", time.Time{}, err
	}
	if _, ok := users[username]; !ok {
		return "", time.Time{}, ErrNotFound
	}

	id, secret, plaintext, err := generateInvite()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := s.now().UTC().Add(InviteTTL)
	rec := inviteRecord{Username: username, SecretHash: hashSecret(secret), CreatedAt: s.now().UTC(), ExpiresAt: expires}
	blob, err := json.Marshal(rec)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshalling invite record: %w", err)
	}
	err = s.mutateSecret(ctx, InvitesSecretName, func(data map[string][]byte) error {
		// Re-invite = reset: drop every older invite for this user so only
		// the newest link works.
		for k, v := range data {
			var old inviteRecord
			if json.Unmarshal(v, &old) == nil && old.Username == username {
				delete(data, k)
			}
		}
		data[id] = blob
		return nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return plaintext, expires, nil
}

// resolveInvite validates a plaintext invite against the store WITHOUT
// consuming it.
func (s *KubeStore) resolveInvite(ctx context.Context, plaintext string) (id string, rec inviteRecord, err error) {
	id, secret, ok := parseInvite(plaintext)
	if !ok {
		return "", inviteRecord{}, ErrInvalidInvite
	}
	invites, err := s.readSecretData(ctx, InvitesSecretName)
	if err != nil {
		return "", inviteRecord{}, err
	}
	blob, ok := invites[id]
	if !ok {
		return "", inviteRecord{}, ErrInvalidInvite
	}
	if err := json.Unmarshal(blob, &rec); err != nil {
		return "", inviteRecord{}, ErrInvalidInvite
	}
	if !secretMatches(secret, rec.SecretHash) || rec.expired(s.now()) {
		return "", inviteRecord{}, ErrInvalidInvite
	}
	return id, rec, nil
}

func (s *KubeStore) InviteUsername(ctx context.Context, plaintext string) (string, error) {
	_, rec, err := s.resolveInvite(ctx, plaintext)
	if err != nil {
		return "", err
	}
	return rec.Username, nil
}

func (s *KubeStore) RedeemInvite(ctx context.Context, plaintext, password string) (string, error) {
	if len(password) < MinPasswordLen {
		return "", ErrWeakPassword
	}
	id, secret, ok := parseInvite(plaintext)
	if !ok {
		return "", ErrInvalidInvite
	}

	// Atomic claim FIRST: delete the invite under optimistic concurrency, so
	// even racing redeemers see exactly one winner. Validation happens inside
	// the mutation against the freshest read.
	var claimed inviteRecord
	err := s.mutateSecret(ctx, InvitesSecretName, func(data map[string][]byte) error {
		blob, ok := data[id]
		if !ok {
			return ErrInvalidInvite
		}
		var rec inviteRecord
		if json.Unmarshal(blob, &rec) != nil {
			return ErrInvalidInvite
		}
		if !secretMatches(secret, rec.SecretHash) || rec.expired(s.now()) {
			return ErrInvalidInvite
		}
		claimed = rec
		delete(data, id)
		return nil
	})
	if err != nil {
		return "", err
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	err = s.mutateSecret(ctx, UsersSecretName, func(data map[string][]byte) error {
		blob, ok := data[claimed.Username]
		if !ok {
			// User deleted between claim and write; the invite is spent by
			// design (never reusable), so surface it as invalid.
			return ErrInvalidInvite
		}
		var rec userRecord
		if json.Unmarshal(blob, &rec) != nil {
			return fmt.Errorf("corrupt user record for %q", claimed.Username)
		}
		rec.PasswordHash = hash
		out, merr := json.Marshal(rec)
		if merr != nil {
			return fmt.Errorf("marshalling user record: %w", merr)
		}
		data[claimed.Username] = out
		return nil
	})
	if err != nil {
		return "", err
	}
	return claimed.Username, nil
}

func (s *KubeStore) Authenticate(ctx context.Context, username, password string) error {
	users, err := s.readSecretData(ctx, UsersSecretName)
	if err != nil {
		return err
	}
	blob, ok := users[username]
	if !ok {
		return auth.ErrInvalidCredentials
	}
	var rec userRecord
	if json.Unmarshal(blob, &rec) != nil {
		return auth.ErrInvalidCredentials
	}
	if rec.Disabled || rec.PasswordHash == "" {
		return auth.ErrInvalidCredentials
	}
	if err := auth.VerifyPassword(rec.PasswordHash, password); err != nil {
		return auth.ErrInvalidCredentials
	}
	return nil
}

func (s *KubeStore) List(ctx context.Context) ([]User, error) {
	users, err := s.readSecretData(ctx, UsersSecretName)
	if err != nil {
		return nil, err
	}
	invites, err := s.readSecretData(ctx, InvitesSecretName)
	if err != nil {
		return nil, err
	}
	inviteExpiry := map[string]time.Time{}
	for _, v := range invites {
		var rec inviteRecord
		if json.Unmarshal(v, &rec) == nil {
			inviteExpiry[rec.Username] = rec.ExpiresAt
		}
	}
	out := make([]User, 0, len(users))
	for username, blob := range users {
		var rec userRecord
		if json.Unmarshal(blob, &rec) != nil {
			continue // skip corrupt entries rather than failing the listing
		}
		u := User{
			Username:    username,
			CreatedAt:   rec.CreatedAt,
			Disabled:    rec.Disabled,
			HasPassword: rec.PasswordHash != "",
		}
		if exp, ok := inviteExpiry[username]; ok {
			e := exp
			u.InviteExpiresAt = &e
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

func (s *KubeStore) Delete(ctx context.Context, username string) error {
	err := s.mutateSecret(ctx, UsersSecretName, func(data map[string][]byte) error {
		if _, ok := data[username]; !ok {
			return ErrNotFound
		}
		delete(data, username)
		return nil
	})
	if err != nil {
		return err
	}
	// Best-effort invite cleanup; an orphaned invite is unusable anyway (the
	// redeem path re-checks the user record).
	return s.mutateSecret(ctx, InvitesSecretName, func(data map[string][]byte) error {
		for k, v := range data {
			var rec inviteRecord
			if json.Unmarshal(v, &rec) == nil && rec.Username == username {
				delete(data, k)
			}
		}
		return nil
	})
}
