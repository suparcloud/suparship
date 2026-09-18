package server

import (
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
	instances := []domain.WorkloadInstance{{Component: "frontend"}, {Component: "api"}}
	byComp := map[string]*runtime.RuntimeInfo{
		"frontend": {Status: runtime.StatusHealthy, Replicas: 2, Available: 2, Image: "kind-registry:5000/demo/shipnotes-frontend:main-b8eff88"},
		"api":      {Status: runtime.StatusHealthy, Replicas: 2, Available: 2, Image: "kind-registry:5000/demo/shipnotes-api:main-87702d3"},
	}
	ah.applyComponentRuntimes(env, instances, byComp)

	if len(env.Status.Components) != 2 {
		t.Fatalf("components = %d, want 2", len(env.Status.Components))
	}
	want := map[string]string{"frontend": "main-b8eff88", "api": "main-87702d3"}
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
