package server

import (
	"context"
	"encoding/json"
	"net/http"

	"gopkg.in/yaml.v3"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/helmvalues"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/chartimport"
)

// EffectiveValuesDTO is the read-only "what will deploy" preview that backs the
// values-editor UI. It is the values-document layering (chart defaults ⊕ template
// platform/env defaults ⊕ developer rawValues), NOT the fully rendered chart:
// the canonical struct-mapping and ((platform.*))/((vars.*)) interpolation are applied
// later at publish with per-env cluster/domain context that isn't available here.
type EffectiveValuesDTO struct {
	// Values is the merged values document.
	Values map[string]any `json:"values"`
	// ChartDefaultsAvailable is false when the chart bundle couldn't be read
	// (built-in/disk/external-mode templates) — the preview then reflects only
	// the platform/env defaults + overrides, not the chart's own defaults.
	ChartDefaultsAvailable bool `json:"chartDefaultsAvailable"`
	// Interpolated reports whether ((platform.*))/((vars.*)) tokens were resolved.
	// Always false in v1 — tokens are shown literally because the platform
	// context is only fully known at publish time.
	Interpolated bool `json:"interpolated"`
	// Layers names the overlays that contributed, low→high, for UI hints.
	Layers []string `json:"layers"`
	// DiscoveredImages are the container images found in the effective values
	// (every image block with a repository), each with its dotted tag key. The CD
	// UI lists these so the user can select which images Kargo manages — excluding
	// sidecars/init/proxy images they don't want promoted. Repository is shown for
	// context; at publish it is re-read from the values, not from here.
	DiscoveredImages []TemplateImageDTO `json:"discoveredImages,omitempty"`
}

// chartDefaults reads the template's chart bundle (.tgz stored as a cluster
// ConfigMap) and returns its decoded values.yaml. Returns (nil, false) — never an
// error — when there is no cluster client or no readable bundle (disk/built-in/
// external-mode templates), so the preview degrades to the overlay layers.
func chartDefaults(ctx context.Context, kc kubernetes.Interface, t *tpl.Template) (map[string]any, bool) {
	if kc == nil || t == nil {
		return nil, false
	}
	data, err := kube.LoadChartBundleVersion(ctx, kc, t.Metadata.Name, t.Metadata.Version)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	arc, err := chartimport.ParseArchive(data)
	if err != nil || arc == nil || len(arc.Values) == 0 {
		return nil, false
	}
	return arc.Values, true
}

// computeEffectiveValues layers the values document low→high, exactly mirroring
// the publisher's envOverlay/rawValuesOverlay order:
//
//	chart defaults
//	  ⊕ template DefaultValues ⊕ template EnvValues[env]       (chart author / repo)
//	  ⊕ org DefaultValues ⊕ org EnvValues[env] ⊕ org ClusterValues[cluster]  (PE/SRE)
//	  ⊕ appRaw ⊕ envRaw                                        (developer)
//
// Inputs are deep-copied so callers' maps (the stored app spec, the template,
// the org override) are never mutated.
func computeEffectiveValues(chartVals, canonicalBase map[string]any, t *tpl.Template, ov *domain.TemplateOverride, envName, cluster string, appRaw, envRaw map[string]any) map[string]any {
	out := helmvalues.DeepCopyMap(chartVals)
	if out == nil {
		out = map[string]any{}
	}
	// canonicalBase is the platform↔chart contract (app/components/suparship/
	// routing) suparship publishes; Helm applies it on top of the chart's own
	// values.yaml defaults at render. Layer it here so the app preview matches
	// what deploys. nil for template-level previews (no concrete app).
	if canonicalBase != nil {
		out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(canonicalBase))
	}
	if t != nil {
		out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(t.Spec.DefaultValues))
		if envName != "" && t.Spec.EnvValues != nil {
			out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(t.Spec.EnvValues[envName]))
		}
	}
	if ov != nil {
		out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(ov.DefaultValues))
		if envName != "" && ov.EnvValues != nil {
			out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(ov.EnvValues[envName]))
		}
		if cluster != "" && ov.ClusterValues != nil {
			out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(ov.ClusterValues[cluster]))
		}
	}
	out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(appRaw))
	out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(envRaw))
	return out
}

// previewLayer, when non-nil, layers the preview band on top of the base-env
// composition — mirroring the publisher's previewRawValuesOverlay order: the
// template+org PreviewDefaultValues (templateDefaults) then the app's own
// preview override (appOverride). It is what makes the effective-preview pane
// show what a preview actually deploys (not just its base env), identically for
// composed and single-component apps.
type previewLayer struct {
	templateDefaults map[string]any
	appOverride      map[string]any
}

