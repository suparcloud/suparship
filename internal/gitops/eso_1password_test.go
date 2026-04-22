package gitops

import (
	"strings"
	"testing"
)

func TestBuild1PasswordClusterSecretStoreYAML(t *testing.T) {
	yaml := Build1PasswordClusterSecretStoreYAML(
		"onepassword-prod",
		"http://op-connect.onepassword-connect.svc.cluster.local:8080",
		"vault-uuid-123",
		"op-connect-token-prod",
		"token",
		"external-secrets",
	)

	if !strings.Contains(yaml, "onepassword-prod") {
		t.Error("expected store name in output")
	}
	if !strings.Contains(yaml, "connectHost: http://op-connect.onepassword-connect.svc.cluster.local:8080") {
		t.Error("expected connectHost in output")
	}
	if !strings.Contains(yaml, "vault-uuid-123") {
		t.Error("expected vault ID in output")
	}
	if !strings.Contains(yaml, "op-connect-token-prod") {
		t.Error("expected token secret name in output")
	}
	if !strings.Contains(yaml, "connectTokenSecretRef") {
		t.Error("expected connect token ref in output")
	}
}

func TestBuild1PasswordExternalSecretYAML(t *testing.T) {
	yaml := Build1PasswordExternalSecretYAML(
		"my-secret", "my-namespace", "vault-uuid-123",
		[]string{"DATABASE_URL", "API_KEY"},
	)

	if !strings.Contains(yaml, "suparship-1password-store") {
		t.Error("expected store ref in output")
	}
	if !strings.Contains(yaml, "vault-uuid-123") {
		t.Error("expected vault UUID in output")
	}
	if !strings.Contains(yaml, "DATABASE_URL") {
		t.Error("expected DATABASE_URL key")
	}
	if !strings.Contains(yaml, "API_KEY") {
		t.Error("expected API_KEY key")
	}
}

func TestESOStoreNames_Includes1Password(t *testing.T) {
	name, ok := ESOStoreNames["1password"]
	if !ok {
		t.Fatal("1password not in ESOStoreNames map")
	}
	if name != onePasswordStoreName {
		t.Errorf("store name = %q, want %q", name, onePasswordStoreName)
	}
}
