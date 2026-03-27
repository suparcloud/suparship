package session

import (
	"testing"
	"time"
)

func TestStoreCreateAndGet(t *testing.T) {
	store := NewStore(time.Hour)

	sess, err := store.Create("admin", "org_admin")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("session ID must not be empty")
	}
	if sess.Username != "admin" {
		t.Fatalf("expected username %q, got %q", "admin", sess.Username)
	}
	if sess.Role != "org_admin" {
		t.Fatalf("expected role %q, got %q", "org_admin", sess.Role)
	}

	got, ok := store.Get(sess.ID)
	if !ok {
		t.Fatal("Get should find the session")
	}
	if got.ID != sess.ID {
		t.Fatalf("Get returned wrong session: %q vs %q", got.ID, sess.ID)
	}
}

func TestStoreGetNotFound(t *testing.T) {
	store := NewStore(time.Hour)

	_, ok := store.Get("nonexistent")
	if ok {
		t.Fatal("Get should return false for nonexistent session")
	}
}

func TestStoreDelete(t *testing.T) {
	store := NewStore(time.Hour)

	sess, err := store.Create("admin", "org_admin")
	if err != nil {
		t.Fatal(err)
	}

	store.Delete(sess.ID)

	_, ok := store.Get(sess.ID)
	if ok {
		t.Fatal("Get should return false after Delete")
	}
}

func TestStoreExpiry(t *testing.T) {
	store := NewStore(1 * time.Millisecond)

	sess, err := store.Create("admin", "org_admin")
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(5 * time.Millisecond)

	_, ok := store.Get(sess.ID)
	if ok {
		t.Fatal("expired session should not be returned")
	}
}

func TestStoreUniqueIDs(t *testing.T) {
	store := NewStore(time.Hour)

	s1, err := store.Create("user1", "role1")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := store.Create("user2", "role2")
	if err != nil {
		t.Fatal(err)
	}

	if s1.ID == s2.ID {
		t.Fatal("sessions should have unique IDs")
	}
}
