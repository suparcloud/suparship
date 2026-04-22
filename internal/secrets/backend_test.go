package secrets

import (
	"errors"
	"testing"
	"time"
)

func TestBackendConfig_Effective(t *testing.T) {
	tests := []struct {
		name string
		cfg  BackendConfig
		want BackendType
	}{
		{"empty defaults to k8s", BackendConfig{}, BackendK8s},
		{"explicit k8s", BackendConfig{Type: BackendK8s}, BackendK8s},
		{"explicit onepassword", BackendConfig{Type: Backend1Password}, Backend1Password},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Effective(); got != tt.want {
				t.Errorf("Effective() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidBackendTypes(t *testing.T) {
	for _, bt := range []BackendType{BackendK8s, Backend1Password} {
		if !ValidBackendTypes[bt] {
			t.Errorf("%q should be valid", bt)
		}
	}
	for _, bt := range []BackendType{"vault", "aws-sm", "invalid", ""} {
		if ValidBackendTypes[bt] {
			t.Errorf("%q should not be valid", bt)
		}
	}
}

func TestBackendConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     BackendConfig
		wantErr error
	}{
		{"k8s valid", BackendConfig{Type: BackendK8s}, nil},
		{"onepassword valid", BackendConfig{Type: Backend1Password}, nil},
		{"empty defaults to k8s (valid)", BackendConfig{}, nil},
		{"unknown type", BackendConfig{Type: "nope"}, ErrInvalidBackendType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("got %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestBackendConfig_FindBinding(t *testing.T) {
	cfg := BackendConfig{
		Type: Backend1Password,
		OnePassword: &OnePasswordConfig{
			Bindings: []EnvBinding{
				{Env: "prod", VaultID: "v-prod"},
				{Env: "staging", VaultID: "v-stg"},
			},
		},
	}

	tests := []struct {
		name   string
		env    string
		wantID string
		wantOK bool
	}{
		{"found prod", "prod", "v-prod", true},
		{"found staging", "staging", "v-stg", true},
		{"not found", "preview", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := cfg.FindBinding(tt.env)
			if tt.wantOK {
				if b == nil {
					t.Fatal("expected binding, got nil")
				}
				if b.VaultID != tt.wantID {
					t.Errorf("VaultID = %q, want %q", b.VaultID, tt.wantID)
				}
			} else if b != nil {
				t.Errorf("expected nil, got %+v", b)
			}
		})
	}

	t.Run("nil OnePassword returns nil", func(t *testing.T) {
		empty := BackendConfig{Type: BackendK8s}
		if b := empty.FindBinding("prod"); b != nil {
			t.Errorf("expected nil, got %+v", b)
		}
	})
}

func TestBackendConfig_UpsertBinding(t *testing.T) {
	t.Run("creates OnePasswordConfig when nil", func(t *testing.T) {
		cfg := BackendConfig{Type: Backend1Password}
		cfg.UpsertBinding(EnvBinding{Env: "prod", VaultID: "v1"})

		if cfg.OnePassword == nil {
			t.Fatal("expected OnePassword to be created")
		}
		if len(cfg.OnePassword.Bindings) != 1 {
			t.Fatalf("expected 1 binding, got %d", len(cfg.OnePassword.Bindings))
		}
		if cfg.OnePassword.Bindings[0].VaultID != "v1" {
			t.Errorf("VaultID = %q, want %q", cfg.OnePassword.Bindings[0].VaultID, "v1")
		}
	})

	t.Run("adds new binding in sorted order", func(t *testing.T) {
		cfg := BackendConfig{
			Type:        Backend1Password,
			OnePassword: &OnePasswordConfig{},
		}
		cfg.UpsertBinding(EnvBinding{Env: "staging", VaultID: "v-stg"})
		cfg.UpsertBinding(EnvBinding{Env: "dev", VaultID: "v-dev"})
		cfg.UpsertBinding(EnvBinding{Env: "prod", VaultID: "v-prod"})

		if len(cfg.OnePassword.Bindings) != 3 {
			t.Fatalf("expected 3 bindings, got %d", len(cfg.OnePassword.Bindings))
		}
		want := []string{"dev", "prod", "staging"}
		for i, w := range want {
			if cfg.OnePassword.Bindings[i].Env != w {
				t.Errorf("binding[%d].Env = %q, want %q", i, cfg.OnePassword.Bindings[i].Env, w)
			}
		}
	})

	t.Run("replaces existing binding", func(t *testing.T) {
		now := time.Now()
		cfg := BackendConfig{
			Type: Backend1Password,
			OnePassword: &OnePasswordConfig{
				Bindings: []EnvBinding{
					{Env: "prod", VaultID: "old-id"},
				},
			},
		}
		cfg.UpsertBinding(EnvBinding{Env: "prod", VaultID: "new-id", Provisioned: true, LastProvisioned: now})

		if len(cfg.OnePassword.Bindings) != 1 {
			t.Fatalf("expected 1 binding, got %d", len(cfg.OnePassword.Bindings))
		}
		b := cfg.OnePassword.Bindings[0]
		if b.VaultID != "new-id" {
			t.Errorf("VaultID = %q, want %q", b.VaultID, "new-id")
		}
		if !b.Provisioned {
			t.Error("expected Provisioned=true")
		}
	})
}

func TestBackendConfig_RemoveBinding(t *testing.T) {
	t.Run("removes existing binding", func(t *testing.T) {
		cfg := BackendConfig{
			Type: Backend1Password,
			OnePassword: &OnePasswordConfig{
				Bindings: []EnvBinding{
					{Env: "prod", VaultID: "v-prod"},
					{Env: "staging", VaultID: "v-stg"},
				},
			},
		}
		cfg.RemoveBinding("prod")

		if len(cfg.OnePassword.Bindings) != 1 {
			t.Fatalf("expected 1 binding, got %d", len(cfg.OnePassword.Bindings))
		}
		if cfg.OnePassword.Bindings[0].Env != "staging" {
			t.Errorf("remaining binding = %q, want staging", cfg.OnePassword.Bindings[0].Env)
		}
	})

	t.Run("no-op for missing env", func(t *testing.T) {
		cfg := BackendConfig{
			Type: Backend1Password,
			OnePassword: &OnePasswordConfig{
				Bindings: []EnvBinding{{Env: "prod", VaultID: "v1"}},
			},
		}
		cfg.RemoveBinding("preview")

		if len(cfg.OnePassword.Bindings) != 1 {
			t.Fatalf("expected 1 binding, got %d", len(cfg.OnePassword.Bindings))
		}
	})

	t.Run("no-op when OnePassword is nil", func(t *testing.T) {
		cfg := BackendConfig{Type: BackendK8s}
		cfg.RemoveBinding("prod") // should not panic
	})
}

func TestBackendConfig_MigrateFromLegacy(t *testing.T) {
	tests := []struct {
		name     string
		cfg      BackendConfig
		wantType BackendType
	}{
		{"maps old 1password to onepassword", BackendConfig{Type: "1password"}, Backend1Password},
		{"already onepassword is no-op", BackendConfig{Type: Backend1Password}, Backend1Password},
		{"k8s unchanged", BackendConfig{Type: BackendK8s}, BackendK8s},
		{"empty unchanged", BackendConfig{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.MigrateFromLegacy()
			if tt.cfg.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", tt.cfg.Type, tt.wantType)
			}
		})
	}
}

func TestVaultName(t *testing.T) {
	tests := []struct {
		org, env string
		want     string
	}{
		{"acme", "prod", "suparship-acme-prod"},
		{"acme", "staging", "suparship-acme-staging"},
	}
	for _, tt := range tests {
		if got := VaultName(tt.org, tt.env); got != tt.want {
			t.Errorf("VaultName(%q, %q) = %q, want %q", tt.org, tt.env, got, tt.want)
		}
	}
}

func TestConnectTokenSecretName(t *testing.T) {
	if got := ConnectTokenSecretName("staging"); got != "op-connect-token-staging" {
		t.Errorf("got %q, want %q", got, "op-connect-token-staging")
	}
}

func TestClusterSecretStoreNameForEnv(t *testing.T) {
	if got := ClusterSecretStoreNameForEnv("prod"); got != "onepassword-prod" {
		t.Errorf("got %q, want %q", got, "onepassword-prod")
	}
}
