package server

import (
	"encoding/json"
	"net/http"
	"strings"

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
	// Images, when non-nil, replaces the template's per-service image mapping
	// (external-CD wiring). Only honored for editable (imported) templates; send
	// an empty array to clear it. Read-only/synced templates reject it.
	Images *[]TemplateImageDTO `json:"images,omitempty"`
	// DeveloperValues, when non-nil, replaces the developer-facing values
	// projection (which Helm value paths the app-creation editor exposes). Send an
	// empty array to clear it, reverting to full-base seeding. Like Images this is
	// curation rather than source chart content, so it is honored for read-only
	// synced/built-in templates too — stored sync-safe.
	DeveloperValues *[]ValueFieldDTO `json:"developerValues,omitempty"`
	// DeliveryMode, when non-nil, sets the template's default app delivery mode:
	// "pipeline" (Kargo + promotion) or "direct" (deploy each env from values, no
	// Kargo); "" reverts to the default (pipeline). Only honored for editable
	// (imported) templates — it changes app-creation semantics, not display
	// metadata, so read-only/synced templates reject it (set it at the source).
	DeliveryMode *string `json:"deliveryMode,omitempty"`
	// Disabled, when non-nil, retires (true) or restores (false) the template:
	// disabled templates stay listed (marked) but refuse new apps; existing
	// apps keep working. Stored in the sync-safe override for EVERY provenance,
	// including built-ins — it's operational state, not template content, and
	// must survive re-syncs and image rebuilds alike.
	Disabled *bool `json:"disabled,omitempty"`
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

	// Disabled is handled up front, for every provenance: it lives in the
	// sync-safe override even for editable templates, because it's operational
	// state — writing it into template.yaml would silently resurrect the
	// template on the next import/sync.
	if patch.Disabled != nil {
		ov, err := kube.LoadTemplateOverride(r.Context(), th.kubeClient, name)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load template override"})
			return
		}
		if ov == nil {
			ov = &domain.TemplateOverride{}
		}
		ov.Disabled = *patch.Disabled
		if err := kube.SaveTemplateOverride(r.Context(), th.kubeClient, name, ov); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save template override"})
			return
		}
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
	if patch.Images != nil {
		updated.Spec.Images = imagesFromDTO(*patch.Images)
	}
	if patch.DeveloperValues != nil {
		updated.Spec.DeveloperValues = developerValuesFromDTO(*patch.DeveloperValues)
	}
	if patch.DeliveryMode != nil {
		mode := strings.TrimSpace(*patch.DeliveryMode)
		if mode != "" && mode != string(domain.DeliveryPipeline) && mode != string(domain.DeliveryDirect) {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "deliveryMode must be \"pipeline\" or \"direct\""})
			return
		}
		updated.Spec.DeliveryMode = mode
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
	if ov, err := kube.LoadTemplateOverride(r.Context(), th.kubeClient, name); err == nil && ov != nil {
		dto.Disabled = ov.Disabled
	}
	dto.Source, dto.Editable = th.templateProvenance(r.Context(), name)
	writeJSON(w, http.StatusOK, dto)
}

// updateMetadataViaOverride persists title/category/description and the image
// mapping for a read-only (synced or built-in) template into the sync-safe
// override ConfigMap, leaving the template body untouched. Returns the template
// DTO with the override applied. injectCanonicalValues is refused here — it
// changes values semantics, not display metadata, and a re-sync/rebuild would
// not honor it from an override. Image mappings ARE allowed: they're CD wiring,
// stored sync-safe so a re-sync can't drop them.
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
	if patch.DeliveryMode != nil {
		mode := strings.TrimSpace(*patch.DeliveryMode)
		if mode != "" && mode != string(domain.DeliveryPipeline) && mode != string(domain.DeliveryDirect) {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "deliveryMode must be \"pipeline\" or \"direct\""})
			return
		}
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
	// Image mappings ARE supported as a sync-safe override: they're CD wiring
	// (which image repos to watch + which values keys hold the tags), not source
	// chart content, so a re-sync must not drop them. Validate by reusing the
	// template's own validation with the proposed images applied.
	if patch.Images != nil {
		check := *t
		check.Spec.Images = imagesFromDTO(*patch.Images)
		if err := check.Validate(); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
		ov.Images = imagesToOverride(*patch.Images)
	}
	// The developer-values projection is sync-safe curation for the same reason:
	// it decides which values the app editor shows, not what the chart renders.
	// Validate by applying the proposed list to a copy of the template.
	if patch.DeveloperValues != nil {
		check := *t
		check.Spec.DeveloperValues = developerValuesFromDTO(*patch.DeveloperValues)
		if err := check.Validate(); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
		ov.DeveloperValues = developerValuesToOverride(*patch.DeveloperValues)
	}
	// Delivery mode is a sync-safe override too: it sets how apps from this
	// template deliver (pipeline vs direct), not source chart content.
	if patch.DeliveryMode != nil {
		ov.DeliveryMode = strings.TrimSpace(*patch.DeliveryMode)
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
	if len(ov.Images) > 0 {
		dto.Images = imagesOverrideToDTO(ov.Images)
	}
	if len(ov.DeveloperValues) > 0 {
		dto.DeveloperValues = developerValuesOverrideToDTO(ov.DeveloperValues)
	}
	if ov.DeliveryMode != "" {
		dto.DeliveryMode = ov.DeliveryMode
	}
	dto.Disabled = ov.Disabled
	dto.Source, dto.Editable = th.templateProvenance(r.Context(), name)
	writeJSON(w, http.StatusOK, dto)
}
