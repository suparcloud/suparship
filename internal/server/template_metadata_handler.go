package server

import (
	"encoding/json"
	"net/http"

	"github.com/suparcloud/suparship/internal/kube"
)

// templateMetadataPatch is the PATCH body for editing template metadata. All
// fields are pointers so only the provided ones are applied (partial update).
type templateMetadataPatch struct {
	Title                 *string `json:"title,omitempty"`
	Category              *string `json:"category,omitempty"`
	Description           *string `json:"description,omitempty"`
	InjectCanonicalValues *bool   `json:"injectCanonicalValues,omitempty"`
}

// handleUpdateTemplateMetadata serves PATCH /api/v1/templates/{name}.
//
// Edits an imported/BYO template's metadata in place (title/category/description/
// passthrough toggle) by re-writing its stored template.yaml — the chart bytes
// are preserved. org_admin only. Refuses:
//   - 404 when the template isn't cluster-stored (disk built-in — ships on disk).
//   - 409 when the template is externally synced (a sync would clobber the edit;
//     fix it at the source).
//
// Setting injectCanonicalValues:false (passthrough) also clears the inferred
// inputs/advancedInputs/mappings — a BYO chart doesn't use them.
func (th *templateHandler) handleUpdateTemplateMetadata(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "template name required"})
		return
	}
	if th.kubeClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "cluster client not configured"})
		return
	}

	src, editable := th.templateProvenance(r.Context(), name)
	if !editable {
		switch src.Origin {
		case "synced":
			writeJSON(w, http.StatusConflict, errorResponse{
				Error: "template " + name + " is managed by " + src.ExternalRepo + " — edit it at the source; a sync would overwrite changes made here",
			})
		default:
			writeJSON(w, http.StatusNotFound, errorResponse{
				Error: "template " + name + " is a built-in default (shipped on disk); not editable from the API",
			})
		}
		return
	}

	var patch templateMetadataPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	// Resolve the current (cluster) template to mutate.
	_, byName := th.resolve(r.Context())
	t, ok := byName[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "template not found"})
		return
	}
	updated := *t // shallow copy; we only touch Spec scalars below

	if patch.Title != nil {
		updated.Spec.Title = *patch.Title
	}
	if patch.Category != nil {
		updated.Spec.Category = *patch.Category
	}
	if patch.Description != nil {
		updated.Spec.Description = *patch.Description
	}
	if patch.InjectCanonicalValues != nil {
		updated.Spec.InjectCanonicalValues = patch.InjectCanonicalValues
		// Passthrough/BYO charts don't use template inputs — drop the inferred ones.
		if !*patch.InjectCanonicalValues {
			updated.Spec.Inputs = nil
			updated.Spec.AdvancedInputs = nil
			updated.Spec.Mappings = nil
		}
	}

	if err := updated.Validate(); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	// Preserve the chart bytes; re-write the template.yaml in the ConfigMap.
	chartTGZ, err := kube.LoadChartBundle(r.Context(), th.kubeClient, name)
	if err != nil {
		if th.logger != nil {
			th.logger.Error("load chart bundle for metadata edit", "name", name, "err", err)
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load chart bundle"})
		return
	}
	if err := kube.SaveTemplate(r.Context(), th.kubeClient, &updated, chartTGZ); err != nil {
		if th.logger != nil {
			th.logger.Error("save template metadata", "name", name, "err", err)
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save template"})
		return
	}

	dto := templateToDetail(&updated)
	dto.Source, dto.Editable = th.templateProvenance(r.Context(), name)
	writeJSON(w, http.StatusOK, dto)
}
