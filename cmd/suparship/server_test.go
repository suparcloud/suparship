package main

import (
	"context"
	"errors"
	"testing"

	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/tpl"
)

func overlayTemplate(name, version string) *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: name, Version: version},
		Spec:       tpl.TemplateSpec{Title: name, Engine: tpl.Engine{Type: tpl.EngineHelm}},
	}
}

// TestResolveTemplate_ClusterFetchErrorFailsLoud locks in F2: a cluster-fetch
// failure must propagate as an error so the publish path aborts rather than
// silently shipping values.yaml without the PE platform/env overlays.
func TestResolveTemplate_ClusterFetchErrorFailsLoud(t *testing.T) {
	a := &gitOpsPublisherAdapter{
		builtin: []*tpl.Template{overlayTemplate("voiceai-agent", "1.0.0")},
		clusterLoader: func(context.Context) ([]*tpl.Template, error) {
			return nil, errors.New("apiserver unavailable")
		},
	}
	got, err := a.resolveTemplate(context.Background(), "voiceai-agent")
	if err == nil {
		t.Fatal("expected an error when the cluster fetch fails, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil template on fetch error, got %+v", got)
	}
}

// TestResolveTemplate_ClusterOverridesBuiltin: on a clean fetch, the cluster
// copy wins over the built-in of the same name.
func TestResolveTemplate_ClusterOverridesBuiltin(t *testing.T) {
	a := &gitOpsPublisherAdapter{
		builtin: []*tpl.Template{overlayTemplate("voiceai-agent", "1.0.0")},
		clusterLoader: func(context.Context) ([]*tpl.Template, error) {
			return []*tpl.Template{overlayTemplate("voiceai-agent", "2.0.0")}, nil
		},
	}
	got, err := a.resolveTemplate(context.Background(), "voiceai-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Metadata.Version != "2.0.0" {
		t.Fatalf("cluster copy should win: got %+v", got)
	}
}

// TestResolveTemplate_NoLoaderUsesBuiltin: nil loader → built-in fallback,
// no error.
func TestResolveTemplate_NoLoaderUsesBuiltin(t *testing.T) {
	a := &gitOpsPublisherAdapter{builtin: []*tpl.Template{overlayTemplate("api", "1.0.0")}}
	got, err := a.resolveTemplate(context.Background(), "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.Metadata.Version != "1.0.0" {
		t.Fatalf("expected built-in fallback, got %+v", got)
	}
	if missing, _ := a.resolveTemplate(context.Background(), "nope"); missing != nil {
		t.Fatalf("expected nil for unknown name, got %+v", missing)
	}
}

// TestSetPlatformOverlays_AppliesDefaultAndEnv: the resolved template's
// DefaultValues + EnvValues[env] land on the publish env.
func TestSetPlatformOverlays_AppliesDefaultAndEnv(t *testing.T) {
	tmpl := overlayTemplate("voiceai-agent", "1.0.0")
	tmpl.Spec.DefaultValues = map[string]any{"replicas": 1}
	tmpl.Spec.EnvValues = map[string]map[string]any{
		"prod": {"replicas": 5},
	}

	pub := gitops.AppPublishEnv{EnvName: "prod"}
	setPlatformOverlays(&pub, tmpl, "prod")

	if v := pub.PlatformDefaultValues["replicas"]; v != 1 {
		t.Fatalf("default overlay not applied: %v", pub.PlatformDefaultValues)
	}
	if v := pub.PlatformEnvValues["replicas"]; v != 5 {
		t.Fatalf("prod env overlay not applied: %v", pub.PlatformEnvValues)
	}
}

// TestSetPlatformOverlays_NilTemplateNoOp: a nil template (app has no matching
// template) leaves the publish env untouched.
func TestSetPlatformOverlays_NilTemplateNoOp(t *testing.T) {
	pub := gitops.AppPublishEnv{EnvName: "prod"}
	setPlatformOverlays(&pub, nil, "prod")
	if pub.PlatformDefaultValues != nil || pub.PlatformEnvValues != nil {
		t.Fatalf("nil template should be a no-op, got %+v / %+v", pub.PlatformDefaultValues, pub.PlatformEnvValues)
	}
}
