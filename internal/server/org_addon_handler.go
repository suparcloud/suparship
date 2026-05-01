package server

// org_addon_handler.go — org-level AddonProfile catalog CRUD.
//
// An AddonProfile pins which wrapper chart + provider serves a given
// addon type for apps in the org. App developers declare AppSpec.Addons
// claims by type only ("redis", "postgres"); the publisher resolves
// them through the org catalog (and any per-env override on
// OrgEnvironment.AddonProfiles) at publish time.
//
// Endpoints (reads require authentication; writes require org_admin role):
//
//	GET    /api/v1/org/addon-profiles
//	PUT    /api/v1/org/addon-profiles/{type}
//	DELETE /api/v1/org/addon-profiles/{type}

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/addons/contracts"
	"github.com/suparcloud/suparship/internal/domain"
)

// AddonProfileDTO is the JSON wire form of one profile entry. Mirrors
// domain.AddonProfile with consumer-friendly tags.
type AddonProfileDTO struct {
	Type     string         `json:"type"`
	Provider string         `json:"provider"`
	Chart    string         `json:"chart"`
	Defaults map[string]any `json:"defaults,omitempty"`
}

// GET /api/v1/org/addon-profiles — list the org-level catalog only.
// Per-env overrides are visible via the org-environment endpoints.
func (rh *rbacHandler) handleListOrgAddonProfiles(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	dtos := make([]AddonProfileDTO, 0, len(org.AddonProfiles))
	for typ, p := range org.AddonProfiles {
		dtos = append(dtos, AddonProfileDTO{
			Type:     typ,
			Provider: p.Provider,
			Chart:    p.Chart,
			Defaults: p.Defaults,
		})
	}
	sort.Slice(dtos, func(i, j int) bool { return dtos[i].Type < dtos[j].Type })
	writeJSON(w, http.StatusOK, map[string]any{
		"addonProfiles":   dtos,
		"availableTypes":  contracts.Types(), // helps the UI render a "type" picker
	})
}

// upsertAddonProfileRequest is the body for PUT.
//
// Provider + Chart are required; Defaults is optional. The wrapper
// chart referenced by Chart must be registered as a built-in template
// (or imported via the chart-import flow) — the publisher fails the
// addon's app.yaml render if not.
type upsertAddonProfileRequest struct {
	Provider string         `json:"provider"`
	Chart    string         `json:"chart"`
	Defaults map[string]any `json:"defaults,omitempty"`
}

// PUT /api/v1/org/addon-profiles/{type} — upsert one entry by addon
// type. Validates that the type matches a registered connection
// contract (contracts.Lookup) so an operator can't configure a profile
// for a type no wrapper template understands.
func (rh *rbacHandler) handlePutOrgAddonProfile(w http.ResponseWriter, r *http.Request) {
	addonType := r.PathValue("type")
	if _, ok := contracts.Lookup(addonType); !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "addon type must match a registered connection contract; see GET /api/v1/org/addon-profiles for valid types",
		})
		return
	}

	var req upsertAddonProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.Provider == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "provider is required"})
		return
	}
	if req.Chart == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "chart is required"})
		return
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	if org.AddonProfiles == nil {
		org.AddonProfiles = domain.AddonProfiles{}
	}
	org.AddonProfiles[addonType] = domain.AddonProfile{
		Type:     addonType,
		Provider: req.Provider,
		Chart:    req.Chart,
		Defaults: req.Defaults,
	}

	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, AddonProfileDTO{
		Type:     addonType,
		Provider: req.Provider,
		Chart:    req.Chart,
		Defaults: req.Defaults,
	})
}

// DELETE /api/v1/org/addon-profiles/{type} — remove one entry.
// Idempotent: deleting a non-existent profile returns 204.
func (rh *rbacHandler) handleDeleteOrgAddonProfile(w http.ResponseWriter, r *http.Request) {
	addonType := r.PathValue("type")

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	if _, ok := org.AddonProfiles[addonType]; !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	delete(org.AddonProfiles, addonType)
	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org: " + err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
