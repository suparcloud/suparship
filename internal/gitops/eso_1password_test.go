package gitops

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/secrets"
)

func TestBuild1PasswordClusterSecretStoreYAML_Connect(t *testing.T) {
	cfg := &secrets.OnePasswordConfig{
		Mode:           secrets.OnePasswordModeConnect,
		ConnectHost:    "https://op-connect.internal:8443",
		ExistingSecret: "op-token",
	}

	yaml := Build1PasswordClusterSecretStoreYAML(cfg)

	if !strings.Contains(yaml, "suparship-1password-store") {
		t.Error("expected store name in output")
	}
	if !strings.Contains(yaml, "connectHost: https://op-connect.internal:8443") {
		t.Error("expected connectHost in output")
	}
	if !strings.Contains(yaml, "op-token") {
		t.Error("expected secret ref in output")
	}
	if !strings.Contains(yaml, "connectTokenSecretRef") {
		t.Error("expected connect token ref in output")
	}
}

func TestBuild1PasswordClusterSecretStoreYAML_ServiceAccount(t *testing.T) {
	cfg := &secrets.OnePasswordConfig{
		Mode:           secrets.OnePasswordModeServiceAccount,
		ExistingSecret: "op-sa-token",
	}

	yaml := Build1PasswordClusterSecretStoreYAML(cfg)

	if !strings.Contains(yaml, "suparship-1password-store") {
		t.Error("expected store name in output")
	}
	if !strings.Contains(yaml, "serviceAccountTokenSecretRef") {
		t.Error("expected service account token ref in output")
	}
	if !strings.Contains(yaml, "op-sa-token") {
		t.Error("expected secret ref in output")
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
