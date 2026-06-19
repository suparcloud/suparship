package server

import (
	"encoding/json"
	"net/http"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
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
// Edits a template's metadata (title/category/description, and the passthrough
// toggle for imported templates). org_admin only. The persistence path depends
// on provenance:
//   - imported/BYO (cluster ConfigMap, no external repo): edited IN PLACE by
//     re-writing the stored template.yaml; chart bytes preserved. The
//     injectCanonicalValues toggle is allowed here.
//   - synced (external repo) or built-in (disk): the template body is read-only
//     (a sync / new build would clobber an in-place edit), so title/category/
//     description are saved to the sync-safe override ConfigMap instead and
//     merged over the template at read time. injectCanonicalValues is rejected
//     for these — it changes values semantics and belongs at the source.
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

	var patch templateMetadataPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	// Resolve the current (cluster) template to mutate / echo back.
	_, byName := th.resolve(r.Context())
	t, ok := byName[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "template not found"})
		return
	}

	// Read-only body (synced / built-in): persist a sync-safe metadata override.
	if !editable {
		th.updateMetadataViaOverride(w, r, name, src, t, patch)
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

// updateMetadataViaOverride persists title/category/description for a read-only
// (synced or built-in) template into the sync-safe override ConfigMap, leaving
// the template body untouched. Returns the template DTO with the override
// applied. injectCanonicalValues is refused here — it changes values semantics,
// not display metadata, and a re-sync/rebuild would not honor it from an override.
func (th *templateHandler) updateMetadataViaOverride(
	w http.ResponseWriter, r *http.Request, name string, src *TemplateSourceDTO, t *tpl.Template, patch templateMetadataPatch,
) {
	if patch.InjectCanonicalValues != nil {
		where := "the source repo"
		if src.Origin == "builtin" {
			where = "an imported copy"
		}
		writeJSON(w, http.StatusConflict, errorResponse{
			Error: "values mode for template " + name + " can only be changed at " + where + " — it isn't a display-metadata override",
		})
		return
	}

	ov, err := kube.LoadTemplateOverride(r.Context(), th.kubeClient, name)
	if err != nil {
		if th.logger != nil {
			th.logger.Error("load template override for metadata edit", "name", name, "err", err)
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load template override"})
		return
	}
	if ov == nil {
		ov = &domain.TemplateOverride{}
	}
	if ov.Metadata == nil {
		ov.Metadata = &domain.TemplateMetadataOverride{}
	}
	// Each provided field is set verbatim — an empty string clears that
	// override (reverting to the template's own value).
	if patch.Title != nil {
		ov.Metadata.Title = *patch.Title
	}
	if patch.Category != nil {
		ov.Metadata.Category = *patch.Category
	}
	if patch.Description != nil {
		ov.Metadata.Description = *patch.Description
	}

	if err := kube.SaveTemplateOverride(r.Context(), th.kubeClient, name, ov); err != nil {
		if th.logger != nil {
			th.logger.Error("save template metadata override", "name", name, "err", err)
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save metadata override"})
		return
	}

	dto := templateToDetail(t)
	dto.Title, dto.Category, dto.Description = applyMetadataOverride(dto.Title, dto.Category, dto.Description, ov.Metadata)
	dto.Source, dto.Editable = th.templateProvenance(r.Context(), name)
	writeJSON(w, http.StatusOK, dto)
}