// mergedPreviewDefaults returns the template's PreviewDefaultValues (chart
// author) merged with the org override's PreviewDefaultValues (PE/SRE), the
// combined "preview defaults" band. Either may be empty.
func mergedPreviewDefaults(t *tpl.Template, ov *domain.TemplateOverride) map[string]any {
	out := map[string]any{}
	if t != nil {
		out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(t.Spec.PreviewDefaultValues))
	}
	if ov != nil {
		out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(ov.PreviewDefaultValues))
	}
	return out
}

func effectiveValuesDTO(chartVals, canonicalBase map[string]any, available bool, t *tpl.Template, ov *domain.TemplateOverride, envName, cluster string, appRaw, envRaw map[string]any, pv *previewLayer) EffectiveValuesDTO {
	layers := []string{}
	if available {
		layers = append(layers, "chart defaults")
	}
	if len(canonicalBase) > 0 {
		layers = append(layers, "platform base")
	}
	if t != nil && len(t.Spec.DefaultValues) > 0 {
		layers = append(layers, "template defaults")
	}
	if t != nil && envName != "" && len(t.Spec.EnvValues[envName]) > 0 {
		layers = append(layers, "template "+envName)
	}
	if ov != nil && len(ov.DefaultValues) > 0 {
		layers = append(layers, "org overrides")
	}
	if ov != nil && envName != "" && len(ov.EnvValues[envName]) > 0 {
		layers = append(layers, "org "+envName)
	}
	if ov != nil && cluster != "" && len(ov.ClusterValues[cluster]) > 0 {
		layers = append(layers, "org cluster "+cluster)
	}
	if len(appRaw) > 0 {
		layers = append(layers, "app overrides")
	}
	if len(envRaw) > 0 {
		layers = append(layers, envName+" overrides")
	}
	if pv != nil && len(pv.templateDefaults) > 0 {
		layers = append(layers, "preview defaults")
	}
	if pv != nil && len(pv.appOverride) > 0 {
		layers = append(layers, "preview overrides")
	}
	values := computeEffectiveValues(chartVals, canonicalBase, t, ov, envName, cluster, appRaw, envRaw)
	// Preview band sits above the base-env composition (see previewLayer).
	if pv != nil {
		values = helmvalues.DeepMerge(values, helmvalues.DeepCopyMap(pv.templateDefaults))
		values = helmvalues.DeepMerge(values, helmvalues.DeepCopyMap(pv.appOverride))
	}
	return EffectiveValuesDTO{
		Values:                 values,
		ChartDefaultsAvailable: available,
		Interpolated:           false,
		Layers:                 layers,
		DiscoveredImages:       imagesToDTO(chartimport.DetectImageMappings(values)),
	}
}

// handleEffectiveValues serves GET /api/v1/templates/{name}/effective-values?env={env}.
// It returns the starting values document for a NOT-yet-created app: chart defaults
// ⊕ the template's platform/env defaults. The create form seeds its read-only
// effective preview from this.
func (th *templateHandler) handleEffectiveValues(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "template name required"})
		return
	}
	_, byName := th.resolve(r.Context())
	t, ok := byName[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "template not found"})
		return
	}
	env := r.URL.Query().Get("env")
	cluster := r.URL.Query().Get("cluster")
	chartVals, available := chartDefaults(r.Context(), th.kubeClient, t)
	ov := loadOverride(r.Context(), th.kubeClient, name)
	// Template-level preview: no concrete app, so no canonical base.
	writeJSON(w, http.StatusOK, effectiveValuesDTO(chartVals, nil, available, t, ov, env, cluster, nil, nil, nil))
}

// loadOverride best-effort reads a template's org-level platform override.
// Returns nil (no override) on nil client or any error — the override is
// additive/optional and must never fail an effective-values computation.
func loadOverride(ctx context.Context, kc kubernetes.Interface, name string) *domain.TemplateOverride {
	if kc == nil {
		return nil
	}
	ov, err := kube.LoadTemplateOverride(ctx, kc, name)
	if err != nil {
		return nil
	}
	return ov
}

// valuesPreviewRequest is the body of the app values-preview endpoint: the live,
// possibly-unsaved editor state. Both fields are optional; when omitted the
// handler falls back to the app's persisted overlays.
type valuesPreviewRequest struct {
	RawValues    map[string]any            `json:"rawValues,omitempty"`
	EnvRawValues map[string]map[string]any `json:"envRawValues,omitempty"`
}

