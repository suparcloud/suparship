package domain

import (
	"strings"
	"testing"
	"time"
)

// ── GenerateNamespace ─────────────────────────────────────────────────────────

func TestGenerateNamespace(t *testing.T) {
	tests := []struct {
		name    string
		appName string
		envName string
		envType AppEnvironmentType
		want    string
	}{
		{
			name:    "staging",
			appName: "hello",
			envName: "staging",
			envType: AppEnvStaging,
			want:    "hello-staging",
		},
		{
			name:    "prod",
			appName: "hello",
			envName: "prod",
			envType: AppEnvProd,
			want:    "hello-prod",
		},
		{
			name:    "preview pr-42",
			appName: "hello",
			envName: "pr-42",
			envType: AppEnvPreview,
			want:    "hello-pr-42",
		},
		{
			name:    "preview feature branch",
			appName: "my-api",
			envName: "feature-branch",
			envType: AppEnvPreview,
			want:    "my-api-feature-branch",
		},
		{
			name:    "staging hyphenated app",
			appName: "api-gateway",
			envName: "staging",
			envType: AppEnvStaging,
			want:    "api-gateway-staging",
		},
		{
			name:    "prod hyphenated app",
			appName: "api-gateway",
			envName: "prod",
			envType: AppEnvProd,
			want:    "api-gateway-prod",
		},
		{
			name:    "preview with numeric suffix",
			appName: "svc",
			envName: "pr-182",
			envType: AppEnvPreview,
			want:    "svc-pr-182",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateNamespace(tt.appName, tt.envName, tt.envType)
			if got != tt.want {
				t.Errorf("GenerateNamespace(%q, %q, %q) = %q, want %q",
					tt.appName, tt.envName, tt.envType, got, tt.want)
			}
		})
	}
}

func TestGenerateNamespaceIsDeterministic(t *testing.T) {
	for i := 0; i < 3; i++ {
		got := GenerateNamespace("hello", "pr-42", AppEnvPreview)
		if got != "hello-pr-42" {
			t.Errorf("run %d: GenerateNamespace not deterministic, got %q", i, got)
		}
	}
}

// GenerateNamespace for preview must agree with the legacy AppPreviewNamespace.
func TestGenerateNamespaceMatchesLegacyHelper(t *testing.T) {
	pairs := [][2]string{
		{"hello", "pr-42"},
		{"my-app", "feature-branch"},
		{"api", "pr-182"},
	}
	for _, p := range pairs {
		appName, previewName := p[0], p[1]
		got := GenerateNamespace(appName, previewName, AppEnvPreview)
		legacy := AppPreviewNamespace(appName, previewName)
		if got != legacy {
			t.Errorf("GenerateNamespace(%q, %q, preview) = %q, legacy = %q",
				appName, previewName, got, legacy)
		}
	}
}

// ── GenerateNamespaceFromPattern ──────────────────────────────────────────────

