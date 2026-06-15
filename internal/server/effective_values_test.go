package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

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

	got := computeEffectiveValues(chart, nil, tmpl, nil, "prod", "", appRaw, envRaw)

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
	got := computeEffectiveValues(nil, nil, tmpl, nil, "staging", "", nil, nil)
	if got["replicaCount"] != 1 {
		t.Errorf("staging replicaCount = %v, want 1 (default, no env override)", got["replicaCount"])
	}
}

func TestComputeEffectiveValues_NilChartDegrades(t *testing.T) {
	tmpl := valuesTemplate()
	got := computeEffectiveValues(nil, nil, tmpl, nil, "prod", "", nil, nil)
	if got["replicaCount"] != 4 {
		t.Errorf("replicaCount = %v, want 4 (env default, no chart)", got["replicaCount"])
	}
}

func TestComputeEffectiveValues_OrgLayerBetweenTemplateAndApp(t *testing.T) {
	tmpl := valuesTemplate() // DefaultValues replicaCount 1, EnvValues.prod replicaCount 4
	ov := &domain.TemplateOverride{
		DefaultValues: map[string]any{"replicaCount": 2}, // org > template default
		EnvValues:     map[string]map[string]any{"prod": {"replicaCount": 6}},
	}

	// No app override → org prod (6) wins over template prod (4).
	if got := computeEffectiveValues(nil, nil, tmpl, ov, "prod", "", nil, nil); got["replicaCount"] != 6 {
		t.Errorf("replicaCount = %v, want 6 (org prod over template prod)", got["replicaCount"])
	}
	// App override still wins over the org layer.
	if got := computeEffectiveValues(nil, nil, tmpl, ov, "prod", "", map[string]any{"replicaCount": 11}, nil); got["replicaCount"] != 11 {
		t.Errorf("replicaCount = %v, want 11 (app over org)", got["replicaCount"])
	}
	// Org default applies to an env with no org EnvValues entry.
	if got := computeEffectiveValues(nil, nil, tmpl, ov, "staging", "", nil, nil); got["replicaCount"] != 2 {
		t.Errorf("staging replicaCount = %v, want 2 (org default)", got["replicaCount"])
	}
}

func TestComputeEffectiveValues_ClusterLayer(t *testing.T) {
	tmpl := valuesTemplate()
	ov := &domain.TemplateOverride{
		EnvValues: map[string]map[string]any{"prod": {"replicaCount": 4}},
		ClusterValues: map[string]map[string]any{
			"eks-uswest": {"ingress": map[string]any{"annotations": map[string]any{"aws": "nlb"}}},
			"aks-eastus": {"ingress": map[string]any{"annotations": map[string]any{"azure": "internal"}}},
		},
	}

	// Cluster block applies for that cluster; env layer also present.
	got := computeEffectiveValues(nil, nil, tmpl, ov, "prod", "eks-uswest", nil, nil)
	ann := got["ingress"].(map[string]any)["annotations"].(map[string]any)
	if ann["aws"] != "nlb" {
		t.Errorf("aws cluster annotation missing: %v", ann)
	}
	if _, leaked := ann["azure"]; leaked {
		t.Errorf("other cluster's block leaked: %v", ann)
	}
	if got["replicaCount"] != 4 {
		t.Errorf("env layer lost: replicaCount = %v, want 4", got["replicaCount"])
	}

	// No cluster → no cluster block.
	none := computeEffectiveValues(nil, nil, tmpl, ov, "prod", "", nil, nil)
	if _, ok := none["ingress"]; ok {
		t.Errorf("cluster block applied without a cluster: %v", none["ingress"])
	}
}

func TestHandlePostEffectiveValues_ReflectsUnsavedClusterBlock(t *testing.T) {
	th := &templateHandler{builtin: []*tpl.Template{valuesTemplate()}}
	body, _ := json.Marshal(TemplateOverrideDTO{
		ClusterValues: map[string]map[string]any{"eks-uswest": {"foo": "bar"}},
	})
	req := httptest.NewRequest("POST", "/api/v1/templates/voiceai-agent/effective-values?env=prod&cluster=eks-uswest", bytes.NewReader(body))
	req.SetPathValue("name", "voiceai-agent")
	rec := httptest.NewRecorder()
	th.handlePostEffectiveValues(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp EffectiveValuesDTO
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Values["foo"] != "bar" {
		t.Errorf("foo = %v, want bar (unsaved cluster block)", resp.Values["foo"])
	}
}

func TestHandleTemplateOverride_PutGetRoundTrip(t *testing.T) {
	client := fake.NewSimpleClientset()
	th := &templateHandler{builtin: []*tpl.Template{valuesTemplate()}, kubeClient: client}

	body, _ := json.Marshal(TemplateOverrideDTO{
		DefaultValues: map[string]any{"replicaCount": 3},
	})
	put := httptest.NewRequest("PUT", "/api/v1/templates/voiceai-agent/overrides", bytes.NewReader(body))
	put.SetPathValue("name", "voiceai-agent")
	pr := httptest.NewRecorder()
	th.handlePutTemplateOverride(pr, put)
	if pr.Code != http.StatusOK {
		t.Fatalf("PUT: expected 200, got %d: %s", pr.Code, pr.Body.String())
	}

	get := httptest.NewRequest("GET", "/api/v1/templates/voiceai-agent/overrides", nil)
	get.SetPathValue("name", "voiceai-agent")
	gr := httptest.NewRecorder()
	th.handleGetTemplateOverride(gr, get)
	var dto TemplateOverrideDTO
	_ = json.NewDecoder(gr.Body).Decode(&dto)
	if dto.DefaultValues["replicaCount"] != float64(3) {
		t.Fatalf("round-trip replicaCount = %v, want 3", dto.DefaultValues["replicaCount"])
	}
}

func TestHandlePostEffectiveValues_ReflectsUnsavedOverride(t *testing.T) {
	th := &templateHandler{builtin: []*tpl.Template{valuesTemplate()}}

	body, _ := json.Marshal(TemplateOverrideDTO{
		EnvValues: map[string]map[string]any{"prod": {"replicaCount": 8}},
	})
	req := httptest.NewRequest("POST", "/api/v1/templates/voiceai-agent/effective-values?env=prod", bytes.NewReader(body))
	req.SetPathValue("name", "voiceai-agent")
	rec := httptest.NewRecorder()
	th.handlePostEffectiveValues(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp EffectiveValuesDTO
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Values["replicaCount"] != float64(8) {
		t.Errorf("replicaCount = %v, want 8 (unsaved org override)", resp.Values["replicaCount"])
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

func TestHandleAppValuesPreview_IncludesCanonicalBase(t *testing.T) {
	// The app preview must include the canonical platform↔chart base (app/
	// components/suparship) so it matches what Helm deploys — not just chart
	// defaults + overlays.
	store := newMemAppStore()
	store.addApp(&domain.App{
		Name:        "agent",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "voiceai-agent"}},
	})
	ah := newAppHandler(store, []*tpl.Template{valuesTemplate()}, nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/demo/apps/agent/envs/prod/values/preview", nil)
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
	// Canonical keys the chart consumes must be present.
	for _, k := range []string{"app", "components", "suparship"} {
		if _, ok := resp.Values[k]; !ok {
			t.Errorf("effective values missing canonical key %q", k)
		}
	}
	app, _ := resp.Values["app"].(map[string]any)
	if app["name"] != "agent" {
		t.Errorf("app.name = %v, want agent", app["name"])
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
