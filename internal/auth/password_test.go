package auth

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestGeneratePassword(t *testing.T) {
	pw, err := GeneratePassword()
	if err != nil {
		t.Fatalf("GeneratePassword: %v", err)
	}
	// 24 bytes → 32 base64 characters
	if len(pw) != 32 {
		t.Fatalf("expected 32-char password, got %d", len(pw))
	}
}

func TestGeneratePasswordUniqueness(t *testing.T) {
	pw1, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	pw2, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	if pw1 == pw2 {
		t.Fatal("two generated passwords should not be identical")
	}
}

func TestHashPassword(t *testing.T) {
	hash, err := HashPassword("test-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "test-password" {
		t.Fatal("hash must not equal plaintext")
	}
	// Verify it's a valid bcrypt hash.
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("test-password")); err != nil {
		t.Fatalf("bcrypt verification failed: %v", err)
	}
}

func TestHashPasswordCost(t *testing.T) {
	hash, err := HashPassword("test-password")
	if err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		t.Fatalf("reading bcrypt cost: %v", err)
	}
	if cost != bcryptCost {
		t.Fatalf("expected cost %d, got %d", bcryptCost, cost)
	}
}

func TestVerifyPasswordCorrect(t *testing.T) {
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword(hash, "correct-password"); err != nil {
		t.Fatalf("VerifyPassword should succeed for correct password: %v", err)
	}
}

func TestVerifyPasswordWrong(t *testing.T) {
	hash, err := HashPassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword(hash, "wrong-password"); err == nil {
		t.Fatal("VerifyPassword should fail for wrong password")
	}
}

func TestRoundTrip(t *testing.T) {
	pw, err := GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword(hash, pw); err != nil {
		t.Fatalf("round-trip failed: generate → hash → verify: %v", err)
	}
}
