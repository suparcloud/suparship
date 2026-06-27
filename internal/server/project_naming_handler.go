package server

// project_naming_handler.go — project-level namespace pattern configuration
//
// Endpoints (reads require project viewer role; writes require project_admin):
//
//	GET  /api/v1/projects/{project}/naming
//	PUT  /api/v1/projects/{project}/naming

import (
	"encoding/json"
	"net/http"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/secrets"
)

// ProjectNamingDTO is the JSON body for GET and PUT
// /api/v1/projects/{project}/naming.
type ProjectNamingDTO struct {
	// NamespacePattern overrides the org-level namespace default for this
	// project. Tokens: {org}, {project}, {env}.
	// Empty = inherit from OrgEnvironment.NamespacePattern or org ResourceNaming.
	NamespacePattern string `json:"namespacePattern,omitempty"`
	// PreviewNamespacePattern controls the namespace of this project's preview
	// environments. Tokens: {project}, {app}, {name}. Must contain {name}.
	// Empty = "{project}-{app}-preview-{name}".
	PreviewNamespacePattern string `json:"previewNamespacePattern,omitempty"`
}

// GET /api/v1/projects/{project}/naming
func (rh *rbacHandler) handleGetProjectNaming(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	if rh.projectStore == nil {
		writeJSON(w, http.StatusOK, ProjectNamingDTO{})
		return
	}
	proj, err := rh.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
		return
	}
	writeJSON(w, http.StatusOK, ProjectNamingDTO{
		NamespacePattern:        proj.Spec.NamespacePattern,
		PreviewNamespacePattern: proj.Spec.PreviewNamespacePattern,
	})
}

// PUT /api/v1/projects/{project}/naming
func (rh *rbacHandler) handlePutProjectNaming(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	if rh.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "project store not available"})
		return
	}

	var req ProjectNamingDTO
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.NamespacePattern != "" {
		sample := secrets.NamingParams{Org: "default", Env: "prod", Project: "acme", App: "web"}
		rendered := secrets.RenderPattern(req.NamespacePattern, sample)
		if err := secrets.ValidateRenderedNamespace(rendered, "namespacePattern"); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
	}
	// Preview pattern: requires the {name} token (per-PR uniqueness) and a
	// DNS-valid resolution. Empty inherits the default.
	if err := domain.ValidatePreviewNamespacePattern(req.PreviewNamespacePattern); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	proj, err := rh.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
		return
	}

	proj.Spec.NamespacePattern = req.NamespacePattern
	proj.Spec.PreviewNamespacePattern = req.PreviewNamespacePattern

	if err := rh.projectStore.Save(r.Context(), proj); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save project"})
		return
	}

	writeJSON(w, http.StatusOK, ProjectNamingDTO{
		NamespacePattern:        proj.Spec.NamespacePattern,
		PreviewNamespacePattern: proj.Spec.PreviewNamespacePattern,
	})
}
