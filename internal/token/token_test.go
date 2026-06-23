package token

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGenerateParseRoundTrip(t *testing.T) {
	id, secret, plaintext, err := generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(plaintext, Prefix) {
		t.Errorf("plaintext %q missing prefix %q", plaintext, Prefix)
	}
	gotID, gotSecret, ok := parse(plaintext)
	if !ok {
		t.Fatalf("parse(%q) failed", plaintext)
	}
	if gotID != id || gotSecret != secret {
		t.Errorf("round trip mismatch: id %q/%q secret %q/%q", gotID, id, gotSecret, secret)
	}
}

func TestGenerateUnique(t *testing.T) {
	_, _, a, _ := generate()
	_, _, b, _ := generate()
	if a == b {
		t.Error("two generated tokens collided")
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := []string{
		"",
		"nope",
		"supat_",
		"supat_short",
		Prefix + strings.Repeat("a", idHexLen+secretHexLen-1), // one char short
		Prefix + strings.Repeat("a", idHexLen+secretHexLen+1), // one char long
		strings.Repeat("a", idHexLen+secretHexLen),            // no prefix
	}
	for _, c := range cases {
		if _, _, ok := parse(c); ok {
			t.Errorf("parse(%q) = ok, want rejected", c)
		}
	}
}

func TestSecretMatches(t *testing.T) {
	_, secret, _, _ := generate()
	h := hashSecret(secret)
	if !secretMatches(secret, h) {
		t.Error("secretMatches rejected the correct secret")
	}
	if secretMatches(secret+"x", h) {
		t.Error("secretMatches accepted a wrong secret")
	}
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC)
	if (Metadata{}).Expired(now) {
		t.Error("token with no expiry must never be expired")
	}
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	if !(Metadata{ExpiresAt: &past}).Expired(now) {
		t.Error("token past its expiry should be expired")
	}
	if (Metadata{ExpiresAt: &future}).Expired(now) {
		t.Error("token before its expiry should not be expired")
	}
}

func TestMemStoreIssueResolve(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()

	plaintext, meta, err := s.Issue(ctx, Metadata{Name: "ci", Project: "api", Role: "developer", CreatedBy: "alice"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if meta.ID == "" {
		t.Error("Issue did not assign an ID")
	}
	got, err := s.Resolve(ctx, plaintext)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ID != meta.ID || got.Project != "api" || got.Role != "developer" || got.CreatedBy != "alice" {
		t.Errorf("Resolve returned %+v, want id=%s project=api role=developer", got, meta.ID)
	}
}

func TestMemStoreResolveInvalid(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	if _, err := s.Resolve(ctx, "garbage"); !errors.Is(err, ErrInvalid) {
		t.Errorf("Resolve(garbage) err = %v, want ErrInvalid", err)
	}
	// Structurally valid but unknown id.
	_, _, plaintext, _ := generate()
	if _, err := s.Resolve(ctx, plaintext); !errors.Is(err, ErrInvalid) {
		t.Errorf("Resolve(unknown) err = %v, want ErrInvalid", err)
	}
}

func TestMemStoreResolveExpired(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	plaintext, _, err := s.Issue(ctx, Metadata{Name: "old", Project: "api", Role: "viewer", ExpiresAt: &past})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := s.Resolve(ctx, plaintext); !errors.Is(err, ErrExpired) {
		t.Errorf("Resolve(expired) err = %v, want ErrExpired", err)
	}
}

func TestMemStoreWrongSecretRejected(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	plaintext, _, _ := s.Issue(ctx, Metadata{Name: "t", Project: "api", Role: "developer"})
	// Keep the id, corrupt the secret tail.
	id, _, _ := parse(plaintext)
	forged := Prefix + id + strings.Repeat("0", secretHexLen)
	if _, err := s.Resolve(ctx, forged); !errors.Is(err, ErrInvalid) {
		t.Errorf("Resolve(forged secret) err = %v, want ErrInvalid", err)
	}
}

func TestMemStoreListScopedToProject(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	_, _, _ = s.Issue(ctx, Metadata{Name: "a", Project: "api", Role: "developer"})
	_, _, _ = s.Issue(ctx, Metadata{Name: "b", Project: "api", Role: "viewer"})
	_, _, _ = s.Issue(ctx, Metadata{Name: "c", Project: "other", Role: "developer"})

	api, err := s.List(ctx, "api")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(api) != 2 {
		t.Errorf("List(api) returned %d tokens, want 2", len(api))
	}
	for _, m := range api {
		if m.Project != "api" {
			t.Errorf("List(api) leaked token from project %q", m.Project)
		}
	}
}

func TestMemStoreDelete(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	plaintext, meta, _ := s.Issue(ctx, Metadata{Name: "t", Project: "api", Role: "developer"})

	// Wrong project must not delete and must report not-found.
	if err := s.Delete(ctx, "other", meta.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(wrong project) err = %v, want ErrNotFound", err)
	}
	if _, err := s.Resolve(ctx, plaintext); err != nil {
		t.Fatalf("token should still resolve after failed cross-project delete: %v", err)
	}

	if err := s.Delete(ctx, "api", meta.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Resolve(ctx, plaintext); !errors.Is(err, ErrInvalid) {
		t.Errorf("deleted token resolved: err = %v, want ErrInvalid", err)
	}
	if err := s.Delete(ctx, "api", meta.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete err = %v, want ErrNotFound", err)
	}
}
