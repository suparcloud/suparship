package helmvalues

import (
	"encoding/json"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/secrets"
)

// noNaming returns the zero-value ResourceNaming used by tests that don't
// care about envFrom names (the focus is the routing-profile output).
func noNaming() secrets.ResourceNaming { return secrets.ResourceNaming{} }

// ── helpers ───────────────────────────────────────────────────────────────────

func webApp(name string, components ...domain.ComponentSpec) *domain.App {
	return &domain.App{
		Name:        name,
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service", Version: "1.0.0"},
			Values: map[string]any{
				imageRepositoryKey: "ghcr.io/org/" + name,
				imageTagKey:        "v1.0.0",
			},
			Components: components,
		},
	}
}

func webComponent(name string) domain.ComponentSpec {
	return domain.ComponentSpec{
		Name:           name,
		Type:           domain.ComponentWeb,
		Enabled:        true,
		ExposeMode:     domain.ExposeExternal,
		PreviewEnabled: true,
	}
}

func workerComponent(name string) domain.ComponentSpec {
	return domain.ComponentSpec{
		Name:           name,
		Type:           domain.ComponentWorker,
		Enabled:        true,
		PreviewEnabled: false,
	}
}

func cronComponent(name string) domain.ComponentSpec {
	return domain.ComponentSpec{
		Name:           name,
		Type:           domain.ComponentCron,
		Enabled:        true,
		PreviewEnabled: false,
	}
}

// ── app.name / app.env ────────────────────────────────────────────────────────

func TestMapToHelmValues_AppContext(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)

	if hv.App.Name != "hello" {
		t.Errorf("App.Name = %q, want %q", hv.App.Name, "hello")
	}
	if hv.App.Env != "staging" {
		t.Errorf("App.Env = %q, want %q", hv.App.Env, "staging")
	}
}

func TestMapToHelmValues_AppContext_Preview(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "pr-42", domain.AppEnvPreview)

	if hv.App.Env != "pr-42" {
		t.Errorf("App.Env = %q, want %q", hv.App.Env, "pr-42")
	}
}

// ── image extraction ──────────────────────────────────────────────────────────

func TestMapToHelmValues_ImageFromValues(t *testing.T) {
	app := webApp("myapp", webComponent("web"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)

	wc := hv.Components["web"]
	if wc == nil {
		t.Fatal("component 'web' missing from output")
	}
	if wc.Image.Repository != "ghcr.io/org/myapp" {
		t.Errorf("Image.Repository = %q, want %q", wc.Image.Repository, "ghcr.io/org/myapp")
	}
	if wc.Image.Tag != "v1.0.0" {
		t.Errorf("Image.Tag = %q, want %q", wc.Image.Tag, "v1.0.0")
	}
}

func TestMapToHelmValues_ImageMissingFromValues(t *testing.T) {
	app := &domain.App{
		Name:        "bare",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{webComponent("web")},
		},
	}
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	wc := hv.Components["web"]
	if wc.Image.Repository != "" || wc.Image.Tag != "" {
		t.Errorf("expected empty image for app with no values, got repo=%q tag=%q",
			wc.Image.Repository, wc.Image.Tag)
	}
}

// ── enabled flag ──────────────────────────────────────────────────────────────

func TestMapToHelmValues_EnabledTrue(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if !hv.Components["web"].Enabled {
		t.Error("web component should be enabled in staging")
	}
}

func TestMapToHelmValues_DisabledComponent(t *testing.T) {
	c := webComponent("web")
	c.Enabled = false
	app := webApp("hello", c)
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if hv.Components["web"].Enabled {
		t.Error("disabled component should remain disabled in staging")
	}
}

func TestMapToHelmValues_PreviewDisablesNonPreviewComponents(t *testing.T) {
	app := webApp("hello", webComponent("web"), workerComponent("worker"))
	hv := MapToHelmValues(app, "pr-42", domain.AppEnvPreview)

	if !hv.Components["web"].Enabled {
		t.Error("web (PreviewEnabled=true) should be enabled in preview")
	}
	if hv.Components["worker"].Enabled {
		t.Error("worker (PreviewEnabled=false) should be disabled in preview")
	}
}

