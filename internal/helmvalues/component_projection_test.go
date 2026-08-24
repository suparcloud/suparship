package helmvalues

import (
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

// TestMapComponentPlatformValuesForEnv_Component verifies platform.Component
// carries the component's user-facing name (for ((platform.component))) while
// platform.app keeps the real app name (tokens must resolve to it).
func TestMapComponentPlatformValuesForEnv_Component(t *testing.T) {
	app := &domain.App{
		Name: "voiceai-lk-sh", ProjectName: "voiceai",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "express-caller", Type: domain.ComponentWorker, Enabled: true,
					Template: &domain.AppTemplateRef{Name: "voiceai-livekit-agent"}},
			},
		},
	}
	p := MapComponentPlatformValuesForEnv(app, app.Spec.Components[0],
		"staging", domain.AppEnvStaging, "localhost", "voiceai", "", "",
		nil, nil, nil)
	if p.Component != "express-caller" {
		t.Errorf("platform.component = %q, want express-caller", p.Component)
	}
	if p.App != "voiceai-lk-sh" {
		t.Errorf("platform.app = %q, want voiceai-lk-sh", p.App)
	}
}

// TestMapPlatformValuesForEnv_Component verifies the app-level map sets
// platform.component only for a SINGLE-component app (it IS its component), and
// leaves it empty for a multi-component app (no single component at app level).
func TestMapPlatformValuesForEnv_Component(t *testing.T) {
	single := &domain.App{
		Name: "api", ProjectName: "demo",
		Spec: domain.AppSpec{Components: []domain.ComponentSpec{
			{Name: "api", Type: domain.ComponentWeb, Enabled: true,
				Template: &domain.AppTemplateRef{Name: "web-service"}},
		}},
	}
	if p := MapPlatformValuesForEnv(single, "staging", domain.AppEnvStaging, "localhost", "demo-staging", "", "", nil, nil, nil); p.Component != "api" {
		t.Errorf("single-component platform.component = %q, want api", p.Component)
	}
	multi := &domain.App{
		Name: "bigly", ProjectName: "demo",
		Spec: domain.AppSpec{Components: []domain.ComponentSpec{
			{Name: "api", Type: domain.ComponentWeb, Enabled: true, Template: &domain.AppTemplateRef{Name: "web-service"}},
			{Name: "worker", Type: domain.ComponentWorker, Enabled: true, Template: &domain.AppTemplateRef{Name: "worker"}},
		}},
	}
	if p := MapPlatformValuesForEnv(multi, "staging", domain.AppEnvStaging, "localhost", "demo-staging", "", "", nil, nil, nil); p.Component != "" {
		t.Errorf("multi-component app-level platform.component = %q, want empty", p.Component)
	}
}

// TestMapComponentPlatformValuesForEnv_PerComponentHost verifies each component
// of a composed app gets its OWN {app}-{component} routing host, so multiple
// exposed components don't collide on one hostname.
func TestMapComponentPlatformValuesForEnv_PerComponentHost(t *testing.T) {
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
		p := MapComponentPlatformValuesForEnv(app, c,
			"staging", domain.AppEnvStaging, "acme.com", "bigly-staging", "", "",
			nil, nil, nil)
		hosts[c.Name] = p.RoutingHost
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
