package secrets

import (
	"errors"
	"testing"
)

func TestOnePasswordConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     OnePasswordConfig
		wantErr error
	}{
		{
			name:    "missing mode",
			cfg:     OnePasswordConfig{ExistingSecret: "tok"},
			wantErr: ErrOnePasswordMissingMode,
		},
		{
			name:    "invalid mode",
			cfg:     OnePasswordConfig{Mode: "bad", ExistingSecret: "tok"},
			wantErr: ErrOnePasswordInvalidMode,
		},
		{
			name:    "connect missing host",
			cfg:     OnePasswordConfig{Mode: OnePasswordModeConnect, ExistingSecret: "tok"},
			wantErr: ErrOnePasswordMissingConnectHost,
		},
		{
			name:    "connect missing secret",
			cfg:     OnePasswordConfig{Mode: OnePasswordModeConnect, ConnectHost: "https://op.local:8443"},
			wantErr: ErrOnePasswordMissingSecret,
		},
		{
			name: "valid connect",
			cfg: OnePasswordConfig{
				Mode:           OnePasswordModeConnect,
				ConnectHost:    "https://op.local:8443",
				ExistingSecret: "op-token",
				Vaults:         map[string]string{"staging": "vault-uuid-1"},
			},
		},
		{
			name:    "service-account missing secret",
			cfg:     OnePasswordConfig{Mode: OnePasswordModeServiceAccount},
			wantErr: ErrOnePasswordMissingSecret,
		},
		{
			name: "valid service-account",
			cfg: OnePasswordConfig{
				Mode:           OnePasswordModeServiceAccount,
				ExistingSecret: "op-sa-token",
			},
		},
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

func TestBackendConfig_Validate_1Password(t *testing.T) {
	cfg := BackendConfig{
		Type: Backend1Password,
		OnePassword: &OnePasswordConfig{
			Mode:           OnePasswordModeConnect,
			ConnectHost:    "https://op.local:8443",
			ExistingSecret: "op-token",
			Vaults:         map[string]string{"staging": "vault-1", "prod": "vault-2"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBackendConfig_Validate_1Password_MissingConfig(t *testing.T) {
	cfg := BackendConfig{Type: Backend1Password}
	err := cfg.Validate()
	if !errors.Is(err, ErrOnePasswordMissingConfig) {
		t.Errorf("got %v, want ErrOnePasswordMissingConfig", err)
	}
}

func TestBackendConfig_Validate_K8s(t *testing.T) {
	cfg := BackendConfig{Type: BackendK8s}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBackendConfig_Validate_Empty(t *testing.T) {
	cfg := BackendConfig{}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error for empty (defaults to k8s): %v", err)
	}
}