func TestMapToHelmValues_PreviewKeepsPreviewEnabledComponents(t *testing.T) {
	c := workerComponent("worker")
	c.PreviewEnabled = true
	app := webApp("hello", webComponent("web"), c)
	hv := MapToHelmValues(app, "pr-42", domain.AppEnvPreview)
	if !hv.Components["worker"].Enabled {
		t.Error("worker with PreviewEnabled=true should stay enabled in preview")
	}
}

// ── replicas ──────────────────────────────────────────────────────────────────

func TestMapToHelmValues_ReplicasDefaultStaging(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if hv.Components["web"].Replicas != defaultReplicas {
		t.Errorf("Replicas = %d, want %d (staging default)", hv.Components["web"].Replicas, defaultReplicas)
	}
}

func TestMapToHelmValues_ReplicasDefaultPreview(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "pr-42", domain.AppEnvPreview)
	if hv.Components["web"].Replicas != defaultPreviewReplicas {
		t.Errorf("Replicas = %d, want %d (preview default)", hv.Components["web"].Replicas, defaultPreviewReplicas)
	}
}

func TestMapToHelmValues_ReplicasFromComponentSpec(t *testing.T) {
	c := webComponent("web")
	c.Replicas = 5
	app := webApp("hello", c)
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if hv.Components["web"].Replicas != 5 {
		t.Errorf("Replicas = %d, want 5", hv.Components["web"].Replicas)
	}
}

func TestMapToHelmValues_ReplicasEnvOverride(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"prod": {Replicas: 4},
	}
	hv := MapToHelmValues(app, "prod", domain.AppEnvProd)
	if hv.Components["web"].Replicas != 4 {
		t.Errorf("Replicas = %d, want 4 (env override)", hv.Components["web"].Replicas)
	}
}

func TestMapToHelmValues_ComponentReplicasTakesPrecedenceOverEnvOverride(t *testing.T) {
	c := webComponent("web")
	c.Replicas = 3
	app := webApp("hello", c)
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"prod": {Replicas: 4},
	}
	hv := MapToHelmValues(app, "prod", domain.AppEnvProd)
	if hv.Components["web"].Replicas != 3 {
		t.Errorf("Replicas = %d, want 3 (component spec wins over env override)", hv.Components["web"].Replicas)
	}
}

// ── expose ────────────────────────────────────────────────────────────────────

func TestMapToHelmValues_IngressOnlyOnRoutingComponent(t *testing.T) {
	// The legacy shim populates IngressValues for the routing component
	// when no profiles are configured (mapper falls back to nginx-no-TLS).
	// The worker component has ExposeMode=disabled (zero-value) and gets
	// no Ingress regardless of profiles.
	app := webApp("hello", webComponent("web"), workerComponent("worker"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)

	if hv.Components["worker"].Ingress != nil {
		t.Errorf("worker should not have Ingress, got %+v", hv.Components["worker"].Ingress)
	}
}

// ── env (config) ──────────────────────────────────────────────────────────────

func TestMapToHelmValues_EnvFromComponentConfig(t *testing.T) {
	c := webComponent("web")
	c.Config = map[string]string{"LOG_LEVEL": "debug", "PORT": "8080"}
	app := webApp("hello", c)
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)

	env := hv.Components["web"].Env
	if env["LOG_LEVEL"] != "debug" {
		t.Errorf("LOG_LEVEL = %q, want %q", env["LOG_LEVEL"], "debug")
	}
	if env["PORT"] != "8080" {
		t.Errorf("PORT = %q, want %q", env["PORT"], "8080")
	}
}

func TestMapToHelmValues_EnvOverrideMergesWithComponentConfig(t *testing.T) {
	c := webComponent("web")
	c.Config = map[string]string{"LOG_LEVEL": "debug", "BASE": "value"}
	app := webApp("hello", c)
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"prod": {Config: map[string]string{"LOG_LEVEL": "warn", "EXTRA": "yes"}},
	}
	hv := MapToHelmValues(app, "prod", domain.AppEnvProd)
	env := hv.Components["web"].Env

	if env["LOG_LEVEL"] != "warn" {
		t.Errorf("LOG_LEVEL = %q, want %q (env override should win)", env["LOG_LEVEL"], "warn")
	}
	if env["BASE"] != "value" {
		t.Errorf("BASE = %q, want %q (component value should survive)", env["BASE"], "value")
	}
	if env["EXTRA"] != "yes" {
		t.Errorf("EXTRA = %q, want %q (env override key should appear)", env["EXTRA"], "yes")
	}
}