func TestGenerateNamespaceFromPattern(t *testing.T) {
	tests := []struct {
		name        string
		appName     string
		envName     string
		projectName string
		pattern     string
		want        string
	}{
		{
			name:    "default empty pattern uses app-env",
			appName: "hello", envName: "staging", projectName: "demo",
			pattern: "",
			want:    "hello-staging",
		},
		{
			name:    "app-only pattern — dedicated cluster",
			appName: "hello", envName: "staging", projectName: "demo",
			pattern: "{app}",
			want:    "hello",
		},
		{
			name:    "app-env pattern — shared cluster",
			appName: "hello", envName: "staging", projectName: "demo",
			pattern: "{app}-{env}",
			want:    "hello-staging",
		},
		{
			name:    "project-app pattern",
			appName: "hello", envName: "staging", projectName: "demo",
			pattern: "{project}-{app}",
			want:    "demo-hello",
		},
		{
			name:    "all tokens",
			appName: "hello", envName: "stg", projectName: "acme",
			pattern: "{project}-{env}-{app}",
			want:    "acme-stg-hello",
		},
		{
			name:    "preview — app-env still works",
			appName: "hello", envName: "pr-42", projectName: "demo",
			pattern: "{app}-{env}",
			want:    "hello-pr-42",
		},
		{
			name:    "hyphenated app and project",
			appName: "api-gateway", envName: "prod", projectName: "my-project",
			pattern: "{app}",
			want:    "api-gateway",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateNamespaceFromPattern(tt.appName, tt.envName, tt.projectName, tt.pattern)
			if got != tt.want {
				t.Errorf("GenerateNamespaceFromPattern(%q, %q, %q, %q) = %q, want %q",
					tt.appName, tt.envName, tt.projectName, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestGenerateNamespaceFromPatternIsDeterministic(t *testing.T) {
	for i := 0; i < 3; i++ {
		got := GenerateNamespaceFromPattern("hello", "staging", "demo", "{app}")
		if got != "hello" {
			t.Errorf("run %d: not deterministic, got %q", i, got)
		}
	}
}

// GenerateNamespace must equal GenerateNamespaceFromPattern with default pattern.
func TestGenerateNamespaceEqualsFromPatternDefault(t *testing.T) {
	cases := []struct{ app, env string }{
		{"hello", "staging"},
		{"hello", "prod"},
		{"hello", "pr-42"},
		{"api-gateway", "feature-branch"},
	}
	for _, c := range cases {
		legacy := GenerateNamespace(c.app, c.env, AppEnvStaging)
		fromPattern := GenerateNamespaceFromPattern(c.app, c.env, "", "")
		if legacy != fromPattern {
			t.Errorf("GenerateNamespace(%q, %q) = %q, GenerateNamespaceFromPattern default = %q",
				c.app, c.env, legacy, fromPattern)
		}
	}
}

// ── GenerateURL ───────────────────────────────────────────────────────────────

func TestGenerateURL(t *testing.T) {
	tests := []struct {
		name    string
		appName string
		envName string
		envType AppEnvironmentType
		want    string
	}{
		{
			name:    "staging",
			appName: "hello",
			envName: "staging",
			envType: AppEnvStaging,
			want:    "http://hello.staging.localhost",
		},
		{
			name:    "prod",
			appName: "hello",
			envName: "prod",
			envType: AppEnvProd,
			want:    "http://hello.prod.localhost",
		},
		{
			name:    "preview pr-42",
			appName: "hello",
			envName: "pr-42",
			envType: AppEnvPreview,
			want:    "http://pr-42.hello.preview.localhost",
		},
		{
			name:    "preview feature branch",
			appName: "my-api",
			envName: "feature-branch",
			envType: AppEnvPreview,
			want:    "http://feature-branch.my-api.preview.localhost",
		},
		{
			name:    "staging hyphenated app",
			appName: "api-gateway",
			envName: "staging",
			envType: AppEnvStaging,
			want:    "http://api-gateway.staging.localhost",
		},
		{
			name:    "prod hyphenated app",
			appName: "api-gateway",
			envName: "prod",
			envType: AppEnvProd,
			want:    "http://api-gateway.prod.localhost",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateURL(tt.appName, tt.envName, tt.envType)
			if got != tt.want {
				t.Errorf("GenerateURL(%q, %q, %q) = %q, want %q",
					tt.appName, tt.envName, tt.envType, got, tt.want)
			}
		})
	}
}

func TestGenerateURLIsDeterministic(t *testing.T) {
	for i := 0; i < 3; i++ {
		got := GenerateURL("hello", "pr-42", AppEnvPreview)
		if got != "http://pr-42.hello.preview.localhost" {
			t.Errorf("run %d: GenerateURL not deterministic, got %q", i, got)
		}
	}
}

func TestGenerateURLHasHTTPScheme(t *testing.T) {
	for _, envType := range []AppEnvironmentType{AppEnvStaging, AppEnvProd, AppEnvPreview} {
		url := GenerateURL("app", "pr-1", envType)
		if !strings.HasPrefix(url, "http://") {
			t.Errorf("GenerateURL for %q: expected http:// prefix, got %q", envType, url)
		}
	}
}

// ── GenerateURLWithDomain ─────────────────────────────────────────────────────

func TestGenerateURLWithDomain(t *testing.T) {
	tests := []struct {
		name    string
		appName string
		envName string
		envType AppEnvironmentType
		domain  string
		want    string
	}{
		{
			name:    "staging custom domain",
			appName: "hello",
			envName: "staging",
			envType: AppEnvStaging,
			domain:  "example.com",
			want:    "http://hello.staging.example.com",
		},
		{
			name:    "prod custom domain",
			appName: "hello",
			envName: "prod",
			envType: AppEnvProd,
			domain:  "example.com",
			want:    "http://hello.prod.example.com",
		},
		{
			name:    "preview custom domain",
			appName: "hello",
			envName: "pr-42",
			envType: AppEnvPreview,
			domain:  "example.com",
			want:    "http://pr-42.hello.preview.example.com",
		},
		{
			name:    "localhost domain matches GenerateURL",
			appName: "hello",
			envName: "staging",
			envType: AppEnvStaging,
			domain:  "localhost",
			want:    GenerateURL("hello", "staging", AppEnvStaging),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateURLWithDomain(tt.appName, tt.envName, tt.envType, tt.domain)
			if got != tt.want {
				t.Errorf("GenerateURLWithDomain(%q, %q, %q, %q) = %q, want %q",
					tt.appName, tt.envName, tt.envType, tt.domain, got, tt.want)
			}
		})
	}
}

// ── EnvironmentInstance struct ────────────────────────────────────────────────

func TestEnvironmentInstanceZeroValue(t *testing.T) {
	var inst EnvironmentInstance
	if inst.AppName != "" {
		t.Errorf("zero AppName should be empty, got %q", inst.AppName)
	}
	if inst.Release != nil {
		t.Error("zero Release should be nil")
	}
	if !inst.CreatedAt.IsZero() {
		t.Errorf("zero CreatedAt should be zero time, got %v", inst.CreatedAt)
	}
}

func TestEnvironmentInstanceFields(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rel := &AppReleaseRef{Image: "ghcr.io/org/app:v1.0.0", Tag: "v1.0.0", Commit: "abc1234"}

	inst := EnvironmentInstance{
		AppName:     "hello",
		ProjectName: "demo",
		EnvType:     AppEnvStaging,
		EnvName:     "staging",
		Namespace:   GenerateNamespace("hello", "staging", AppEnvStaging),
		URL:         GenerateURL("hello", "staging", AppEnvStaging),
		Release:     rel,
		Status:      AppRuntimeStatus{Phase: StatusHealthy, Replicas: 2, Available: 2},
		CreatedAt:   now,
	}

	if inst.Namespace != "hello-staging" {
		t.Errorf("Namespace = %q, want %q", inst.Namespace, "hello-staging")
	}
	if inst.URL != "http://hello.staging.localhost" {
		t.Errorf("URL = %q, want %q", inst.URL, "http://hello.staging.localhost")
	}
	if inst.Release == nil || inst.Release.Tag != "v1.0.0" {
		t.Errorf("Release.Tag = unexpected value: %v", inst.Release)
	}
	if inst.Status.Phase != StatusHealthy {
		t.Errorf("Status.Phase = %q, want %q", inst.Status.Phase, StatusHealthy)
	}
	if !inst.CreatedAt.Equal(now) {
		t.Errorf("CreatedAt = %v, want %v", inst.CreatedAt, now)
	}
}

func TestEnvironmentInstancePreviewFields(t *testing.T) {
	inst := EnvironmentInstance{
		AppName:     "hello",
		ProjectName: "demo",
		EnvType:     AppEnvPreview,
		EnvName:     "pr-42",
		Namespace:   GenerateNamespace("hello", "pr-42", AppEnvPreview),
		URL:         GenerateURL("hello", "pr-42", AppEnvPreview),
		Status:      AppRuntimeStatus{Phase: StatusNotDeployed},
		CreatedAt:   time.Now(),
	}

	if inst.EnvType != AppEnvPreview {
		t.Errorf("EnvType = %q, want preview", inst.EnvType)
	}
	if inst.Namespace != "hello-pr-42" {
		t.Errorf("Namespace = %q, want %q", inst.Namespace, "hello-pr-42")
	}
	if inst.URL != "http://pr-42.hello.preview.localhost" {
		t.Errorf("URL = %q, want %q", inst.URL, "http://pr-42.hello.preview.localhost")
	}
	if inst.Release != nil {
		t.Errorf("Release should be nil for undeployed preview, got %v", inst.Release)
	}
}

func TestEnvironmentInstanceReleaseNilWhenNotDeployed(t *testing.T) {
	inst := EnvironmentInstance{
		AppName: "hello",
		EnvType: AppEnvStaging,
		EnvName: "staging",
		Status:  AppRuntimeStatus{Phase: StatusNotDeployed},
	}
	if inst.Release != nil {
		t.Error("Release should be nil when not deployed")
	}
}

// GenerateNamespace (default pattern) must contain both app name and env name.
func TestGenerateNamespaceDefaultContainsAppAndEnv(t *testing.T) {
	cases := []struct {
		appName string
		envName string
		envType AppEnvironmentType
	}{
		{"hello", "staging", AppEnvStaging},
		{"hello", "prod", AppEnvProd},
		{"hello", "pr-42", AppEnvPreview},
		{"api-gateway", "feature-branch", AppEnvPreview},
	}

	for _, c := range cases {
		ns := GenerateNamespace(c.appName, c.envName, c.envType)
		url := GenerateURL(c.appName, c.envName, c.envType)

		if !strings.Contains(ns, c.appName) {
			t.Errorf("GenerateNamespace(%q, %q, %q) = %q: missing app name",
				c.appName, c.envName, c.envType, ns)
		}
		if !strings.Contains(ns, c.envName) {
			t.Errorf("GenerateNamespace(%q, %q, %q) = %q: missing env name",
				c.appName, c.envName, c.envType, ns)
		}
		if !strings.Contains(url, c.appName) {
			t.Errorf("GenerateURL(%q, %q, %q) = %q: missing app name",
				c.appName, c.envName, c.envType, url)
		}
	}
}

// GenerateNamespaceFromPattern with {app}-only pattern must contain app name only.
func TestGenerateNamespaceFromPatternAppOnly(t *testing.T) {
	ns := GenerateNamespaceFromPattern("hello", "staging", "demo", "{app}")
	if !strings.Contains(ns, "hello") {
		t.Errorf("pattern {app}: namespace %q missing app name", ns)
	}
	if strings.Contains(ns, "staging") {
		t.Errorf("pattern {app}: namespace %q unexpectedly contains env name", ns)
	}
}
