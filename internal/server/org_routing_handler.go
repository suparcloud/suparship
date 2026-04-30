package server

// org_routing_handler.go — org-level routing-profile CRUD.
//
// A routing profile maps an ExposeMode (internal/external) to the ingress
// class + cert-manager ClusterIssuer the chart should use for components
// targeting that tier. Org-level profiles set the defaults; per-environment
// overrides live on OrgEnvironment.RoutingProfiles and replace matching
// names sparsely.
//
// Endpoints (reads require authentication; writes require org_admin role):
//
//	GET    /api/v1/org/routing-profiles
//	PUT    /api/v1/org/routing-profiles/{name}
//	DELETE /api/v1/org/routing-profiles/{name}

import (
	"encoding/json"
	"net/http"

	"github.com/suparcloud/suparship/internal/domain"
)

// RoutingProfileDTO is the JSON wire form of a single profile. Mirrors
// domain.RoutingProfile but uses JSON tags suited for a UI client.
type RoutingProfileDTO struct {
	Name             string `json:"name"`
	IngressClassName string `json:"ingressClassName"`
	ClusterIssuer    string `json:"clusterIssuer,omitempty"`
	BaseDomain       string `json:"baseDomain,omitempty"`
}

// GET /api/v1/org/routing-profiles — returns the org-level map only.
// Per-env overrides are visible via the org-environment endpoints.
func (rh *rbacHandler) handleListOrgRoutingProfiles(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	dtos := make([]RoutingProfileDTO, 0, len(org.RoutingProfiles))
	for name, p := range org.RoutingProfiles {
		dtos = append(dtos, RoutingProfileDTO{
			Name:             name,
			IngressClassName: p.IngressClassName,
			ClusterIssuer:    p.ClusterIssuer,
			BaseDomain:       p.BaseDomain,
		})
	}
	// Stable order: profiles render as a list in the UI.
	sortRoutingProfiles(dtos)
	writeJSON(w, http.StatusOK, map[string]any{"routingProfiles": dtos})
}

// upsertRoutingProfileRequest is the body for PUT.
//
// IngressClassName is required. ClusterIssuer is optional — empty means
// plain HTTP ingress (no cert-manager annotation, no tls block). BaseDomain
// is optional and overrides Environment.BaseDomain when set.
type upsertRoutingProfileRequest struct {
	IngressClassName string `json:"ingressClassName"`
	ClusterIssuer    string `json:"clusterIssuer,omitempty"`
	BaseDomain       string `json:"baseDomain,omitempty"`
}

// PUT /api/v1/org/routing-profiles/{name} — upsert a single profile by
// ExposeMode name. Validates that the name is one of "internal" or
// "external" (the closed set on ExposeMode); "disabled" is not a profile
// name and is rejected.
func (rh *rbacHandler) handlePutOrgRoutingProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	mode := domain.ExposeMode(name)
	if !mode.Valid() || mode == domain.ExposeDisabled {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "name must be one of \"internal\" or \"external\"",
		})
		return
	}

	var req upsertRoutingProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.IngressClassName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "ingressClassName is required"})
		return
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	if org.RoutingProfiles == nil {
		org.RoutingProfiles = domain.RoutingProfiles{}
	}
	org.RoutingProfiles[name] = domain.RoutingProfile{
		IngressClassName: req.IngressClassName,
		ClusterIssuer:    req.ClusterIssuer,
		BaseDomain:       req.BaseDomain,
	}

	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, RoutingProfileDTO{
		Name:             name,
		IngressClassName: req.IngressClassName,
		ClusterIssuer:    req.ClusterIssuer,
		BaseDomain:       req.BaseDomain,
	})
}

// DELETE /api/v1/org/routing-profiles/{name} — remove a single profile.
// Idempotent: deleting a non-existent profile returns 204.
func (rh *rbacHandler) handleDeleteOrgRoutingProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	if _, ok := org.RoutingProfiles[name]; !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	delete(org.RoutingProfiles, name)
	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org: " + err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sortRoutingProfiles sorts in-place by Name ascending. Small enough that
// a single bubble pass is fine — typically two entries (internal/external).
func sortRoutingProfiles(dtos []RoutingProfileDTO) {
	for i := 1; i < len(dtos); i++ {
		for j := i; j > 0 && dtos[j-1].Name > dtos[j].Name; j-- {
			dtos[j-1], dtos[j] = dtos[j], dtos[j-1]
		}
	}
}
