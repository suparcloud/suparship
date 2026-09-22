package gitops_test

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

// readIngressHost returns ingress.host from a rendered values.yaml.
func readIngressHost(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	ing, _ := m["ingress"].(map[string]any)
	host, _ := ing["host"].(string)
	return host
}

// routedHostApp wires ((platform.routingHost)) into ingress.host — the chart
// contract — with staging's hostname routed to the pr-42 preview.
func routedHostApp() *domain.App {
	return &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "voiceai-livekit-agent"},
			RawValues: map[string]any{
				"ingress": map[string]any{"host": "((platform.routingHost))"},
			},
			EnvironmentDefaults: map[string]domain.EnvironmentOverride{
				"staging": {RoutedToPreview: "pr-42"},
			},
		},
	}
}

// TestPublish_RoutedHostSwapReachesValues proves the host swap end to end
// through the publisher: the routed preview renders on the env's stable host,
// the env on its "-origin" alternate, and an unrelated preview and prod keep
// their normal hosts.
func TestPublish_RoutedHostSwapReachesValues(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)
	app := routedHostApp()

	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "acme.com"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "acme.com"},
	}
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("publish app: %v", err)
	}
	if got := readIngressHost(t, valuesPath(dir, "staging")); got != "hello-origin.staging.acme.com" {
		t.Errorf("routed-away staging ingress.host = %q, want hello-origin.staging.acme.com", got)
	}
	if got := readIngressHost(t, valuesPath(dir, "prod")); got != "hello.prod.acme.com" {
		t.Errorf("prod ingress.host = %q, want hello.prod.acme.com (never swapped)", got)
	}

	for _, pv := range []struct{ name, want string }{
		{"pr-42", "hello.staging.acme.com"},     // routed: takes staging's host
		{"pr-7", "pr-7.hello.preview.acme.com"}, // sibling preview: its own host
	} {
		spec := gitops.PreviewPublishSpec{
			PreviewName:   pv.name,
			BaseEnv:       "staging",
			ClusterServer: "https://kubernetes.default.svc",
			Namespace:     "demo-hello-preview-" + pv.name,
			BaseDomain:    "acme.com",
			ImageTag:      pv.name + "-abc",
		}
		if err := p.PublishPreviewForTest(dir, app, spec); err != nil {
			t.Fatalf("publish preview %s: %v", pv.name, err)
		}
		path := filepath.Join(dir, "previews", "staging", "demo", pv.name, "hello", "values.yaml")
		if got := readIngressHost(t, path); got != pv.want {
			t.Errorf("preview %s ingress.host = %q, want %q", pv.name, got, pv.want)
		}
	}

	// Restore: clearing the field puts both back on their own hosts.
	app.Spec.EnvironmentDefaults["staging"] = domain.EnvironmentOverride{}
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("republish app: %v", err)
	}
	if got := readIngressHost(t, valuesPath(dir, "staging")); got != "hello.staging.acme.com" {
		t.Errorf("restored staging ingress.host = %q, want hello.staging.acme.com", got)
	}
	spec := gitops.PreviewPublishSpec{
		PreviewName: "pr-42", BaseEnv: "staging", ClusterServer: "https://kubernetes.default.svc",
		Namespace: "demo-hello-preview-pr-42", BaseDomain: "acme.com", ImageTag: "pr-42-abc",
	}
	if err := p.PublishPreviewForTest(dir, app, spec); err != nil {
		t.Fatalf("republish preview: %v", err)
	}
	path := filepath.Join(dir, "previews", "staging", "demo", "pr-42", "hello", "values.yaml")
	if got := readIngressHost(t, path); got != "pr-42.hello.preview.acme.com" {
		t.Errorf("restored preview ingress.host = %q, want pr-42.hello.preview.acme.com", got)
	}
}
