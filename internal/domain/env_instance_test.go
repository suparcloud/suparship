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
			got := GenerateURL(tt.appName, tt.envName, tt.envType, false)
			if got != tt.want {
				t.Errorf("GenerateURL(%q, %q, %q) = %q, want %q",
					tt.appName, tt.envName, tt.envType, got, tt.want)
			}
		})
	}
}

func TestGenerateURLIsDeterministic(t *testing.T) {
	for i := 0; i < 3; i++ {
		got := GenerateURL("hello", "pr-42", AppEnvPreview, false)
		if got != "http://pr-42.hello.preview.localhost" {
			t.Errorf("run %d: GenerateURL not deterministic, got %q", i, got)
		}
	}
}

func TestGenerateURLScheme(t *testing.T) {
	for _, envType := range []AppEnvironmentType{AppEnvStaging, AppEnvProd, AppEnvPreview} {
		if url := GenerateURL("app", "pr-1", envType, false); !strings.HasPrefix(url, "http://") {
			t.Errorf("GenerateURL(secure=false) for %q: expected http:// prefix, got %q", envType, url)
		}
		if url := GenerateURL("app", "pr-1", envType, true); !strings.HasPrefix(url, "https://") {
			t.Errorf("GenerateURL(secure=true) for %q: expected https:// prefix, got %q", envType, url)
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
			want:    GenerateURL("hello", "staging", AppEnvStaging, false),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateURLWithDomain(tt.appName, tt.envName, tt.envType, tt.domain, false)
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
		URL:         GenerateURL("hello", "staging", AppEnvStaging, false),
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
		URL:         GenerateURL("hello", "pr-42", AppEnvPreview, false),
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
		url := GenerateURL(c.appName, c.envName, c.envType, false)

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

// ── IsDedicatedClusterTopology ────────────────────────────────────────────────

func TestIsDedicatedClusterTopology(t *testing.T) {
	tests := []struct {
		name string
		refs []string
		want bool
	}{
		{"empty list", []string{}, false},
		{"single empty ref", []string{""}, false},
		{"single non-empty ref (one env)", []string{"cluster-a"}, true},
		{"two distinct refs", []string{"cluster-a", "cluster-b"}, true},
		{"duplicate refs", []string{"cluster-a", "cluster-a"}, false},
		{"one empty among distinct", []string{"cluster-a", ""}, false},
		{"three distinct", []string{"c1", "c2", "c3"}, true},
		{"three with duplicate", []string{"c1", "c2", "c1"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsDedicatedClusterTopology(tt.refs)
			if got != tt.want {
				t.Errorf("IsDedicatedClusterTopology(%v) = %v, want %v", tt.refs, got, tt.want)
			}
		})
	}
}

// ── ResolveNamespace ──────────────────────────────────────────────────────────

func TestResolveNamespace(t *testing.T) {
	tests := []struct {
		name    string
		in      NamespaceResolveInput
		want    string
		wantErr bool
	}{
		{
			name: "shared cluster app scope — hardcoded default",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeApp, Dedicated: false,
			},
			want: "billing-api-staging",
		},
		{
			name: "dedicated cluster app scope — no env suffix",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "prod", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeApp, Dedicated: true,
			},
			want: "billing-api",
		},
		{
			name: "shared cluster project scope — hardcoded default",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeProject, Dedicated: false,
			},
			want: "billing-staging",
		},
		{
			name: "dedicated cluster project scope — no env suffix",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "prod", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeProject, Dedicated: true,
			},
			want: "billing",
		},
		{
			name: "org app default overrides hardcoded default",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeApp, Dedicated: false,
				OrgAppDefault: "{project}-{app}",
			},
			want: "billing-api",
		},
		{
			name: "org env pattern overrides org app default",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeApp, Dedicated: false,
				OrgAppDefault:    "{project}-{app}",
				OrgEnvAppPattern: "{project}-{app}-stg",
			},
			want: "billing-api-stg",
		},
		{
			name: "project pattern overrides org env pattern",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeApp, Dedicated: false,
				OrgEnvAppPattern: "{project}-{app}-stg",
				ProjectPattern:   "{project}-{app}-{env}",
			},
			want: "billing-api-staging",
		},
		{
			name: "app pattern is highest priority",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeApp, Dedicated: false,
				OrgEnvAppPattern: "{project}-{app}-stg",
				ProjectPattern:   "{project}-{app}-{env}",
				AppPattern:       "{app}",
			},
			want: "api",
		},
		{
			name: "org project default used for project scope",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeProject, Dedicated: false,
				OrgProjectDefault: "{project}-{env}",
			},
			want: "billing-staging",
		},
		// Regression: per-env app pattern containing "{app}" must NOT bleed
		// into project-scope resolution. Operators set per-env app patterns
		// for dedicated namespaces; applying them to a project-shared
		// namespace yields nonsensical results (one app's name as the
		// project namespace).
		{
			name: "scope=project ignores OrgEnvAppPattern",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeProject, Dedicated: true,
				OrgEnvAppPattern:  "{app}",
				OrgProjectDefault: "{project}",
			},
			want: "billing",
		},
		{
			name: "scope=project uses OrgEnvProjPattern when set",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeProject, Dedicated: true,
				OrgEnvAppPattern:  "{app}",
				OrgEnvProjPattern: "{project}-{env}",
				OrgProjectDefault: "{project}",
			},
			want: "billing-staging",
		},
		{
			name: "scope=app ignores OrgEnvProjPattern",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeApp, Dedicated: true,
				OrgEnvAppPattern:  "{project}-{app}",
				OrgEnvProjPattern: "{project}",
			},
			want: "billing-api",
		},
		{
			name: "empty scope treated as app scope",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Dedicated: false,
			},
			want: "billing-api-staging",
		},
		{
			name: "org token substituted",
			in: NamespaceResolveInput{
				AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeApp, Dedicated: false,
				AppPattern: "{org}-{project}-{app}",
			},
			want: "myorg-billing-api",
		},
		{
			name: "invalid namespace — uppercase in result",
			in: NamespaceResolveInput{
				AppName: "API", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
				Scope: NamespaceScopeApp, Dedicated: false,
				AppPattern: "{app}-{env}",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveNamespace(tt.in)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveNamespace() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ResolveNamespace() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveNamespaceIsDeterministic(t *testing.T) {
	in := NamespaceResolveInput{
		AppName: "api", EnvName: "staging", ProjectName: "billing", OrgName: "myorg",
		Scope: NamespaceScopeApp, Dedicated: false,
	}
	first, err := ResolveNamespace(in)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, err := ResolveNamespace(in)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Errorf("run %d: not deterministic, got %q, want %q", i, got, first)
		}
	}
}

