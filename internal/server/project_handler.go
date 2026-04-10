package server

// project_handler.go — project CRUD handlers
//
// Endpoints:
//
//	POST /api/v1/projects         — create a new project (org_admin only)
//	DELETE /api/v1/projects/{p}   — delete a project    (org_admin only)
//
// List and detail are served by org.go (handleGetProjects / handleGetProject).

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
)

// projectNameRE allows lowercase letters, digits, and hyphens; must start with
// a letter; max 48 characters (fits safely in Kubernetes label values and
// namespace names after adding environment suffixes).
var projectNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

// ── POST /api/v1/projects ─────────────────────────────────────────────────────

type createProjectRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
}

func (rh *rbacHandler) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	if rh.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "project store not configured"})
		return
	}

	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
		return
	}
	if !projectNameRE.MatchString(req.Name) {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "name must start with a lowercase letter and contain only lowercase letters, digits, or hyphens (max 48 chars)",
		})
		return
	}

	// Reject duplicates.
	if _, err := rh.projectStore.Get(r.Context(), req.Name); err == nil {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "project already exists"})
		return
	}

	p := &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: req.Name},
		Spec: project.ProjectSpec{
			DisplayName: req.DisplayName,
			Description: req.Description,
			// Environments intentionally empty — inherited from org defaults.
			Environments: []project.Environment{},
			Services:     []project.Service{},
		},
	}

	if err := rh.projectStore.Save(r.Context(), p); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to create project: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, ProjectDTO{
		Name:        p.Metadata.Name,
		DisplayName: p.Spec.DisplayName,
		Description: p.Spec.Description,
	})
}

// ── DELETE /api/v1/projects/{project} ────────────────────────────────────────

func (rh *rbacHandler) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	if rh.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "project store not configured"})
		return
	}

	projectName := r.PathValue("project")

	if err := rh.projectStore.Delete(r.Context(), projectName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
		return
	}

	// Also remove any project-scoped role bindings from the org config so the
	// deleted project doesn't linger in the RBAC list.
	org, err := rh.orgStore.GetOrg(r.Context())
	if err == nil {
		bindings := org.RoleBindings[:0]
		for _, rb := range org.RoleBindings {
			if rb.Project != projectName {
				bindings = append(bindings, rb)
			}
		}
		org.RoleBindings = bindings
		_ = rh.orgStore.SaveOrg(r.Context(), org) // best-effort; project is already deleted
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── requireOrgAdminForProject ─────────────────────────────────────────────────
// Middleware used by project-create and project-delete which are org-admin ops.

func (rh *rbacHandler) orgAdminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFromContext(r.Context())
		org, err := rh.orgStore.GetOrg(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org"})
			return
		}
		if !org.HasPermission(sess.Username, "*", rbac.RoleOrgAdmin) {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "org_admin role required"})
			return
		}
		next(w, r)
	}
}
