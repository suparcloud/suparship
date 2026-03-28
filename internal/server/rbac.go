package server

import (
	"net/http"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
)

// ProjectExtractor returns the project name relevant to a request.
type ProjectExtractor func(r *http.Request) string

// ProjectFromPathValue returns a ProjectExtractor that reads a named path
// parameter set by the Go 1.22+ ServeMux pattern (e.g. {project}).
func ProjectFromPathValue(param string) ProjectExtractor {
	return func(r *http.Request) string {
		return r.PathValue(param)
	}
}

// rbacHandler provides RBAC enforcement middleware and placeholder project
// routes. It composes authentication (via authHandler) with authorization
// (via rbac.OrgProvider).
type rbacHandler struct {
	auth             *authHandler
	orgProvider      rbac.OrgProvider
	projectStore     project.Store     // optional: merges project store into project listing
	serviceHandler   *serviceHandler   // optional: enables POST .../services
	inventoryHandler *inventoryHandler // optional: enables inventory endpoints
	previewHandler   *previewHandler   // optional: enables preview endpoints
	promoteHandler   *promoteHandler   // optional: enables promote endpoint
}

// requireRole returns middleware that enforces authentication and checks that
// the session user has at least the given role for the project extracted from
// the request. Returns 401 for missing/invalid sessions and 403 for
// insufficient permissions.
func (rh *rbacHandler) requireRole(role rbac.Role, extractProject ProjectExtractor) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return rh.auth.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			sess := sessionFromContext(r.Context())

			org, err := rh.orgProvider.GetOrg(r.Context())
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
				return
			}

			project := extractProject(r)
			if !org.HasPermission(sess.Username, project, role) {
				writeJSON(w, http.StatusForbidden, errorResponse{Error: "insufficient permissions"})
				return
			}

			next(w, r)
		})
	}
}

func (rh *rbacHandler) registerRoutes(mux *http.ServeMux) {
	byProject := ProjectFromPathValue("project")

	viewProject := rh.requireRole(rbac.RoleViewer, byProject)
	manageProject := rh.requireRole(rbac.RoleProjectAdmin, byProject)
	devProject := rh.requireRole(rbac.RoleDeveloper, byProject)

	// Org-level read endpoints — authenticated users only.
	mux.HandleFunc("GET /api/v1/org", rh.auth.requireAuth(rh.handleGetOrg))
	mux.HandleFunc("GET /api/v1/teams", rh.auth.requireAuth(rh.handleGetTeams))
	mux.HandleFunc("GET /api/v1/projects", rh.auth.requireAuth(rh.handleGetProjects))

	// Project-scoped endpoints — role-based access.
	mux.HandleFunc("GET /api/v1/projects/{project}", viewProject(placeholderHandler))
	mux.HandleFunc("GET /api/v1/projects/{project}/rbac", viewProject(rh.handleGetProjectRBAC))
	mux.HandleFunc("PUT /api/v1/projects/{project}", manageProject(placeholderHandler))
	if rh.serviceHandler != nil {
		mux.HandleFunc("POST /api/v1/projects/{project}/services", devProject(rh.serviceHandler.handleCreateService))
	}
	if rh.inventoryHandler != nil {
		mux.HandleFunc("GET /api/v1/environments", rh.auth.requireAuth(rh.inventoryHandler.handleListEnvironments))
		mux.HandleFunc("GET /api/v1/projects/{project}/services", viewProject(rh.inventoryHandler.handleListServices))
		mux.HandleFunc("GET /api/v1/projects/{project}/services/{service}", viewProject(rh.inventoryHandler.handleGetService))
	}
	if rh.previewHandler != nil {
		mux.HandleFunc("GET /api/v1/previews", rh.auth.requireAuth(rh.previewHandler.handleListPreviews))
		mux.HandleFunc("POST /api/v1/previews", rh.auth.requireAuth(rh.previewHandler.handleCreatePreview))
		mux.HandleFunc("DELETE /api/v1/previews/{name}", rh.auth.requireAuth(rh.previewHandler.handleDeletePreview))
	}
	if rh.promoteHandler != nil {
		mux.HandleFunc("POST /api/v1/projects/{project}/services/{service}/promote", manageProject(rh.promoteHandler.handlePromote))
	}
}

// placeholderHandler returns 200 with a stub JSON body. It will be replaced
// once real project/service handlers are implemented.
func placeholderHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
