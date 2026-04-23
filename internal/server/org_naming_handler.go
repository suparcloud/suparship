package server

// org_naming_handler.go — org-level namespace naming pattern configuration
//
// Endpoints (reads require authentication; writes require org_admin role):
//
//	GET  /api/v1/org/naming
//	PUT  /api/v1/org/naming

import (
	"encoding/json"
	"net/http"

	"github.com/suparcloud/suparship/internal/secrets"
)

// OrgNamingDTO is the JSON body for GET and PUT /api/v1/org/naming.
// It exposes only the namespace pattern fields from ResourceNaming; other
// naming settings (vault items, ClusterSecretStore) are managed separately.
type OrgNamingDTO struct {
	// ProjectNamespace is the org-wide default pattern for project-level
	// Kubernetes namespaces. Tokens: {org}, {project}, {env}.
	// Empty = topology-aware default applied at namespace-resolution time.
	//   Shared cluster:    "{project}-{env}" → "myproject-staging"
	//   Dedicated cluster: "{project}"       → "myproject"
	ProjectNamespace string `json:"projectNamespace,omitempty"`

	// AppNamespace is the org-wide default pattern for app-level
	// Kubernetes namespaces. Tokens: {org}, {project}, {app}, {env}.
	// Empty = topology-aware default applied at namespace-resolution time.
	//   Shared cluster:    "{project}-{app}-{env}" → "myproject-api-staging"
	//   Dedicated cluster: "{project}-{app}"       → "myproject-api"
	AppNamespace string `json:"appNamespace,omitempty"`
}

// GET /api/v1/org/naming
func (rh *rbacHandler) handleGetOrgNaming(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}
	writeJSON(w, http.StatusOK, OrgNamingDTO{
		ProjectNamespace: org.ResourceNaming.ProjectNamespace,
		AppNamespace:     org.ResourceNaming.AppNamespace,
	})
}

// PUT /api/v1/org/naming
func (rh *rbacHandler) handlePutOrgNaming(w http.ResponseWriter, r *http.Request) {
	var req OrgNamingDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	// Validate the patterns produce valid Kubernetes namespace names.
	// Use a sample set of values representative of what would be rendered.
	sample := secrets.NamingParams{Org: "default", Env: "prod", Project: "acme", App: "web"}
	if req.ProjectNamespace != "" {
		rendered := secrets.RenderPattern(req.ProjectNamespace, sample)
		if err := secrets.ValidateRenderedNamespace(rendered, "projectNamespace"); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
	}
	if req.AppNamespace != "" {
		rendered := secrets.RenderPattern(req.AppNamespace, sample)
		if err := secrets.ValidateRenderedNamespace(rendered, "appNamespace"); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
	}

	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
		return
	}

	org.ResourceNaming.ProjectNamespace = req.ProjectNamespace
	org.ResourceNaming.AppNamespace = req.AppNamespace

	if err := rh.orgStore.SaveOrg(r.Context(), org); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save org"})
		return
	}

	writeJSON(w, http.StatusOK, OrgNamingDTO{
		ProjectNamespace: org.ResourceNaming.ProjectNamespace,
		AppNamespace:     org.ResourceNaming.AppNamespace,
	})
}