func TestMapToHelmValues_NoEnvWhenConfigEmpty(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if hv.Components["web"].Env != nil {
		t.Errorf("Env should be nil when no config is set, got %v", hv.Components["web"].Env)
	}
}

// ── resources (size preset) ───────────────────────────────────────────────────

func TestMapToHelmValues_ResourcesOmittedWhenNoPreset(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if hv.Components["web"].Resources != nil {
		t.Errorf("Resources should be nil when no size preset is set, got %v", hv.Components["web"].Resources)
	}
}

func TestMapToHelmValues_ResourcesFromComponentSizePreset(t *testing.T) {
	c := webComponent("web")
	c.SizePreset = domain.SizeLarge
	app := webApp("hello", c)
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)

	if hv.Components["web"].Resources == nil {
		t.Fatal("Resources should be set when SizePreset is configured")
	}
	if hv.Components["web"].Resources.Size != "large" {
		t.Errorf("Resources.Size = %q, want %q", hv.Components["web"].Resources.Size, "large")
	}
}

func TestMapToHelmValues_ResourcesEnvOverridePreset(t *testing.T) {
	c := webComponent("web")
	c.SizePreset = domain.SizeSmall
	app := webApp("hello", c)
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"prod": {SizePreset: domain.SizeLarge},
	}
	hv := MapToHelmValues(app, "prod", domain.AppEnvProd)
	if hv.Components["web"].Resources.Size != "large" {
		t.Errorf("Resources.Size = %q, want %q (env override)", hv.Components["web"].Resources.Size, "large")
	}
}

// ── routing ───────────────────────────────────────────────────────────────────

func TestMapToHelmValues_RoutingHostStaging(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	want := "hello.staging.localhost"
	if hv.Routing.Host != want {
		t.Errorf("Routing.Host = %q, want %q", hv.Routing.Host, want)
	}
}

func TestMapToHelmValues_RoutingHostProd(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "prod", domain.AppEnvProd)
	want := "hello.prod.localhost"
	if hv.Routing.Host != want {
		t.Errorf("Routing.Host = %q, want %q", hv.Routing.Host, want)
	}
}

func TestMapToHelmValues_RoutingHostPreview(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "pr-42", domain.AppEnvPreview)
	want := "pr-42.hello.preview.localhost"
	if hv.Routing.Host != want {
		t.Errorf("Routing.Host = %q, want %q", hv.Routing.Host, want)
	}
}

