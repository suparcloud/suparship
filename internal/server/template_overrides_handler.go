package server

import (
	"encoding/json"
	"net/http"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/kube"
)

// TemplateOverrideDTO is the wire shape for a template's org-level platform
// override — the PE/SRE-authored Helm values layered on top of the template's
// own DefaultValues/EnvValues and below developer overrides at publish.
type TemplateOverrideDTO struct {
	DefaultValues map[string]any            `json:"defaultValues,omitempty"`
	EnvValues     map[string]map[string]any `json:"envValues,omitempty"`
	ClusterValues map[string]map[string]any `json:"clusterValues,omitempty"`
}

// handleGetTemplateOverride serves GET /api/v1/templates/{name}/overrides.
// Returns the saved org-level override (empty object when none). requireAuth.
func (th *templateHandler) handleGetTemplateOverride(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "template name required"})
		return
	}
	if th.kubeClient == nil {
		writeJSON(w, http.StatusOK, TemplateOverrideDTO{})
		return
	}
	ov, err := kube.LoadTemplateOverride(r.Context(), th.kubeClient, name)
	if err != nil {
		if th.logger != nil {
			th.logger.Error("load template override", "name", name, "err", err)
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load template override"})
		return
	}
	if ov == nil {
		writeJSON(w, http.StatusOK, TemplateOverrideDTO{})
		return
	}
	writeJSON(w, http.StatusOK, TemplateOverrideDTO{DefaultValues: ov.DefaultValues, EnvValues: ov.EnvValues, ClusterValues: ov.ClusterValues})
}

// handlePutTemplateOverride serves PUT /api/v1/templates/{name}/overrides.
// Replaces the org-level override (send an empty body to clear). org_admin only.
func (th *templateHandler) handlePutTemplateOverride(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "template name required"})
		return
	}
	if th.kubeClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "cluster client not configured"})
		return
	}
	var dto TemplateOverrideDTO
	if err := json.NewDecoder(r.Body).Decode(&dto); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	// The override ConfigMap also co-stores the image mapping and display-metadata
	// overrides (set from the Images / metadata editors). This endpoint only owns
	// the Helm values layer, so load-modify-write: replace just the values fields
	// and preserve Images/Metadata, otherwise saving platform overrides would
	// clobber an image mapping a PE wired up for CD.
	ov, err := kube.LoadTemplateOverride(r.Context(), th.kubeClient, name)
	if err != nil {
		if th.logger != nil {
			th.logger.Error("load template override", "name", name, "err", err)
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load template override"})
		return
	}
	if ov == nil {
		ov = &domain.TemplateOverride{}
	}
	ov.DefaultValues, ov.EnvValues, ov.ClusterValues = dto.DefaultValues, dto.EnvValues, dto.ClusterValues
	if err := kube.SaveTemplateOverride(r.Context(), th.kubeClient, name, ov); err != nil {
		if th.logger != nil {
			th.logger.Error("save template override", "name", name, "err", err)
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save template override"})
		return
	}
	writeJSON(w, http.StatusOK, TemplateOverrideDTO{DefaultValues: ov.DefaultValues, EnvValues: ov.EnvValues, ClusterValues: ov.ClusterValues})
}

// handlePostEffectiveValues serves POST /api/v1/templates/{name}/effective-values?env={env}.
// Previews the effective values with the request's UNSAVED org override layered
// in (chart ⊕ template ⊕ supplied override), so the PE editor can show a live
// preview as it's edited. Computes only — never persists. requireAuth.
func (th *templateHandler) handlePostEffectiveValues(w http.ResponseWriter, r *http.Request) {
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
	var dto TemplateOverrideDTO
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&dto)
	}
	ov := &domain.TemplateOverride{DefaultValues: dto.DefaultValues, EnvValues: dto.EnvValues, ClusterValues: dto.ClusterValues}
	env := r.URL.Query().Get("env")
	cluster := r.URL.Query().Get("cluster")
	chartVals, available := chartDefaults(r.Context(), th.kubeClient, t)
	// Template-level preview: no concrete app, so no canonical base.
	writeJSON(w, http.StatusOK, effectiveValuesDTO(chartVals, nil, available, t, ov, env, cluster, nil, nil))
}
