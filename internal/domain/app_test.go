package domain

import (
	"testing"
)

func TestParseAppEnvironmentType(t *testing.T) {
	tests := []struct {
		input   string
		want    AppEnvironmentType
		wantErr bool
	}{
		{input: "staging", want: AppEnvStaging},
		{input: "prod", want: AppEnvProd},
		{input: "preview", want: AppEnvPreview},
		{input: "", wantErr: true},
		{input: "dev", wantErr: true},
		{input: "STAGING", wantErr: true},
		{input: "production", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseAppEnvironmentType(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseAppEnvironmentType(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseAppEnvironmentType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAppEnvironmentTypeValid(t *testing.T) {
	tests := []struct {
		input AppEnvironmentType
		want  bool
	}{
		{AppEnvStaging, true},
		{AppEnvProd, true},
		{AppEnvPreview, true},
		{"", false},
		{"dev", false},
		{"STAGING", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("AppEnvironmentType(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseComponentType(t *testing.T) {
	tests := []struct {
		input   string
		want    ComponentType
		wantErr bool
	}{
		{input: "web", want: ComponentWeb},
		{input: "worker", want: ComponentWorker},
		{input: "cron", want: ComponentCron},
		{input: "job", want: ComponentJob},
		{input: "", wantErr: true},
		{input: "gateway", wantErr: true},
		{input: "WEB", wantErr: true},
		{input: "scheduler", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseComponentType(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseComponentType(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseComponentType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestComponentTypeValid(t *testing.T) {
	tests := []struct {
		input ComponentType
		want  bool
	}{
		{ComponentWeb, true},
		{ComponentWorker, true},
		{ComponentCron, true},
		{ComponentJob, true},
		{"", false},
		{"gateway", false},
		{"WEB", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("ComponentType(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestAppEnvironmentTypeErrorMessage(t *testing.T) {
	_, err := ParseAppEnvironmentType("bogus")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	for _, keyword := range []string{"bogus", "staging", "prod", "preview"} {
		if !contains(msg, keyword) {
			t.Errorf("error message %q missing keyword %q", msg, keyword)
		}
	}
}

func TestComponentTypeErrorMessage(t *testing.T) {
	_, err := ParseComponentType("bogus")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	for _, keyword := range []string{"bogus", "web", "worker", "cron"} {
		if !contains(msg, keyword) {
			t.Errorf("error message %q missing keyword %q", msg, keyword)
		}
	}
}

func TestParseExposeMode(t *testing.T) {
	tests := []struct {
		input   string
		want    ExposeMode
		wantErr bool
	}{
		{"disabled", ExposeDisabled, false},
		{"internal", ExposeInternal, false},
		{"external", ExposeExternal, false},
		{"", ExposeDisabled, false}, // empty maps to disabled
		{"public", "", true},
		{"DISABLED", "", true}, // case-sensitive
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseExposeMode(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseExposeMode(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseExposeMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExposeModeValid(t *testing.T) {
	tests := []struct {
		input ExposeMode
		want  bool
	}{
		{ExposeDisabled, true},
		{ExposeInternal, true},
		{ExposeExternal, true},
		{ExposeMode(""), true}, // empty is valid (treated as disabled)
		{ExposeMode("public"), false},
	}
	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("ExposeMode(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// contains is a small helper so the test file has no extra imports.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

func TestResolveAppClusterTargets(t *testing.T) {
	envRefs := []string{"a", "b", "c"}
	tests := []struct {
		name       string
		sel        []string
		envDefault []string
		envRefs    []string
		want       []string
	}{
		{name: "unset inherits env default (active)", sel: nil, envDefault: []string{"a"}, envRefs: envRefs, want: []string{"a"}},
		{name: "unset inherits env default (all)", sel: nil, envDefault: []string{"a", "b", "c"}, envRefs: envRefs, want: []string{"a", "b", "c"}},
		{name: "star = all env refs", sel: []string{"*"}, envDefault: []string{"a"}, envRefs: envRefs, want: []string{"a", "b", "c"}},
		{name: "star among others still all", sel: []string{"b", "*"}, envDefault: []string{"a"}, envRefs: envRefs, want: []string{"a", "b", "c"}},
		{name: "explicit subset", sel: []string{"c", "a"}, envDefault: []string{"a"}, envRefs: envRefs, want: []string{"c", "a"}},
		{name: "unknown refs dropped", sel: []string{"a", "zzz"}, envDefault: []string{"a"}, envRefs: envRefs, want: []string{"a"}},
		{name: "dups collapsed", sel: []string{"a", "a", "b"}, envDefault: []string{"a"}, envRefs: envRefs, want: []string{"a", "b"}},
		{name: "all unknown yields empty", sel: []string{"zzz"}, envDefault: []string{"a"}, envRefs: envRefs, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveAppClusterTargets(tt.sel, tt.envDefault, tt.envRefs)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestStatefulComponents(t *testing.T) {
	tmpl := &AppTemplateRef{Name: "t"}
	spec := AppSpec{Components: []ComponentSpec{
		{Name: "web", Template: tmpl},
		{Name: "cache", Template: tmpl, Stateful: true},
		{Name: "db", Template: tmpl, Stateful: true},
		{Name: "no-tmpl", Stateful: true}, // no Template → excluded
	}}
	got := spec.StatefulComponents()
	if len(got) != 2 {
		t.Fatalf("StatefulComponents len = %d, want 2: %+v", len(got), got)
	}
	// Name-sorted: cache before db.
	if got[0].Name != "cache" || got[1].Name != "db" {
		t.Errorf("StatefulComponents = %q,%q, want cache,db (name-sorted)", got[0].Name, got[1].Name)
	}
}

// TestWorkloadInstances verifies the app.kubernetes.io/instance label derivation
// that live-status and log queries select on: a single-source app's sole workload
// is labelled {app}; a composed app's components are each labelled {app}-{comp}.
func TestWorkloadInstances(t *testing.T) {
	// Single-source: one component, instance = app name.
	single := &App{Name: "bigly", ProjectName: "demo", Spec: AppSpec{
		Components: []ComponentSpec{
			{Name: "web", Type: ComponentWeb, Enabled: true, Template: &AppTemplateRef{Name: "web"}},
		},
	}}
	got := single.WorkloadInstances()
	if len(got) != 1 || got[0].Instance != "bigly" {
		t.Fatalf("single-source WorkloadInstances = %+v, want [{web bigly}]", got)
	}
	if single.InstanceForComponent("web") != "bigly" {
		t.Errorf("InstanceForComponent(web) = %q, want bigly", single.InstanceForComponent("web"))
	}

	// Composed: each component instance = {app}-{component}; a disabled one is skipped.
	composed := &App{Name: "bigly", ProjectName: "demo", Spec: AppSpec{
		Components: []ComponentSpec{
			{Name: "api", Type: ComponentWeb, Enabled: true, Template: &AppTemplateRef{Name: "web"}},
			{Name: "worker", Type: ComponentType("worker"), Enabled: true, Template: &AppTemplateRef{Name: "worker"}},
			{Name: "off", Type: ComponentWeb, Enabled: false, Template: &AppTemplateRef{Name: "web"}},
		},
	}}
	gc := composed.WorkloadInstances()
	if len(gc) != 2 {
		t.Fatalf("composed WorkloadInstances = %+v, want 2 (disabled skipped)", gc)
	}
	want := map[string]string{"api": "bigly-api", "worker": "bigly-worker"}
	for _, wi := range gc {
		if want[wi.Component] != wi.Instance {
			t.Errorf("component %q instance = %q, want %q", wi.Component, wi.Instance, want[wi.Component])
		}
	}
	if composed.InstanceForComponent("worker") != "bigly-worker" {
		t.Errorf("InstanceForComponent(worker) = %q, want bigly-worker", composed.InstanceForComponent("worker"))
	}
	if composed.InstanceForComponent("nope") != "" {
		t.Errorf("InstanceForComponent(nope) = %q, want empty", composed.InstanceForComponent("nope"))
	}
}

// TestWorkloadInstances_OneShot verifies job/cron components are flagged OneShot
// so live status excludes them (a one-shot has no running pods and must not read
// as "not deployed" nor drag the composed app's aggregate down).
func TestWorkloadInstances_OneShot(t *testing.T) {
	app := &App{Name: "telephony", ProjectName: "voiceai", Spec: AppSpec{
		Components: []ComponentSpec{
			{Name: "backend", Type: ComponentWeb, Enabled: true, Template: &AppTemplateRef{Name: "web"}},
			{Name: "migration", Type: ComponentJob, Enabled: true, Template: &AppTemplateRef{Name: "job"}},
			{Name: "nightly", Type: ComponentCron, Enabled: true, Template: &AppTemplateRef{Name: "cronjob"}},
		},
	}}
	oneShot := map[string]bool{}
	for _, wi := range app.WorkloadInstances() {
		oneShot[wi.Component] = wi.OneShot
	}
	if oneShot["backend"] {
		t.Error("web component must not be OneShot")
	}
	if !oneShot["migration"] {
		t.Error("job component must be OneShot")
	}
	if !oneShot["nightly"] {
		t.Error("cron component must be OneShot")
	}
	if !ComponentJob.OneShot() || !ComponentCron.OneShot() {
		t.Error("ComponentJob/ComponentCron must report OneShot")
	}
	if ComponentWeb.OneShot() || ComponentWorker.OneShot() {
		t.Error("web/worker must not report OneShot")
	}
}

// TestEffectiveComponents verifies the unified-model component surfacing: an app
// with stored components returns them; a legacy single-source app with none gets
// one synthesized primary from its template (so the component-based UI has a row).
func TestEffectiveComponents(t *testing.T) {
	// Stored components are returned as-is.
	withComps := &App{Name: "bigly", Spec: AppSpec{
		Template: AppTemplateRef{Name: "web"},
		Components: []ComponentSpec{
			{Name: "api", Type: ComponentWeb, Enabled: true, Template: &AppTemplateRef{Name: "web"}},
		},
	}}
	if got := withComps.EffectiveComponents(); len(got) != 1 || got[0].Name != "api" {
		t.Errorf("stored components should pass through, got %+v", got)
	}

	// Empty components + a template → one synthesized primary named after the app.
	legacy := &App{Name: "gateway", Spec: AppSpec{Template: AppTemplateRef{Name: "byo-gw"}}}
	got := legacy.EffectiveComponents()
	if len(got) != 1 {
		t.Fatalf("legacy single-source app should synthesize 1 component, got %d", len(got))
	}
	if got[0].Name != "gateway" || got[0].Template == nil || got[0].Template.Name != "byo-gw" || !got[0].Enabled {
		t.Errorf("synthesized primary = %+v, want name=gateway template=byo-gw enabled", got[0])
	}

	// No template and no components → nothing to synthesize.
	if got := (&App{Name: "empty"}).EffectiveComponents(); got != nil {
		t.Errorf("no template + no components → nil, got %+v", got)
	}
}
