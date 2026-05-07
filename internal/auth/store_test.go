package auth

import "testing"

func TestNewBootstrapCredentials(t *testing.T) {
	creds, plaintext, err := NewBootstrapCredentials("admin")
	if err != nil {
		t.Fatalf("NewBootstrapCredentials: %v", err)
	}

	if creds.Username != "admin" {
		t.Fatalf("expected username %q, got %q", "admin", creds.Username)
	}
	if creds.PasswordHash == "" {
		t.Fatal("password hash must not be empty")
	}
	if creds.PasswordHash == plaintext {
		t.Fatal("password hash must not equal plaintext")
	}
	if plaintext == "" {
		t.Fatal("plaintext password must not be empty")
	}
}

func TestNewBootstrapCredentialsEmptyUsername(t *testing.T) {
	_, _, err := NewBootstrapCredentials("")
	if err == nil {
		t.Fatal("expected error for empty username")
	}
}

func TestBootstrapCredentialsVerify(t *testing.T) {
	creds, plaintext, err := NewBootstrapCredentials("admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.Verify(plaintext); err != nil {
		t.Fatalf("Verify should succeed with correct password: %v", err)
	}
	if err := creds.Verify("wrong-password"); err == nil {
		t.Fatal("Verify should fail with wrong password")
	}
}

func TestSecretDataRoundTrip(t *testing.T) {
	creds, _, err := NewBootstrapCredentials("admin")
	if err != nil {
		t.Fatal(err)
	}

	ref := DefaultSecretRef()
	data := creds.SecretData(ref)

	if data[DefaultSecretKeyUser] != "admin" {
		t.Fatalf("expected username %q in secret data, got %q", "admin", data[DefaultSecretKeyUser])
	}
	if data[DefaultSecretKeyPasswordHash] == "" {
		t.Fatal("password-hash must not be empty in secret data")
	}

	restored, err := CredentialsFromSecretData(data, ref)
	if err != nil {
		t.Fatalf("CredentialsFromSecretData: %v", err)
	}
	if restored.Username != creds.Username {
		t.Fatalf("username mismatch after round-trip: %q vs %q", restored.Username, creds.Username)
	}
	if restored.PasswordHash != creds.PasswordHash {
		t.Fatal("password hash mismatch after round-trip")
	}
}

func TestCredentialsFromSecretDataMissingKeys(t *testing.T) {
	tests := []struct {
		name string
		data map[string]string
	}{
		{"missing username", map[string]string{DefaultSecretKeyPasswordHash: "$2a$12$hash"}},
		{"empty username", map[string]string{DefaultSecretKeyUser: "", DefaultSecretKeyPasswordHash: "$2a$12$hash"}},
		{"missing hash", map[string]string{DefaultSecretKeyUser: "admin"}},
		{"empty hash", map[string]string{DefaultSecretKeyUser: "admin", DefaultSecretKeyPasswordHash: ""}},
	}

	ref := DefaultSecretRef()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := CredentialsFromSecretData(tt.data, ref)
			if err == nil {
				t.Fatal("expected error for invalid secret data")
			}
		})
	}
}