// handleAppValuesPreview serves
// POST /api/v1/projects/{project}/apps/{app}/envs/{env}/values/preview.
// It computes the effective values for an existing app and env, layering the
// request's (unsaved) overlays so the editor can show a live preview as the user
// types. Computes only — never mutates.
func (ah *appHandler) handleAppValuesPreview(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	app, err := ah.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	var req valuesPreviewRequest
	if r.Body != nil {
		// An empty/absent body is valid — fall back to the persisted overlays.
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	appRaw := req.RawValues
	if appRaw == nil {
		appRaw = app.Spec.RawValues
	}
	var envRaw map[string]any
	if req.EnvRawValues != nil {
		envRaw = req.EnvRawValues[envName]
	} else if ov, ok := app.Spec.EnvironmentDefaults[envName]; ok {
		envRaw = ov.RawValues
	}

	t, _ := ah.lookupTemplate(r.Context(), app.Spec.Template.Name)
	// skipChart drops the chart defaults AND the canonical struct base so the
	// response is only the CONCISE platform base (template ⊕ org overrides) for the
	// env — the seed the single-component editor pre-fills with, without the chart's
	// hundreds of default keys or the structural app/components/suparship scaffolding.
	skipChart := r.URL.Query().Get("skipChart") == "true"
	var (
		chartVals map[string]any
		available bool
	)
	if !skipChart {
		chartVals, available = chartDefaults(r.Context(), ah.kubeClient, t)
	}
	ov := loadOverride(r.Context(), ah.kubeClient, app.Spec.Template.Name)
	// Include the canonical platform↔chart base (app/components/suparship/routing)
	// so the preview matches what Helm renders — but only for canonical templates.
	// BYO/passthrough templates emit only their own values + overrides + tokens.
	var canonicalBase map[string]any
	if !skipChart && (t == nil || t.Spec.CanonicalValues()) {
		canonicalBase = ah.canonicalBaseMap(r.Context(), app, envName)
	}
	// Resolve envName's workload cluster so cluster-scoped template overrides
	// (org ClusterValues[cluster]) show in the preview, exactly as they deploy.
	cluster := ah.envCluster(r.Context(), envName)
	// Preview scope (?preview=true): envName is the base env the preview clones;
	// layer the template+org preview defaults and the app's preview override on
	// top so composed and single-component apps show the same effective preview
	// (mirrors the publisher's previewRawValuesOverlay).
	var pv *previewLayer
	if r.URL.Query().Get("preview") == "true" {
		override := app.Spec.EnvironmentDefaults[domain.PreviewOverrideKey].RawValues
		if req.EnvRawValues != nil {
			if v, ok := req.EnvRawValues[domain.PreviewOverrideKey]; ok {
				override = v // unsaved edits from the editor win
			}
		}
		pv = &previewLayer{templateDefaults: mergedPreviewDefaults(t, ov), appOverride: override}
	}
	dto := effectiveValuesDTO(chartVals, canonicalBase, available, t, ov, envName, cluster, appRaw, envRaw, pv)
	// Composed app: the primary-template discovery above misses each component's
	// own chart+overlay, so REPLACE the discovered images with the union of every
	// component's images (each tagged with its owning component) — the app-level
	// Images panel is the single place they're managed.
	if app.Spec.IsComposed() {
		dto.DiscoveredImages = ah.componentDiscoveredImages(r.Context(), app, envName)
	}
	writeJSON(w, http.StatusOK, dto)
}

// envCluster resolves envName's workload cluster via the org Environment's
// EffectiveClusterRef, so cluster-scoped template overrides layer into the
// effective preview the same way they do at publish. Returns "" when there is no
// org provider, the org can't be read, the env is unknown, or it's unbound.
func (ah *appHandler) envCluster(ctx context.Context, envName string) string {
	if ah.orgProvider == nil {
		return ""
	}
	org, err := ah.orgProvider.GetOrg(ctx)
	if err != nil || org == nil {
		return ""
	}
	for _, e := range org.Environments {
		if e.Name == envName {
			return e.EffectiveClusterRef()
		}
	}
	return ""
}

// componentDiscoveredImages returns the union of a composed app's per-component
// discovered images (each tagged with its owning component), so the single
// app-level Images panel can list and route them to the right component's values.
func (ah *appHandler) componentDiscoveredImages(ctx context.Context, app *domain.App, envName string) []TemplateImageDTO {
	var out []TemplateImageDTO
	for _, c := range app.Spec.Components {
		if c.Template == nil {
			continue
		}
		t, ok := ah.lookupTemplate(ctx, c.Template.Name)
		if !ok || t == nil {
			continue
		}
		ov := loadOverride(ctx, ah.kubeClient, c.Template.Name)
		for _, img := range DiscoverComponentImages(ctx, ah.kubeClient, t, ov, envName, c.Values) {
			out = append(out, TemplateImageDTO{
				Name:       img.Name,
				Repository: img.Repository,
				TagKey:     img.TagKey,
				Component:  c.Name,
			})
		}
	}
	return out
}

// canonicalBaseMap renders the canonical HelmValues base suparship publishes for
// an app+env (app/components/suparship/routing) into a map, so the effective
// preview reflects the platform↔chart contract Helm applies — not just chart
// defaults + overlays. Routing host / cluster resolution is approximate here
// (nil profiles, empty baseDomain); the structural keys are what matter.
func (ah *appHandler) canonicalBaseMap(ctx context.Context, app *domain.App, envName string) map[string]any {
	envType := domain.AppEnvStaging
	namespace := ""
	if env, err := ah.appStore.GetAppEnvironment(ctx, app.ProjectName, app.Name, envName); err == nil && env != nil {
		envType = env.EnvType
		namespace = env.Namespace
	}
	orgName := ""
	if ah.orgProvider != nil {
		if org, err := ah.orgProvider.GetOrg(ctx); err == nil && org != nil {
			orgName = org.Name
		}
	}
	return CanonicalBaseMap(app, envName, envType, namespace, orgName)
}

// CanonicalBaseMap renders the canonical HelmValues base suparship publishes for
// an app+env (app/components/suparship/routing) into a map, so callers can layer
// it under the chart defaults + overrides to match what Helm renders. Routing
// host / cluster resolution is approximate (nil profiles, empty baseDomain); the
// structural keys (including each component's image block) are what matter for
// image discovery. Exported so the publish path discovers the same images the
// preview shows.
func CanonicalBaseMap(app *domain.App, envName string, envType domain.AppEnvironmentType, namespace, orgName string) map[string]any {
	hv := helmvalues.MapToHelmValuesForEnv(app, envName, envType, "", namespace, "", orgName, nil, nil, nil)
	raw, err := yaml.Marshal(hv)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := yaml.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// DiscoverAppImages returns the container images present in an app+env's effective
// Helm values (chart defaults ⊕ canonical base ⊕ template/org overlays ⊕ developer
// rawValues), each with its repository and dotted tag key. It is the publish-time
// counterpart of the values-preview discovery, so the images CD can manage match
// exactly what the UI lists. canonicalBase is included only for canonical
// templates (BYO/passthrough charts deploy their own values verbatim).
func DiscoverAppImages(ctx context.Context, kc kubernetes.Interface, t *tpl.Template, ov *domain.TemplateOverride, app *domain.App, envName string, envType domain.AppEnvironmentType, namespace, orgName string, appRaw, envRaw map[string]any) []tpl.TemplateImage {
	chartVals, _ := chartDefaults(ctx, kc, t)
	var canonicalBase map[string]any
	if t == nil || t.Spec.CanonicalValues() {
		canonicalBase = CanonicalBaseMap(app, envName, envType, namespace, orgName)
	}
	values := computeEffectiveValues(chartVals, canonicalBase, t, ov, envName, "", appRaw, envRaw)
	return chartimport.DetectImageMappings(values)
}

// DiscoverComponentImages returns the container images present in ONE composed
// component's effective Helm values: its template's chart defaults ⊕ the
// template/org value overlays ⊕ the component's own Values overlay. No canonical
// base is layered — for a composed component the platform base carries no image
// (each component's repository lives in its own overlay), so chart defaults ⊕
// overlay is the exact discovery surface, and it needs no app/env cluster context.
// This is the per-component counterpart of DiscoverAppImages, powering both the
// publish-time Warehouse resolution and the UI's per-component image checklist.
func DiscoverComponentImages(ctx context.Context, kc kubernetes.Interface, t *tpl.Template, ov *domain.TemplateOverride, envName string, overlay map[string]any) []tpl.TemplateImage {
	chartVals, _ := chartDefaults(ctx, kc, t)
	values := computeEffectiveValues(chartVals, nil, t, ov, envName, "", overlay, nil)
	return chartimport.DetectImageMappings(values)
}