func TestResolveNamespace_SharedStack(t *testing.T) {
	// A shared-namespace stack co-locates its apps under {project}-{stack}-{env},
	// overriding the app/project scope so members share a namespace (DNS).
	ns, err := ResolveNamespace(NamespaceResolveInput{
		AppName:     "agent-server",
		EnvName:     "staging",
		ProjectName: "voiceproj",
		Scope:       NamespaceScopeApp, // app scope is overridden by the shared stack
		StackName:   "voiceai",
		StackShared: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ns != "voiceproj-voiceai-staging" {
		t.Errorf("ns = %q, want voiceproj-voiceai-staging", ns)
	}

	// A different app in the same stack resolves to the SAME namespace.
	ns2, _ := ResolveNamespace(NamespaceResolveInput{
		AppName: "web", EnvName: "staging", ProjectName: "voiceproj",
		StackName: "voiceai", StackShared: true,
	})
	if ns2 != ns {
		t.Errorf("stack members must share a namespace: %q vs %q", ns2, ns)
	}

	// SharedNamespace off → falls back to the app-scope namespace.
	nsApp, _ := ResolveNamespace(NamespaceResolveInput{
		AppName: "web", EnvName: "staging", ProjectName: "voiceproj",
		StackName: "voiceai", StackShared: false,
	})
	if nsApp != "voiceproj-web-staging" {
		t.Errorf("non-shared stack ns = %q, want voiceproj-web-staging", nsApp)
	}

	// Custom stack pattern is honored.
	nsPat, _ := ResolveNamespace(NamespaceResolveInput{
		AppName: "web", EnvName: "prod", ProjectName: "voiceproj",
		StackName: "voiceai", StackShared: true, StackPattern: "{stack}-{env}",
	})
	if nsPat != "voiceai-prod" {
		t.Errorf("custom stack pattern ns = %q, want voiceai-prod", nsPat)
	}
}

// --- Preview namespace patterns ---

func TestGeneratePreviewNamespaceFromPattern(t *testing.T) {
	tests := []struct {
		name        string
		appName     string
		previewName string
		projectName string
		pattern     string
		want        string
	}{
		{"default empty pattern", "hello", "pr-42", "demo", "", "demo-hello-preview-pr-42"},
		{"app and name only", "hello", "pr-42", "demo", "{app}-{name}", "hello-pr-42"},
		{"project app name", "api", "pr-7", "acme", "{project}-{app}-{name}", "acme-api-pr-7"},
		{"branch name", "web", "feature-login", "shop", "{app}-{name}", "web-feature-login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GeneratePreviewNamespaceFromPattern(tt.appName, tt.previewName, tt.projectName, tt.pattern)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidatePreviewNamespacePattern(t *testing.T) {
	valid := []string{
		"",                               // empty → default
		"{project}-{app}-preview-{name}", // the default
		"{app}-{name}",
		"{project}-{name}",
		"preview-{name}",
		"{project}-previews",      // shared namespace (no {name}) is allowed
		"{project}-{app}-preview", // shared per-app namespace
	}
	for _, p := range valid {
		if err := ValidatePreviewNamespacePattern(p); err != nil {
			t.Errorf("pattern %q should be valid: %v", p, err)
		}
	}

	invalid := []string{
		"{project}-{app}-{env}-{name}", // unsupported {env} token
		"{name}_{app}",                 // underscore is not DNS-valid
	}
	for _, p := range invalid {
		if err := ValidatePreviewNamespacePattern(p); err == nil {
			t.Errorf("pattern %q should be rejected", p)
		}
	}
}