func TestMapToHelmValues_RoutingComponentFromExposedComponent(t *testing.T) {
	app := webApp("hello", webComponent("web"), workerComponent("worker"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if hv.Routing.Component != "web" {
		t.Errorf("Routing.Component = %q, want %q", hv.Routing.Component, "web")
	}
}

func TestMapToHelmValues_RoutingComponentFallsBackToWebType(t *testing.T) {
	c := webComponent("web")
	c.ExposeMode = domain.ExposeDisabled // not explicitly exposed
	app := webApp("hello", c, workerComponent("worker"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if hv.Routing.Component != "web" {
		t.Errorf("Routing.Component = %q, want %q (fallback to web type)", hv.Routing.Component, "web")
	}
}

func TestMapToHelmValues_RoutingComponentFallsBackToFirstAlphabetically(t *testing.T) {
	w := workerComponent("worker")
	c := cronComponent("cron")
	app := webApp("hello", w, c)
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if hv.Routing.Component != "cron" {
		t.Errorf("Routing.Component = %q, want %q (first alphabetically)", hv.Routing.Component, "cron")
	}
}

func TestMapToHelmValues_RoutingComponentPicksFirstExposedAlphabetically(t *testing.T) {
	api := webComponent("api")
	frontend := webComponent("frontend")
	app := webApp("hello", frontend, api)
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if hv.Routing.Component != "api" {
		t.Errorf("Routing.Component = %q, want %q (first exposed alphabetically)", hv.Routing.Component, "api")
	}
}

// ── multi-component ───────────────────────────────────────────────────────────

func TestMapToHelmValues_MultiComponentAllPresent(t *testing.T) {
	app := webApp("api-gw", webComponent("web"), workerComponent("worker"), cronComponent("cron"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)

	for _, name := range []string{"web", "worker", "cron"} {
		if hv.Components[name] == nil {
			t.Errorf("component %q missing from output", name)
		}
	}
	if len(hv.Components) != 3 {
		t.Errorf("expected 3 components, got %d", len(hv.Components))
	}
}

func TestMapToHelmValues_EmptyComponentsProducesEmptyMap(t *testing.T) {
	app := &domain.App{Name: "bare", ProjectName: "demo"}
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)
	if hv.Components == nil {
		t.Error("Components should not be nil")
	}
	if len(hv.Components) != 0 {
		t.Errorf("expected 0 components, got %d", len(hv.Components))
	}
}

// ── determinism ───────────────────────────────────────────────────────────────

func TestMapToHelmValues_IsDeterministic(t *testing.T) {
	app := webApp("hello",
		webComponent("web"),
		workerComponent("worker"),
		cronComponent("cron"),
	)
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"staging": {Replicas: 3, Config: map[string]string{"ENV": "staging"}},
	}

	first := marshal(t, MapToHelmValues(app, "staging", domain.AppEnvStaging))
	for i := 0; i < 5; i++ {
		got := marshal(t, MapToHelmValues(app, "staging", domain.AppEnvStaging))
		if got != first {
			t.Errorf("run %d: non-deterministic output", i+1)
		}
	}
}

// marshal serializes v to a canonical JSON string for comparison.
func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	return string(b)
}

// ── stripScheme ───────────────────────────────────────────────────────────────

// ── ingress / routing profiles ────────────────────────────────────────────────

func TestResolveIngress_NoProfilesYieldsNoIngress(t *testing.T) {
	// ExposeMode set but no profiles configured: the mapper drops the
	// ingress silently. Validation lives in domain.ValidateExposeModes,
	// which the publisher and app-save handlers run before reaching here.
	app := webApp("hello", webComponent("web"))
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)

	if hv.Components["web"].Ingress != nil {
		t.Errorf("no profiles should yield nil Ingress, got %+v", hv.Components["web"].Ingress)
	}
}

func TestResolveIngress_NeverAppliedToWorker(t *testing.T) {
	// Worker has ExposeMode=disabled (zero value): never gets an Ingress
	// regardless of profiles. Only the routing component receives one.
	c := workerComponent("worker")
	app := webApp("hello", c)
	org := domain.RoutingProfiles{
		string(domain.ExposeExternal): {IngressClassName: "nginx", ClusterIssuer: "letsencrypt-prod"},
	}
	hv := MapToHelmValuesForEnv(app, "staging", domain.AppEnvStaging, "localhost", "", "", "", org, nil, nil, nil)

	if hv.Components["worker"].Ingress != nil {
		t.Errorf("worker should not have Ingress, got %+v", hv.Components["worker"].Ingress)
	}
}

func TestResolveIngress_DisabledMode(t *testing.T) {
	c := webComponent("web")
	c.ExposeMode = domain.ExposeDisabled
	app := webApp("hello", c)
	hv := MapToHelmValues(app, "staging", domain.AppEnvStaging)

	if hv.Components["web"].Ingress != nil {
		t.Errorf("disabled mode should produce nil Ingress, got %+v", hv.Components["web"].Ingress)
	}
}

func TestResolveIngress_FromOrgProfile_NoTLS(t *testing.T) {
	c := webComponent("web")
	c.ExposeMode = domain.ExposeInternal
	app := webApp("hello", c)
	org := domain.RoutingProfiles{
		string(domain.ExposeInternal): {IngressClassName: "nginx-internal"},
	}
	hv := MapToHelmValuesForEnv(app, "staging", domain.AppEnvStaging, "localhost", "", "", "", org, nil, nil, nil)

	got := hv.Components["web"].Ingress
	if got == nil {
		t.Fatal("expected Ingress from internal profile")
	}
	if got.ClassName != "nginx-internal" {
		t.Errorf("ClassName = %q, want nginx-internal", got.ClassName)
	}
	if got.ClusterIssuer != "" {
		t.Errorf("internal profile has no TLS; ClusterIssuer = %q, want empty", got.ClusterIssuer)
	}
}

func TestResolveIngress_FromOrgProfile_WithTLS(t *testing.T) {
	c := webComponent("web")
	c.ExposeMode = domain.ExposeExternal
	app := webApp("hello", c)
	org := domain.RoutingProfiles{
		string(domain.ExposeExternal): {IngressClassName: "nginx", ClusterIssuer: "letsencrypt-prod"},
	}
	hv := MapToHelmValuesForEnv(app, "prod", domain.AppEnvProd, "acme.com", "", "", "", org, nil, nil, nil)

	got := hv.Components["web"].Ingress
	if got == nil {
		t.Fatal("expected Ingress from external profile")
	}
	if got.ClusterIssuer != "letsencrypt-prod" {
		t.Errorf("ClusterIssuer = %q, want letsencrypt-prod", got.ClusterIssuer)
	}
}

func TestResolveIngress_EnvProfileOverridesOrg(t *testing.T) {
	c := webComponent("web")
	c.ExposeMode = domain.ExposeExternal
	app := webApp("hello", c)
	org := domain.RoutingProfiles{
		string(domain.ExposeExternal): {IngressClassName: "nginx", ClusterIssuer: "letsencrypt-prod"},
	}
	env := domain.RoutingProfiles{
		string(domain.ExposeExternal): {IngressClassName: "nginx-staging", ClusterIssuer: "letsencrypt-staging"},
	}
	hv := MapToHelmValuesForEnv(app, "staging", domain.AppEnvStaging, "staging.acme.com", "", "", "", org, env, nil, nil)

	got := hv.Components["web"].Ingress
	if got == nil {
		t.Fatal("expected Ingress; got nil")
	}
	if got.ClusterIssuer != "letsencrypt-staging" {
		t.Errorf("env should win: ClusterIssuer = %q, want letsencrypt-staging", got.ClusterIssuer)
	}
}

func TestResolveIngress_UnknownModeYieldsNoIngress(t *testing.T) {
	// Validation is the caller's responsibility — the mapper drops the
	// ingress silently rather than blocking chart render. Documents the
	// contract: bad config → no ingress, never a panic.
	c := webComponent("web")
	c.ExposeMode = domain.ExposeExternal
	app := webApp("hello", c)
	org := domain.RoutingProfiles{
		string(domain.ExposeInternal): {IngressClassName: "nginx-internal"},
	}
	hv := MapToHelmValuesForEnv(app, "staging", domain.AppEnvStaging, "localhost", "", "", "", org, nil, nil, nil)

	if hv.Components["web"].Ingress != nil {
		t.Errorf("unknown mode should yield nil Ingress, got %+v", hv.Components["web"].Ingress)
	}
}

func TestResolveRoutingComponent_PrefersExternalOverInternal(t *testing.T) {
	// admin (alphabetically first, internal) should NOT win against api
	// (alphabetically later, external). Documents the new tier preference.
	admin := domain.ComponentSpec{Name: "admin", Type: domain.ComponentWeb, Enabled: true, ExposeMode: domain.ExposeInternal, PreviewEnabled: true}
	api := domain.ComponentSpec{Name: "api", Type: domain.ComponentWeb, Enabled: true, ExposeMode: domain.ExposeExternal, PreviewEnabled: true}
	got := resolveRoutingComponent([]domain.ComponentSpec{admin, api})
	if got != "api" {
		t.Errorf("routing component = %q, want api (external should beat internal)", got)
	}
}

func TestStripScheme(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://hello.staging.localhost", "hello.staging.localhost"},
		{"https://hello.prod.example.com", "hello.prod.example.com"},
		{"hello.prod.localhost", "hello.prod.localhost"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripScheme(tt.input)
		if got != tt.want {
			t.Errorf("stripScheme(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
