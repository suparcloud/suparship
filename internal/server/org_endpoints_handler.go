package server

// org_endpoints_handler.go — org-level endpoint (URL scheme) configuration
//
// Endpoints (reads require authentication; writes require org_admin role):
//
//	GET  /api/v1/org/endpoints
//	PUT  /api/v1/org/endpoints

import (
	"encoding/json"
	"net/http"
)

// OrgEndpointsDTO is the JSON body for GET and PUT /api/v1/org/endpoints.
type OrgEndpointsDTO struct {
	// SecureEndpoints controls the URL scheme of suparship-generated app
	// endpoints (preview/env URLs, Gateway-API HTTPRoute URLs): https when
	// true, http when false. Defaults to true; local/dev installs with a
	// TLS-less ingress set it false. TLS termination itself remains the
	// routing profile's / chart's concern.
	SecureEndpoints bool `json:"secureEndpoints"`
}

// GET /api/v1/org/endpoints
func (rh *rbacHandler) handleGetOrgEndpoints(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	writeJSON(w, http.StatusOK, OrgEndpointsDTO{SecureEndpoints: org.EffectiveSecureEndpoints()})
}

// PUT /api/v1/org/endpoints
func (rh *rbacHandler) handlePutOrgEndpoints(w http.ResponseWriter, r *http.Request) {
	var req OrgEndpointsDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	org.SecureEndpoints = &req.SecureEndpoints

	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org"})
		return
	}

	writeJSON(w, http.StatusOK, OrgEndpointsDTO{SecureEndpoints: org.EffectiveSecureEndpoints()})
}
