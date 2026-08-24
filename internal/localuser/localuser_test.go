package localuser

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/auth"
)

// Both implementations must satisfy identical semantics; every test runs
// against MemStore and the fake-clientset-backed KubeStore.
func stores(t *testing.T) map[string]Store {
	t.Helper()
	return map[string]Store{
		"mem":  NewMemStore("admin"),
		"kube": NewKubeStore(fake.NewSimpleClientset(), "admin"),
	}
}

func TestInviteLifecycle(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			if err := s.CreateUser(ctx, "jane"); err != nil {
				t.Fatalf("CreateUser: %v", err)
			}
			tok, expires, err := s.IssueInvite(ctx, "jane")
			if err != nil {
				t.Fatalf("IssueInvite: %v", err)
			}
			if !strings.HasPrefix(tok, InvitePrefix) {
				t.Fatalf("token %q should carry the %q prefix", tok, InvitePrefix)
			}
			if time.Until(expires) < 6*24*time.Hour {
				t.Fatalf("expiry %v should be ~7 days out", expires)
			}

			// Greeting resolves without consuming.
			if u, err := s.InviteUsername(ctx, tok); err != nil || u != "jane" {
				t.Fatalf("InviteUsername = %q, %v", u, err)
			}
			if u, err := s.InviteUsername(ctx, tok); err != nil || u != "jane" {
				t.Fatalf("InviteUsername (second read) = %q, %v", u, err)
			}

			// Redeem sets the password and spends the link.
			if u, err := s.RedeemInvite(ctx, tok, "s3cret-pass"); err != nil || u != "jane" {
				t.Fatalf("RedeemInvite = %q, %v", u, err)
			}
			if err := s.Authenticate(ctx, "jane", "s3cret-pass"); err != nil {
				t.Fatalf("Authenticate after redeem: %v", err)
			}
			if err := s.Authenticate(ctx, "jane", "wrong-pass"); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("wrong password should be invalid credentials, got %v", err)
			}

			// SINGLE-USE: the spent link is dead for both reads and redeems.
			if _, err := s.InviteUsername(ctx, tok); !errors.Is(err, ErrInvalidInvite) {
				t.Fatalf("spent invite should be invalid, got %v", err)
			}
			if _, err := s.RedeemInvite(ctx, tok, "another-pass"); !errors.Is(err, ErrInvalidInvite) {
				t.Fatalf("second redeem must fail, got %v", err)
			}
			if err := s.Authenticate(ctx, "jane", "s3cret-pass"); err != nil {
				t.Fatalf("original password must survive the failed second redeem: %v", err)
			}
		})
	}
}

func TestReinviteInvalidatesOldLinkAndResetsPassword(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_ = s.CreateUser(ctx, "jane")
			tok1, _, _ := s.IssueInvite(ctx, "jane")
			if _, err := s.RedeemInvite(ctx, tok1, "first-pass1"); err != nil {
				t.Fatalf("first redeem: %v", err)
			}

			// Re-invite = password reset: new link works, old password until then.
			tok2, _, err := s.IssueInvite(ctx, "jane")
			if err != nil {
				t.Fatalf("re-invite: %v", err)
			}
			tok3, _, err := s.IssueInvite(ctx, "jane")
			if err != nil {
				t.Fatalf("second re-invite: %v", err)
			}
			// Only the NEWEST outstanding link is redeemable.
			if _, err := s.RedeemInvite(ctx, tok2, "should-not-work"); !errors.Is(err, ErrInvalidInvite) {
				t.Fatalf("older re-invite must be invalidated, got %v", err)
			}
			if _, err := s.RedeemInvite(ctx, tok3, "second-pass2"); err != nil {
				t.Fatalf("newest link redeem: %v", err)
			}
			if err := s.Authenticate(ctx, "jane", "second-pass2"); err != nil {
				t.Fatalf("new password should work: %v", err)
			}
			if err := s.Authenticate(ctx, "jane", "first-pass1"); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("old password must be dead after reset, got %v", err)
			}
		})
	}
}

func TestCreateUserRefusesDuplicatesAndReserved(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			if err := s.CreateUser(ctx, "admin"); !errors.Is(err, ErrReserved) {
				t.Fatalf("admin username must be reserved, got %v", err)
			}
			if err := s.CreateUser(ctx, "has space"); err == nil {
				t.Fatal("whitespace username must be rejected")
			}
			_ = s.CreateUser(ctx, "jane")
			if err := s.CreateUser(ctx, "jane"); !errors.Is(err, ErrExists) {
				t.Fatalf("duplicate must be ErrExists, got %v", err)
			}
		})
	}
}

func TestAuthenticateEdgeCases(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			// Unknown user.
			if err := s.Authenticate(ctx, "ghost", "whatever1"); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("unknown user: got %v", err)
			}
			// Invited but password never set.
			_ = s.CreateUser(ctx, "pending")
			_, _, _ = s.IssueInvite(ctx, "pending")
			if err := s.Authenticate(ctx, "pending", "anything1"); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("password-less user must not authenticate, got %v", err)
			}
			// Weak password refused at redeem.
			tok, _, _ := s.IssueInvite(ctx, "pending")
			if _, err := s.RedeemInvite(ctx, tok, "short"); !errors.Is(err, ErrWeakPassword) {
				t.Fatalf("weak password: got %v", err)
			}
			// The weak attempt must NOT have spent the link.
			if _, err := s.RedeemInvite(ctx, tok, "long-enough"); err != nil {
				t.Fatalf("redeem after weak attempt: %v", err)
			}
		})
	}
}

func TestDeleteRemovesUserAndInvite(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_ = s.CreateUser(ctx, "jane")
			tok, _, _ := s.IssueInvite(ctx, "jane")
			if err := s.Delete(ctx, "jane"); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := s.InviteUsername(ctx, tok); !errors.Is(err, ErrInvalidInvite) {
				t.Fatalf("invite must die with the user, got %v", err)
			}
			if err := s.Authenticate(ctx, "jane", "whatever1"); !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("deleted user must not authenticate, got %v", err)
			}
			if err := s.Delete(ctx, "jane"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("double delete: got %v", err)
			}
		})
	}
}

func TestListStatuses(t *testing.T) {
	ctx := context.Background()
	for name, s := range stores(t) {
		t.Run(name, func(t *testing.T) {
			_ = s.CreateUser(ctx, "active-user")
			tok, _, _ := s.IssueInvite(ctx, "active-user")
			_, _ = s.RedeemInvite(ctx, tok, "password1")
			_ = s.CreateUser(ctx, "invited-user")
			_, _, _ = s.IssueInvite(ctx, "invited-user")

			users, err := s.List(ctx)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(users) != 2 {
				t.Fatalf("expected 2 users, got %d", len(users))
			}
			// Sorted by username: active-user, invited-user.
			if !users[0].HasPassword || users[0].InviteExpiresAt != nil {
				t.Errorf("active-user: %+v", users[0])
			}
			if users[1].HasPassword || users[1].InviteExpiresAt == nil {
				t.Errorf("invited-user: %+v", users[1])
			}
		})
	}
}
