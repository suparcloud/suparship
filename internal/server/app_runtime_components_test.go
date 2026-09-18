package server

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/runtime"
)

func TestImageTagFromRef(t *testing.T) {
	cases := map[string]string{
		"kind-registry:5000/demo/app:main-87702d3": "main-87702d3", // registry port is not a tag
		"registry.example.com/demo/app:v1.2.3":     "v1.2.3",
		"nginx":                                    "",
		"localhost:5000/demo/app":                  "", // port only, no tag
		"demo/app@sha256:abc":                      "abc",
	}
	for ref, want := range cases {
		if got := imageTagFromRef(ref); got != want {
			t.Errorf("imageTagFromRef(%q) = %q, want %q", ref, got, want)
		}
	}
}

// A composed app whose components run different tags (a split Kargo freight)
// must expose each component's image and tag, so the UI can say "mixed"
// instead of showing the first component's tag as the app's release.
func TestApplyComponentRuntimes_ExposesPerComponentTags(t *testing.T) {
	ah := &appHandler{}
	env := &domain.AppEnvironment{}
	instances := []domain.WorkloadInstance{{Component: "frontend", CDBound: true}, {Component: "api", CDBound: true}, {Component: "db"}}
	byComp := map[string]*runtime.RuntimeInfo{
		"frontend": {Status: runtime.StatusHealthy, Replicas: 2, Available: 2, Image: "kind-registry:5000/demo/shipnotes-frontend:main-b8eff88"},
		"api":      {Status: runtime.StatusHealthy, Replicas: 2, Available: 2, Image: "kind-registry:5000/demo/shipnotes-api:main-87702d3"},
		"db":       {Status: runtime.StatusHealthy, Replicas: 1, Available: 1, Image: "postgres:16-alpine"},
	}
	ah.applyComponentRuntimes(env, instances, byComp)

	if len(env.Status.Components) != 3 {
		t.Fatalf("components = %d, want 3", len(env.Status.Components))
	}
	want := map[string]string{"frontend": "main-b8eff88", "api": "main-87702d3", "db": "16-alpine"}
	for _, c := range env.Status.Components {
		if c.Tag != want[c.Component] || c.Image == "" {
			t.Errorf("%s: image=%q tag=%q, want tag %q", c.Component, c.Image, c.Tag, want[c.Component])
		}
	}
	// The env-level release still carries the first component's tag — the UI
	// decides how to present the split from the per-component breakdown.
	if env.Release == nil || env.Release.Tag != "main-b8eff88" {
		t.Errorf("env release = %+v, want first component's tag", env.Release)
	}
}

// The split shows up as a diagnostic that names the cause, and only when
// CD-bound components disagree: an unbound database on its own tag is noise.
func TestSplitReleaseDiagnostic(t *testing.T) {
	bound := []domain.WorkloadInstance{{Component: "frontend", CDBound: true}, {Component: "api", CDBound: true}, {Component: "db"}}
	comp := func(name, tag string) domain.ComponentRuntimeStatus {
		return domain.ComponentRuntimeStatus{Component: name, Tag: tag}
	}

	d := splitReleaseDiagnostic(bound, []domain.ComponentRuntimeStatus{comp("frontend", "main-b8eff88"), comp("api", "main-87702d3"), comp("db", "16-alpine")})
	if d == nil {
		t.Fatal("split CD-bound tags must produce a diagnostic")
	}
	if d.Level != domain.DiagnosticWarning || d.Source != "release" {
		t.Errorf("diagnostic level/source = %s/%s", d.Level, d.Source)
	}
	for _, want := range []string{"frontend: main-b8eff88", "api: main-87702d3"} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("detail missing %q: %q", want, d.Detail)
		}
	}
	if strings.Contains(d.Detail, "db:") {
		t.Errorf("unbound db must not be listed: %q", d.Detail)
	}
	if !strings.Contains(d.Hint, "NewestBuild") {
		t.Errorf("hint should name the Kargo strategy: %q", d.Hint)
	}

	if d := splitReleaseDiagnostic(bound, []domain.ComponentRuntimeStatus{comp("frontend", "main-87702d3"), comp("api", "main-87702d3"), comp("db", "16-alpine")}); d != nil {
		t.Errorf("agreeing CD-bound tags must not warn (db is unbound): %+v", d)
	}
	if d := splitReleaseDiagnostic([]domain.WorkloadInstance{{Component: "web", CDBound: true}}, []domain.ComponentRuntimeStatus{comp("web", "v1")}); d != nil {
		t.Errorf("single component must not warn: %+v", d)
	}
}
