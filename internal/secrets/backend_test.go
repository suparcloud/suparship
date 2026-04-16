package secrets

import "testing"

func TestBackendConfig_Effective(t *testing.T) {
	tests := []struct {
		name string
		cfg  BackendConfig
		want BackendType
	}{
		{"empty defaults to k8s", BackendConfig{}, BackendK8s},
		{"explicit k8s", BackendConfig{Type: BackendK8s}, BackendK8s},
		{"explicit vault", BackendConfig{Type: BackendVault}, BackendVault},
		{"explicit aws-sm", BackendConfig{Type: BackendAWSSM}, BackendAWSSM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.Effective()
			if got != tt.want {
				t.Errorf("Effective() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidBackendTypes(t *testing.T) {
	for _, bt := range []BackendType{BackendK8s, BackendVault, BackendAWSSM} {
		if !ValidBackendTypes[bt] {
			t.Errorf("%q should be valid", bt)
		}
	}
	if ValidBackendTypes["invalid"] {
		t.Error("'invalid' should not be valid")
	}
}
