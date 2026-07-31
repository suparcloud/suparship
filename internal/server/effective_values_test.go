package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/rbac"
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

// A composed component's effective preview (asComponentOverlay=true) must merge the
// STORED platform override's env values UNDER the component's own overlay — else the
// effective misses env-scoped platform overrides that DO deploy (preview ≠ deploy).
// Without the flag, the body IS the override (the Platform-overrides page), so the
// stored override must not be loaded.
func TestHandlePostEffectiveValues_ComponentOverlayMergesStoredEnvOverride(t *testing.T) {
	tmpl := valuesTemplate()
	tmpl.Metadata.Name = "voiceai-agent"
	client := fake.NewSimpleClientset()
	th := &templateHandler{builtin: []*tpl.Template{tmpl}, kubeClient: client}

	// Seed a stored platform override: staging sets platformStaging.
	putBody, _ := json.Marshal(TemplateOverrideDTO{
		EnvValues: map[string]map[string]any{"staging": {"platformStaging": "yes"}},
	})
	put := httptest.NewRequest("PUT", "/api/v1/templates/voiceai-agent/overrides", bytes.NewReader(putBody))
	put.SetPathValue("name", "voiceai-agent")
	th.handlePutTemplateOverride(httptest.NewRecorder(), put)

	body, _ := json.Marshal(TemplateOverrideDTO{
		DefaultValues: map[string]any{"componentBase": "b"},
		EnvValues:     map[string]map[string]any{"staging": {"componentStaging": "c"}},
	})

	decode := func(url string) EffectiveValuesDTO {
		req := httptest.NewRequest("POST", url, bytes.NewReader(body))
		req.SetPathValue("name", "voiceai-agent")
		rec := httptest.NewRecorder()
		th.handlePostEffectiveValues(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var resp EffectiveValuesDTO
		_ = json.NewDecoder(rec.Body).Decode(&resp)
		return resp
	}

	// asComponentOverlay=true → stored platform env override merges, under the
	// component's own base + staging overlays.
	comp := decode("/api/v1/templates/voiceai-agent/effective-values?env=staging&asComponentOverlay=true")
	if comp.Values["platformStaging"] != "yes" {
		t.Errorf("platformStaging = %v, want yes (stored platform env override must merge)", comp.Values["platformStaging"])
	}
	if comp.Values["componentBase"] != "b" || comp.Values["componentStaging"] != "c" {
		t.Errorf("component overlays lost: base=%v staging=%v", comp.Values["componentBase"], comp.Values["componentStaging"])
	}

	// No flag → body is the override itself; stored override NOT loaded.
	page := decode("/api/v1/templates/voiceai-agent/effective-values?env=staging")
	if _, present := page.Values["platformStaging"]; present {
		t.Errorf("platformStaging must NOT appear without asComponentOverlay: %v", page.Values)
	}
	if page.Values["componentStaging"] != "c" {
		t.Errorf("body-as-override staging value lost: %v", page.Values["componentStaging"])
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

// TestHandlePostEffectiveValues_PreviewLayersTemplateDefaults verifies the
// preview scope: with ?preview=true, the template's PreviewDefaultValues layer in
// and the app's preview override (envValues.preview) sits on top — so the
// effective pane shows what a composed preview actually deploys.
func TestHandlePostEffectiveValues_PreviewLayersTemplateDefaults(t *testing.T) {
	tmpl := valuesTemplate()
	tmpl.Spec.PreviewDefaultValues = map[string]any{
		"previewOnly":  "yes",
		"replicaCount": 1,
	}
	th := &templateHandler{builtin: []*tpl.Template{tmpl}}

	body, _ := json.Marshal(TemplateOverrideDTO{
		DefaultValues: map[string]any{"base": "b"}, // component base overlay
		EnvValues:     map[string]map[string]any{"preview": {"replicaCount": 9}},
	})
	req := httptest.NewRequest("POST", "/api/v1/templates/voiceai-agent/effective-values?env=staging&preview=true", bytes.NewReader(body))
	req.SetPathValue("name", "voiceai-agent")
	rec := httptest.NewRecorder()
	th.handlePostEffectiveValues(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp EffectiveValuesDTO
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	// Template preview default appears.
	if resp.Values["previewOnly"] != "yes" {
		t.Errorf("previewOnly = %v, want yes (template preview default not applied)", resp.Values["previewOnly"])
	}
	// The component base overlay is present.
	if resp.Values["base"] != "b" {
		t.Errorf("base = %v, want b (component base overlay)", resp.Values["base"])
	}
	// The preview override wins over the template preview default.
	if resp.Values["replicaCount"] != float64(9) {
		t.Errorf("replicaCount = %v, want 9 (preview override on top)", resp.Values["replicaCount"])
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

func TestHandleAppValuesPreview_PassthroughOmitsCanonicalBase(t *testing.T) {
	// A BYO/passthrough template (injectCanonicalValues:false) → the preview must
	// NOT include the canonical app/platform/suparship block, only chart + overlays.
	store := newMemAppStore()
	store.addApp(&domain.App{
		Name:        "byo",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "voiceai-agent"}},
	})
	passthrough := false
	tmpl := valuesTemplate()
	tmpl.Metadata.Name = "voiceai-agent"
	tmpl.Spec.InjectCanonicalValues = &passthrough
	ah := newAppHandler(store, []*tpl.Template{tmpl}, nil, nil)

	req := httptest.NewRequest("POST", "/api/v1/projects/demo/apps/byo/envs/prod/values/preview", nil)
	req.SetPathValue("project", "demo")
	req.SetPathValue("app", "byo")
	req.SetPathValue("env", "prod")
	rec := httptest.NewRecorder()
	ah.handleAppValuesPreview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp EffectiveValuesDTO
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	for _, k := range []string{"app", "platform", "suparship", "routing"} {
		if _, present := resp.Values[k]; present {
			t.Errorf("passthrough preview must omit canonical key %q: %v", k, resp.Values)
		}
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

// Fix 1: the app values-preview resolves the env's workload cluster and layers
// the org override's ClusterValues[cluster]. Previously the cluster was hard-
// coded "", so cluster-scoped template overrides never appeared in the preview
// even though they deploy.
func TestHandleAppValuesPreview_MergesClusterOverride(t *testing.T) {
	store := newMemAppStore()
	store.addApp(&domain.App{
		Name:        "agent",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "voiceai-agent"}},
	})
	store.addEnv(&domain.AppEnvironment{
		AppName: "agent", ProjectName: "demo", EnvName: "prod",
		EnvType: domain.AppEnvProd, Namespace: "demo-agent-prod",
	})

	passthrough := false
	tmpl := valuesTemplate()
	tmpl.Metadata.Name = "voiceai-agent"
	tmpl.Spec.InjectCanonicalValues = &passthrough // effective = chart+overlays only

	client := fake.NewSimpleClientset()
	if err := kube.SaveTemplateOverride(context.Background(), client, "voiceai-agent", &domain.TemplateOverride{
		ClusterValues: map[string]map[string]any{"c1": {"clusterKey": "fromcluster"}},
	}); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	ah := &appHandler{
		appStore:    store,
		builtin:     []*tpl.Template{tmpl},
		kubeClient:  client,
		orgProvider: &staticOrgProvider{org: &rbac.Org{Environments: []rbac.OrgEnvironment{
			{Name: "prod", ClusterRefs: []string{"c1"}, ActiveClusterRef: "c1"},
		}}},
		statusCache: newStatusCache(statusCacheTTL),
	}

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
	if resp.Values["clusterKey"] != "fromcluster" {
		t.Errorf("clusterKey = %v, want fromcluster (cluster-scoped override must merge)", resp.Values["clusterKey"])
	}
}

// Fix 2: with ?preview=true the app values-preview layers the template's
// PreviewDefaultValues and the app's own preview override on top of the base
// env — so single-component apps show the same effective preview as composed
// ones. Without the flag the preview band must NOT apply.
func TestHandleAppValuesPreview_PreviewScopeLayersDefaults(t *testing.T) {
	store := newMemAppStore()
	store.addApp(&domain.App{
		Name:        "agent",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "voiceai-agent"},
			EnvironmentDefaults: map[string]domain.EnvironmentOverride{
				domain.PreviewOverrideKey: {RawValues: map[string]any{"replicaCount": 9}},
			},
		},
	})
	store.addEnv(&domain.AppEnvironment{
		AppName: "agent", ProjectName: "demo", EnvName: "staging",
		EnvType: domain.AppEnvStaging, Namespace: "demo-agent-staging",
	})

	passthrough := false
	tmpl := valuesTemplate()
	tmpl.Metadata.Name = "voiceai-agent"
	tmpl.Spec.InjectCanonicalValues = &passthrough
	tmpl.Spec.PreviewDefaultValues = map[string]any{"previewOnly": "yes", "replicaCount": 1}

	ah := &appHandler{appStore: store, builtin: []*tpl.Template{tmpl}, statusCache: newStatusCache(statusCacheTTL)}

	decode := func(url string) EffectiveValuesDTO {
		req := httptest.NewRequest("POST", url, nil)
		req.SetPathValue("project", "demo")
		req.SetPathValue("app", "agent")
		req.SetPathValue("env", "staging")
		rec := httptest.NewRecorder()
		ah.handleAppValuesPreview(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var resp EffectiveValuesDTO
		_ = json.NewDecoder(rec.Body).Decode(&resp)
		return resp
	}

	// preview=true → template preview default + app preview override apply.
	prev := decode("/api/v1/projects/demo/apps/agent/envs/staging/values/preview?preview=true")
	if prev.Values["previewOnly"] != "yes" {
		t.Errorf("previewOnly = %v, want yes (template preview default must merge)", prev.Values["previewOnly"])
	}
	if prev.Values["replicaCount"] != float64(9) {
		t.Errorf("replicaCount = %v, want 9 (app preview override on top)", prev.Values["replicaCount"])
	}

	// no flag → the preview band must not apply.
	base := decode("/api/v1/projects/demo/apps/agent/envs/staging/values/preview")
	if _, present := base.Values["previewOnly"]; present {
		t.Errorf("previewOnly must NOT appear outside preview scope: %v", base.Values)
	}
}
