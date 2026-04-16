package registry

import (
	"errors"
	"testing"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{
			name: "disabled is always valid",
			cfg:  Config{Enabled: false},
		},
		{
			name:    "enabled requires url",
			cfg:     Config{Enabled: true},
			wantErr: ErrMissingURL,
		},
		{
			name: "enabled with url is valid",
			cfg:  Config{Enabled: true, URL: "ghcr.io"},
		},
		{
			name: "full config",
			cfg: Config{
				Enabled:       true,
				URL:           "registry.example.com",
				Username:      "robot",
				AuthSecretRef: "reg-creds",
				Environments:  []string{"staging", "prod"},
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

func TestConfig_AppliesToEnv(t *testing.T) {
	tests := []struct {
		name   string
		cfg    Config
		env    string
		expect bool
	}{
		{
			name: "disabled never applies",
			cfg:  Config{Enabled: false, URL: "ghcr.io"},
			env:  "staging",
		},
		{
			name:   "empty environments means all",
			cfg:    Config{Enabled: true, URL: "ghcr.io"},
			env:    "staging",
			expect: true,
		},
		{
			name:   "listed environment applies",
			cfg:    Config{Enabled: true, URL: "ghcr.io", Environments: []string{"staging", "prod"}},
			env:    "staging",
			expect: true,
		},
		{
			name: "unlisted environment does not apply",
			cfg:  Config{Enabled: true, URL: "ghcr.io", Environments: []string{"prod"}},
			env:  "staging",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.AppliesToEnv(tt.env)
			if got != tt.expect {
				t.Errorf("AppliesToEnv(%q) = %v, want %v", tt.env, got, tt.expect)
			}
		})
	}
}
