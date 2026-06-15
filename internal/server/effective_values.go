package server

import (
	"context"
	"encoding/json"
	"net/http"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/helmvalues"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/chartimport"
)

// EffectiveValuesDTO is the read-only "what will deploy" preview that backs the
// values-editor UI. It is the values-document layering (chart defaults ⊕ template
// platform/env defaults ⊕ developer rawValues), NOT the fully rendered chart:
// the canonical struct-mapping and {platform.*}/{vars.*} interpolation are applied
// later at publish with per-env cluster/domain context that isn't available here.
type EffectiveValuesDTO struct {
	// Values is the merged values document.
	Values map[string]any `json:"values"`
	// ChartDefaultsAvailable is false when the chart bundle couldn't be read
	// (built-in/disk/external-mode templates) — the preview then reflects only
	// the platform/env defaults + overrides, not the chart's own defaults.
	ChartDefaultsAvailable bool `json:"chartDefaultsAvailable"`
	// Interpolated reports whether {platform.*}/{vars.*} tokens were resolved.
	// Always false in v1 — tokens are shown literally because the platform
	// context is only fully known at publish time.
	Interpolated bool `json:"interpolated"`
	// Layers names the overlays that contributed, low→high, for UI hints.
	Layers []string `json:"layers"`
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
//	chart defaults ⊕ template DefaultValues ⊕ template EnvValues[env] ⊕ appRaw ⊕ envRaw
//
// Inputs are deep-copied so callers' maps (the stored app spec, the template) are
// never mutated.
func computeEffectiveValues(chartVals map[string]any, t *tpl.Template, envName string, appRaw, envRaw map[string]any) map[string]any {
	out := helmvalues.DeepCopyMap(chartVals)
	if out == nil {
		out = map[string]any{}
	}
	if t != nil {
		out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(t.Spec.DefaultValues))
		if envName != "" && t.Spec.EnvValues != nil {
			out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(t.Spec.EnvValues[envName]))
		}
	}
	out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(appRaw))
	out = helmvalues.DeepMerge(out, helmvalues.DeepCopyMap(envRaw))
	return out
}

func effectiveValuesDTO(chartVals map[string]any, available bool, t *tpl.Template, envName string, appRaw, envRaw map[string]any) EffectiveValuesDTO {
	layers := []string{}
	if available {
		layers = append(layers, "chart defaults")
	}
	if t != nil && len(t.Spec.DefaultValues) > 0 {
		layers = append(layers, "platform defaults")
	}
	if t != nil && envName != "" && len(t.Spec.EnvValues[envName]) > 0 {
		layers = append(layers, "platform "+envName)
	}
	if len(appRaw) > 0 {
		layers = append(layers, "app overrides")
	}
	if len(envRaw) > 0 {
		layers = append(layers, envName+" overrides")
	}
	return EffectiveValuesDTO{
		Values:                 computeEffectiveValues(chartVals, t, envName, appRaw, envRaw),
		ChartDefaultsAvailable: available,
		Interpolated:           false,
		Layers:                 layers,
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
	chartVals, available := chartDefaults(r.Context(), th.kubeClient, t)
	writeJSON(w, http.StatusOK, effectiveValuesDTO(chartVals, available, t, env, nil, nil))
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
	chartVals, available := chartDefaults(r.Context(), ah.kubeClient, t)
	writeJSON(w, http.StatusOK, effectiveValuesDTO(chartVals, available, t, envName, appRaw, envRaw))
}
