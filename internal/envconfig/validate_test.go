package envconfig_test

import (
	"testing"

	"github.com/suparcloud/suparship/internal/envconfig"
)

func TestValidateEnvConfig_ValidVars(t *testing.T) {
	cfg := envconfig.EnvConfig{
		Vars: map[string]string{
			"LOG_LEVEL":    "info",
			"_PRIVATE":     "x",
			"DB_URL_2":     "postgres://...",
			"CamelCase":    "ok",
		},
	}
	if err := envconfig.ValidateEnvConfig(cfg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateEnvConfig_InvalidVarKey(t *testing.T) {
	cases := []struct {
		key string
	}{
		{""},
		{"1STARTS_WITH_NUMBER"},
		{"has space"},
		{"has=equals"},
		{"has-hyphen"},
		{"has.dot"},
	}
	for _, tc := range cases {
		cfg := envconfig.EnvConfig{
			Vars: map[string]string{tc.key: "value"},
		}
		if err := envconfig.ValidateEnvConfig(cfg); err == nil {
			t.Errorf("expected error for key %q, got nil", tc.key)
		}
	}
}

func TestValidateSecretRef_Valid(t *testing.T) {
	ref := envconfig.SecretRef{
		Provider: "k8s",
		Name:     "my-secret",
		Key:      "password",
		EnvKey:   "DB_PASSWORD",
	}
	if err := envconfig.ValidateSecretRef(ref); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateSecretRef_MissingFields(t *testing.T) {
	base := envconfig.SecretRef{
		Provider: "k8s",
		Name:     "my-secret",
		Key:      "password",
		EnvKey:   "DB_PASSWORD",
	}

	cases := []struct {
		name   string
		mutate func(*envconfig.SecretRef)
	}{
		{"missing provider", func(r *envconfig.SecretRef) { r.Provider = "" }},
		{"missing name", func(r *envconfig.SecretRef) { r.Name = "" }},
		{"missing key", func(r *envconfig.SecretRef) { r.Key = "" }},
		{"missing envKey", func(r *envconfig.SecretRef) { r.EnvKey = "" }},
		{"unknown provider", func(r *envconfig.SecretRef) { r.Provider = "gcp-sm" }},
		{"invalid envKey", func(r *envconfig.SecretRef) { r.EnvKey = "invalid-key" }},
	}
	for _, tc := range cases {
		ref := base
		tc.mutate(&ref)
		if err := envconfig.ValidateSecretRef(ref); err == nil {
			t.Errorf("case %q: expected error, got nil", tc.name)
		}
	}
}

func TestValidateEnvConfig_SecretRefErrors(t *testing.T) {
	cfg := envconfig.EnvConfig{
		SecretRefs: []envconfig.SecretRef{
			{Provider: "k8s", Name: "s", Key: "k", EnvKey: "VALID"},
			{Provider: "", Name: "s", Key: "k", EnvKey: "VALID"}, // invalid
		},
	}
	if err := envconfig.ValidateEnvConfig(cfg); err == nil {
		t.Fatal("expected error for invalid SecretRef, got nil")
	}
}
