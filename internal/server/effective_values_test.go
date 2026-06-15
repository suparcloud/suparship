package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

func valuesTemplate() *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "voiceai-agent", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:  "VoiceAI Agent",
			Engine: tpl.Engine{Type: tpl.EngineHelm},
			DefaultValues: map[string]any{
				"replicaCount": 1,
				"resources":    map[string]any{"requests": map[string]any{"cpu": "100m"}},
			},
			EnvValues: map[string]map[string]any{
				"prod": {"replicaCount": 4},
			},
		},
	}
}

func TestComputeEffectiveValues_LayerPrecedence(t *testing.T) {
	tmpl := valuesTemplate()
	chart := map[string]any{
		"replicaCount": 1,
		"image":        map[string]any{"repository": "ghcr.io/org/app", "tag": "base"},
	}
	appRaw := map[string]any{"image": map[string]any{"tag": "dev"}}
	envRaw := map[string]any{"replicaCount": 9}

	got := computeEffectiveValues(chart, tmpl, "prod", appRaw, envRaw)

	// envRaw (9) > template EnvValues.prod (4) > DefaultValues/chart (1).
	if got["replicaCount"] != 9 {
		t.Errorf("replicaCount = %v, want 9 (env raw wins)", got["replicaCount"])
	}
	// nested: repository survives from chart, tag overridden by appRaw.
	img := got["image"].(map[string]any)
	if img["repository"] != "ghcr.io/org/app" || img["tag"] != "dev" {
		t.Errorf("image merge wrong: %v", img)
	}
	// resources from template DefaultValues present.
	cpu := got["resources"].(map[string]any)["requests"].(map[string]any)["cpu"]
	if cpu != "100m" {
		t.Errorf("cpu = %v, want 100m", cpu)
	}
	// inputs not mutated.
	if tmpl.Spec.DefaultValues["replicaCount"] != 1 || chart["replicaCount"] != 1 {
		t.Error("computeEffectiveValues mutated its inputs")
	}
}

func TestComputeEffectiveValues_EnvValuesOnlyForThatEnv(t *testing.T) {
	tmpl := valuesTemplate()
	// staging has no EnvValues entry → falls back to DefaultValues replicaCount 1.
	got := computeEffectiveValues(nil, tmpl, "staging", nil, nil)
	if got["replicaCount"] != 1 {
		t.Errorf("staging replicaCount = %v, want 1 (default, no env override)", got["replicaCount"])
	}
}

func TestComputeEffectiveValues_NilChartDegrades(t *testing.T) {
	tmpl := valuesTemplate()
	got := computeEffectiveValues(nil, tmpl, "prod", nil, nil)
	if got["replicaCount"] != 4 {
		t.Errorf("replicaCount = %v, want 4 (env default, no chart)", got["replicaCount"])
	}
}

func TestHandleEffectiveValues_OverlayWithoutChartBundle(t *testing.T) {
	// kubeClient nil → no chart bundle; the preview is the platform/env overlay.
	th := &templateHandler{builtin: []*tpl.Template{valuesTemplate()}}

	req := httptest.NewRequest("GET", "/api/v1/templates/voiceai-agent/effective-values?env=prod", nil)
	req.SetPathValue("name", "voiceai-agent")
	rec := httptest.NewRecorder()
	th.handleEffectiveValues(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp EffectiveValuesDTO
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ChartDefaultsAvailable {
		t.Error("chartDefaultsAvailable should be false with no kube client")
	}
	if resp.Values["replicaCount"] != float64(4) { // JSON numbers decode as float64
		t.Errorf("replicaCount = %v, want 4 (prod env value)", resp.Values["replicaCount"])
	}
}

func TestHandleEffectiveValues_UnknownTemplate404(t *testing.T) {
	th := &templateHandler{builtin: []*tpl.Template{valuesTemplate()}}
	req := httptest.NewRequest("GET", "/api/v1/templates/nope/effective-values", nil)
	req.SetPathValue("name", "nope")
	rec := httptest.NewRecorder()
	th.handleEffectiveValues(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleAppValuesPreview_ReflectsUnsavedBody(t *testing.T) {
	store := newMemAppStore()
	store.addApp(&domain.App{
		Name:        "agent",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template:  domain.AppTemplateRef{Name: "voiceai-agent"},
			RawValues: map[string]any{"replicaCount": 2},
		},
	})
	ah := newAppHandler(store, []*tpl.Template{valuesTemplate()}, nil, nil)

	// Unsaved editor state overrides the persisted rawValues for env prod.
	body, _ := json.Marshal(valuesPreviewRequest{
		EnvRawValues: map[string]map[string]any{"prod": {"replicaCount": 7}},
	})
	req := httptest.NewRequest("POST", "/api/v1/projects/demo/apps/agent/envs/prod/values/preview", bytes.NewReader(body))
	req.SetPathValue("project", "demo")
	req.SetPathValue("app", "agent")
	req.SetPathValue("env", "prod")
	rec := httptest.NewRecorder()
	ah.handleAppValuesPreview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp EffectiveValuesDTO
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	// env raw 7 > app raw (ignored — body envRawValues supplied) and > env default 4.
	if resp.Values["replicaCount"] != float64(7) {
		t.Errorf("replicaCount = %v, want 7 (unsaved env override)", resp.Values["replicaCount"])
	}
}

func TestHandleAppValuesPreview_UnknownApp404(t *testing.T) {
	store := newMemAppStore()
	store.apps["demo"] = map[string]*domain.App{}
	ah := newAppHandler(store, []*tpl.Template{valuesTemplate()}, nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/demo/apps/ghost/envs/prod/values/preview", nil)
	req.SetPathValue("project", "demo")
	req.SetPathValue("app", "ghost")
	req.SetPathValue("env", "prod")
	rec := httptest.NewRecorder()
	ah.handleAppValuesPreview(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
