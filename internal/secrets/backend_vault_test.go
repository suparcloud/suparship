package secrets

import (
	"errors"
	"testing"
)

func TestBackendVault_Registered(t *testing.T) {
	if !ValidBackendTypes[BackendVault] {
		t.Fatal("BackendVault is not a valid backend type")
	}
	if got := (BackendConfig{Type: BackendVault}).Effective(); got != BackendVault {
		t.Errorf("Effective() = %q, want %q", got, BackendVault)
	}
}

func TestHCVaultConfig_EffectiveMount(t *testing.T) {
	tests := []struct {
		name string
		cfg  *HCVaultConfig
		want string
	}{
		{"nil config", nil, DefaultVaultMount},
		{"empty mount", &HCVaultConfig{}, DefaultVaultMount},
		{"whitespace mount", &HCVaultConfig{Mount: "   "}, DefaultVaultMount},
		{"explicit mount", &HCVaultConfig{Mount: "platform-kv"}, "platform-kv"},
		{"trims", &HCVaultConfig{Mount: "  platform-kv  "}, "platform-kv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveMount(); got != tt.want {
				t.Errorf("EffectiveMount() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUsesPerClusterCredentials(t *testing.T) {
	tests := []struct {
		backend BackendType
		want    bool
	}{
		{BackendK8s, false},
		{"", false}, // empty defaults to k8s
		{Backend1Password, true},
		{BackendVault, true},
	}
	for _, tt := range tests {
		if got := (BackendConfig{Type: tt.backend}).UsesPerClusterCredentials(); got != tt.want {
			t.Errorf("backend %q: UsesPerClusterCredentials() = %v, want %v", tt.backend, got, tt.want)
		}
	}
}

func TestBackendConfig_ValidateVault(t *testing.T) {
	// Vault active without an address is rejected.
	c := BackendConfig{Type: BackendVault}
	if err := c.Validate(); !errors.Is(err, ErrVaultAddressRequired) {
		t.Errorf("Validate() = %v, want ErrVaultAddressRequired", err)
	}

	// With an address it passes.
	c.Vault = &HCVaultConfig{Address: "https://vault.example.com:8200"}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() with address = %v, want nil", err)
	}

	// A partial Vault config is fine while a DIFFERENT backend is active —
	// otherwise you could never switch away from a half-configured Vault.
	inactive := BackendConfig{Type: BackendK8s, Vault: &HCVaultConfig{}}
	if err := inactive.Validate(); err != nil {
		t.Errorf("Validate() with inactive partial vault = %v, want nil", err)
	}
}

// Per-cluster tokens are stored per backend. Switching backends must not
// surface the other backend's credential — they are different secrets against
// different systems, and confusing them would publish a 1Password token as a
// Vault token.
func TestClusterTokens_IsolatedPerBackend(t *testing.T) {
	c := &BackendConfig{Type: Backend1Password}
	c.UpsertClusterToken(ClusterTokenRef{Cluster: "eu-1", Sealed: true})

	if ref := c.FindClusterToken("eu-1"); ref == nil || !ref.Sealed {
		t.Fatalf("1Password token not stored: %+v", ref)
	}

	// Switch to Vault: the 1Password token must not be visible.
	c.Type = BackendVault
	if ref := c.FindClusterToken("eu-1"); ref != nil {
		t.Errorf("vault backend sees the 1Password token: %+v", ref)
	}

	// Store a Vault token for the same cluster; both now coexist.
	c.UpsertClusterToken(ClusterTokenRef{Cluster: "eu-1", Sealed: true, LastError: "vault"})
	if ref := c.FindClusterToken("eu-1"); ref == nil || ref.LastError != "vault" {
		t.Fatalf("vault token not stored: %+v", ref)
	}
	if c.OnePassword == nil || len(c.OnePassword.ClusterTokens) != 1 {
		t.Error("1Password tokens were clobbered by the vault write")
	}

	// Switching back reveals the original, untouched.
	c.Type = Backend1Password
	if ref := c.FindClusterToken("eu-1"); ref == nil || ref.LastError != "" {
		t.Errorf("1Password token changed across the switch: %+v", ref)
	}

	// Removal only affects the active backend.
	c.RemoveClusterToken("eu-1")
	if ref := c.FindClusterToken("eu-1"); ref != nil {
		t.Error("1Password token not removed")
	}
	c.Type = BackendVault
	if ref := c.FindClusterToken("eu-1"); ref == nil {
		t.Error("removing the 1Password token also removed the vault one")
	}
}

// k8s has no per-cluster credential; the accessors must be inert rather than
// allocating a config block for a backend that cannot use one.
func TestClusterTokens_NoOpForK8s(t *testing.T) {
	c := &BackendConfig{Type: BackendK8s}
	c.UpsertClusterToken(ClusterTokenRef{Cluster: "eu-1", Sealed: true})

	if ref := c.FindClusterToken("eu-1"); ref != nil {
		t.Errorf("k8s backend returned a cluster token: %+v", ref)
	}
	if c.OnePassword != nil || c.Vault != nil {
		t.Error("k8s upsert allocated a backend config block")
	}
	c.RemoveClusterToken("eu-1") // must not panic
}

func TestSetupComplete_Vault(t *testing.T) {
	envs := []string{"staging"}
	clusters := []string{"eu-1"}

	// No address yet.
	c := BackendConfig{Type: BackendVault}
	if ok, reason := c.SetupComplete(envs, clusters); ok || reason == "" {
		t.Errorf("SetupComplete() = %v, %q; want incomplete with a reason", ok, reason)
	}

	// Address set, token still missing.
	c.Vault = &HCVaultConfig{Address: "https://vault.example.com:8200"}
	ok, reason := c.SetupComplete(envs, clusters)
	if ok {
		t.Error("SetupComplete() = true with no sealed cluster token")
	}
	if reason == "" {
		t.Error("expected a reason naming the unsealed cluster")
	}

	// Sealed token completes setup — note there is NO vault-registration step,
	// unlike 1Password: per-scope paths are derived, not registered.
	cp := &c
	cp.UpsertClusterToken(ClusterTokenRef{Cluster: "eu-1", Sealed: true})
	if ok, reason := cp.SetupComplete(envs, clusters); !ok {
		t.Errorf("SetupComplete() = false, %q; want complete", reason)
	}
}

func TestClusterCredentialSecretName(t *testing.T) {
	tests := []struct {
		backend BackendType
		want    string
	}{
		{BackendK8s, ""},
		{Backend1Password, ConnectTokenSecretName},
		{BackendVault, VaultTokenClusterSecretName},
	}
	for _, tt := range tests {
		if got := (BackendConfig{Type: tt.backend}).ClusterCredentialSecretName(); got != tt.want {
			t.Errorf("backend %q: got %q, want %q", tt.backend, got, tt.want)
		}
	}
}
