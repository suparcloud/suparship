package secrets

import (
	"strings"
	"testing"
)

func TestResourceNaming_Defaults(t *testing.T) {
	var n ResourceNaming
	params := NamingParams{Org: "default", Env: "prod", Project: "acme", App: "web", Provider: "1password", Cluster: "prod-us"}

	tests := []struct {
		name   string
		render func() string
		want   string
	}{
		{"AppResource", func() string { return n.RenderAppResource(params) }, "web-secrets"},
		{"AppConfigMap", func() string { return n.RenderAppConfigMap(params) }, "web-config"},
		{"ClusterSecretStore", func() string { return n.RenderClusterSecretStore(params) }, "1password-prod"},
		{"VaultItem org", func() string { return n.RenderVaultItem(LevelOrg, params) }, "org"},
		{"VaultItem envType", func() string { return n.RenderVaultItem(LevelEnvironment, params) }, "env-prod"},
		{"VaultItem project", func() string { return n.RenderVaultItem(LevelProject, params) }, "acme"},
		{"VaultItem app", func() string { return n.RenderVaultItem(LevelApp, params) }, "acme-web"},
		{"VaultItem appEnv", func() string { return n.RenderVaultItem(LevelAppEnv, params) }, "acme-web-prod"},
		{"VaultItem cluster", func() string { return n.RenderVaultItem(LevelCluster, params) }, "cluster-prod-us"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.render()
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResourceNaming_CustomPatterns(t *testing.T) {
	n := ResourceNaming{
		AppResource:        "{app}-{env}",
		ClusterSecretStore: "{org}-{provider}-{env}",
		VaultItem: ItemNaming{
			Org:     "{org}-secrets",
			EnvType: "{org}-{env}",
			Project: "{project}-secrets",
			App:     "{project}-{app}-secrets",
			AppEnv:  "{project}-{app}-{env}-secrets",
		},
	}
	params := NamingParams{Org: "myorg", Env: "staging", Project: "billing", App: "api", Provider: "vault"}

	if got := n.RenderAppResource(params); got != "api-staging" {
		t.Errorf("AppResource: got %q, want %q", got, "api-staging")
	}
	if got := n.RenderClusterSecretStore(params); got != "myorg-vault-staging" {
		t.Errorf("ClusterSecretStore: got %q, want %q", got, "myorg-vault-staging")
	}
	if got := n.RenderVaultItem(LevelOrg, params); got != "myorg-secrets" {
		t.Errorf("VaultItem org: got %q, want %q", got, "myorg-secrets")
	}
	if got := n.RenderVaultItem(LevelAppEnv, params); got != "billing-api-staging-secrets" {
		t.Errorf("VaultItem appEnv: got %q, want %q", got, "billing-api-staging-secrets")
	}
}

func TestResourceNaming_Validate_Defaults(t *testing.T) {
	var n ResourceNaming
	if err := n.Validate(); err != nil {
		t.Errorf("defaults should validate: %v", err)
	}
}

func TestResourceNaming_Validate_DNS1123(t *testing.T) {
	n := ResourceNaming{AppResource: "{app} {env}"}
	err := n.Validate()
	if err == nil {
		t.Fatal("expected validation error for whitespace in name")
	}
	if !strings.Contains(err.Error(), "whitespace") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResourceNaming_Validate_CollisionDetection(t *testing.T) {
	n := ResourceNaming{
		VaultItem: ItemNaming{
			Project: "{project}",
			App:     "{project}", // collides with Project when app is empty
		},
	}
	err := n.Validate()
	if err == nil {
		t.Fatal("expected collision detection error")
	}
	if !strings.Contains(err.Error(), "collide") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateRenderedDNS1123(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "web", false},
		{"valid with dash", "acme-web-prod", false},
		{"empty", "", true},
		{"uppercase", "Web", true},
		{"starts with dash", "-web", true},
		{"contains space", "web prod", true},
		{"contains underscore", "web_prod", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRenderedDNS1123(tt.input, "test")
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRenderedDNS1123(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateRenderedNamespace(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		{"valid simple", "web", false, ""},
		{"valid with dash", "acme-web-prod", false, ""},
		{"valid 63 chars", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false, ""},
		{"invalid empty", "", true, ""},
		{"invalid uppercase", "Web", true, "DNS-1123"},
		{"invalid underscore", "web_prod", true, "DNS-1123"},
		{"invalid 64 chars", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true, "63-char"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRenderedNamespace(tt.input, "test")
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateRenderedNamespace(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if err != nil && tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestResourceNaming_NamespacePatterns(t *testing.T) {
	params := NamingParams{Org: "myorg", Env: "staging", Project: "billing", App: "api", Provider: "vault"}

	t.Run("defaults return empty (topology decides)", func(t *testing.T) {
		var n ResourceNaming
		if got := n.EffectiveProjectNamespace(); got != "" {
			t.Errorf("EffectiveProjectNamespace default: got %q, want empty", got)
		}
		if got := n.EffectiveAppNamespace(); got != "" {
			t.Errorf("EffectiveAppNamespace default: got %q, want empty", got)
		}
	})

	t.Run("custom project namespace pattern", func(t *testing.T) {
		n := ResourceNaming{ProjectNamespace: "{project}-{env}"}
		got := RenderPattern(n.EffectiveProjectNamespace(), params)
		if got != "billing-staging" {
			t.Errorf("got %q, want %q", got, "billing-staging")
		}
	})

	t.Run("custom app namespace pattern", func(t *testing.T) {
		n := ResourceNaming{AppNamespace: "{project}-{app}-{env}"}
		got := RenderPattern(n.EffectiveAppNamespace(), params)
		if got != "billing-api-staging" {
			t.Errorf("got %q, want %q", got, "billing-api-staging")
		}
	})

	t.Run("validate accepts valid namespace patterns", func(t *testing.T) {
		n := ResourceNaming{
			ProjectNamespace: "{project}-{env}",
			AppNamespace:     "{project}-{app}-{env}",
		}
		if err := n.Validate(); err != nil {
			t.Errorf("unexpected validation error: %v", err)
		}
	})

	t.Run("validate rejects invalid project namespace pattern", func(t *testing.T) {
		n := ResourceNaming{ProjectNamespace: "{project} {env}"}
		if err := n.Validate(); err == nil {
			t.Error("expected validation error for whitespace in projectNamespace")
		}
	})

	t.Run("validate rejects too-long app namespace pattern", func(t *testing.T) {
		// Renders to 64 chars with sample values
		long := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaX"
		n := ResourceNaming{AppNamespace: long}
		if err := n.Validate(); err == nil {
			t.Error("expected validation error for too-long appNamespace")
		}
	})
}
