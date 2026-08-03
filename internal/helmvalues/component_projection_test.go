package helmvalues

import (
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

// TestMapComponentHelmValuesForEnv_Projection verifies the single-component
// projection: values land under the chart's canonical component key (not the
// component's own name), and app.name becomes "{app}-{component}" so the chart's
// fullname names this component's resources distinctly from its siblings.
func TestMapComponentHelmValuesForEnv_Projection(t *testing.T) {
	app := &domain.App{
		Name:        "bigly",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "api", Type: domain.ComponentWeb, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "web-service"}},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "worker"}},
			},
		},
	}

	// Project "api" onto the chart's canonical key "web".
	hv := MapComponentHelmValuesForEnv(app, app.Spec.Components[0], "web",
		"staging", domain.AppEnvStaging, "localhost", "bigly-staging", "", "",
		nil, nil, nil)

	if hv.App.Name != "bigly-api" {
		t.Errorf("app.name = %q, want bigly-api (per-component fullname)", hv.App.Name)
	}
	if len(hv.Components) != 1 {
		t.Fatalf("Components = %d, want exactly 1 (single-component projection)", len(hv.Components))
	}
	if _, ok := hv.Components["web"]; !ok {
		t.Errorf("Components missing canonical key %q; got %v", "web", hv.Components)
	}
	if _, ok := hv.Components["api"]; ok {
		t.Errorf("Components must not carry the source component name %q — it is projected onto the chart key", "api")
	}
	// Platform identity keeps the real app name (tokens must resolve to it).
	if hv.Platform.App != "bigly" {
		t.Errorf("platform.app = %q, want bigly", hv.Platform.App)
	}
}

// TestMapComponentHelmValuesForEnv_Component verifies platform.Component carries the
// component's USER-FACING name (for ((platform.component))), NOT the chart key it is
// projected onto.
func TestMapComponentHelmValuesForEnv_Component(t *testing.T) {
	app := &domain.App{
		Name: "voiceai-lk-sh", ProjectName: "voiceai",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "express-caller", Type: domain.ComponentWorker, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "voiceai-livekit-agent"}},
			},
		},
	}
	// Projected onto the chart's canonical key "agent" — but platform.component must
	// stay "express-caller".
	hv := MapComponentHelmValuesForEnv(app, app.Spec.Components[0], "agent",
		"staging", domain.AppEnvStaging, "localhost", "voiceai", "", "",
		nil, nil, nil)
	if hv.Platform.Component != "express-caller" {
		t.Errorf("platform.component = %q, want express-caller (user name, not chart key)", hv.Platform.Component)
	}
}

// TestMapToHelmValuesForEnv_Component verifies the app-level map sets
// platform.component only for a SINGLE-component app (it IS its component), and
// leaves it empty for a multi-component app (no single component at app level).
func TestMapToHelmValuesForEnv_Component(t *testing.T) {
	single := &domain.App{
		Name: "api", ProjectName: "demo",
		Spec: domain.AppSpec{Components: []domain.ComponentSpec{
			{Name: "api", Type: domain.ComponentWeb, Enabled: true,
				Template: &domain.AppTemplateRef{Name: "web-service"}},
		}},
	}
	if hv := MapToHelmValuesForEnv(single, "staging", domain.AppEnvStaging, "localhost", "demo-staging", "", "", nil, nil, nil); hv.Platform.Component != "api" {
		t.Errorf("single-component platform.component = %q, want api", hv.Platform.Component)
	}
	multi := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{Components: []domain.ComponentSpec{
			{Name: "api", Type: domain.ComponentWeb, Enabled: true, Template: &domain.AppTemplateRef{Name: "web-service"}},
			{Name: "worker", Type: domain.ComponentWorker, Enabled: true, Template: &domain.AppTemplateRef{Name: "worker"}},
		}},
	}
	if hv := MapToHelmValuesForEnv(multi, "staging", domain.AppEnvStaging, "localhost", "demo-staging", "", "", nil, nil, nil); hv.Platform.Component != "" {
		t.Errorf("multi-component app-level platform.component = %q, want empty", hv.Platform.Component)
	}
}

// TestMapComponentHelmValuesForEnv_PerComponentHost verifies each component of a
// composed app gets its OWN {app}-{component} routing host, so multiple exposed
// components don't collide on one hostname.
func TestMapComponentHelmValuesForEnv_PerComponentHost(t *testing.T) {
	app := &domain.App{
		Name:        "bigly",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "api", Type: domain.ComponentWeb, Enabled: true, ExposeMode: domain.ExposeExternal,
					Template: &domain.AppTemplateRef{Name: "web-service"}},
				{Name: "frontend", Type: domain.ComponentWeb, Enabled: true, ExposeMode: domain.ExposeExternal,
					Template: &domain.AppTemplateRef{Name: "web-service"}},
			},
		},
	}
	hosts := map[string]string{}
	for _, c := range app.Spec.Components {
		hv := MapComponentHelmValuesForEnv(app, c, "web",
			"staging", domain.AppEnvStaging, "acme.com", "bigly-staging", "", "",
			nil, nil, nil)
		hosts[c.Name] = hv.Routing.Host
		if hv.Routing.Host != hv.Platform.RoutingHost {
			t.Errorf("%s: routing.host %q != platform.routingHost %q", c.Name, hv.Routing.Host, hv.Platform.RoutingHost)
		}
	}
	if hosts["api"] != "bigly-api.staging.acme.com" {
		t.Errorf("api host = %q, want bigly-api.staging.acme.com", hosts["api"])
	}
	if hosts["frontend"] != "bigly-frontend.staging.acme.com" {
		t.Errorf("frontend host = %q, want bigly-frontend.staging.acme.com", hosts["frontend"])
	}
	if hosts["api"] == hosts["frontend"] {
		t.Error("api and frontend must not share a host")
	}
}

// TestMapComponentHelmValuesForEnv_EmptyKeyFallsBack verifies that an empty
// componentKey falls back to the component's own name (a chart authored to read
// components.<its-own-name> works without a template loader).
func TestMapComponentHelmValuesForEnv_EmptyKeyFallsBack(t *testing.T) {
	app := &domain.App{
		Name:        "bigly",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "worker"}},
			},
		},
	}
	hv := MapComponentHelmValuesForEnv(app, app.Spec.Components[0], "",
		"staging", domain.AppEnvStaging, "localhost", "bigly-staging", "", "",
		nil, nil, nil)

	if _, ok := hv.Components["worker"]; !ok {
		t.Errorf("empty key should fall back to component name %q; got %v", "worker", hv.Components)
	}
	if hv.App.Name != "bigly-worker" {
		t.Errorf("app.name = %q, want bigly-worker", hv.App.Name)
	}
}
