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
	logsHandler      *logsHandler      // optional: enables logs endpoint
	appHandler       *appHandler       // optional: enables app read endpoints
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
	mux.HandleFunc("GET /api/v1/projects/{project}", viewProject(rh.handleGetProject))
	mux.HandleFunc("GET /api/v1/projects/{project}/rbac", viewProject(rh.handleGetProjectRBAC))
	mux.HandleFunc("PUT /api/v1/projects/{project}", manageProject(placeholderHandler))
	// --- Legacy service-oriented routes (compatibility) ---
	//
	// The routes below are deprecated. They are retained for backwards
	// compatibility and emit a "Deprecation: true" response header so API
	// clients can detect the migration signal.
	//
	// Migration guide: docs/migration-app-model.md
	//
	//   POST   /api/v1/projects/{project}/services          → POST   /api/v1/projects/{project}/apps
	//   GET    /api/v1/environments                         → GET    /api/v1/projects/{project}/apps/{app}/environments
	//   GET    /api/v1/projects/{project}/services          → GET    /api/v1/projects/{project}/apps
	//   GET    /api/v1/projects/{project}/services/{svc}    → GET    /api/v1/projects/{project}/apps/{app}
	//   GET    .../services/{svc}/previews                  → GET    .../apps/{app}/previews
	//   GET    /api/v1/previews                             → GET    .../apps/{app}/previews
	//   POST   /api/v1/previews                             → POST   .../apps/{app}/previews
	//   DELETE /api/v1/previews/{name}                      → DELETE .../apps/{app}/previews/{name}
	//   POST   .../services/{svc}/promote                   → POST   .../apps/{app}/promote
	//   GET    .../services/{svc}/logs                      → GET    .../apps/{app}/logs
	if rh.serviceHandler != nil {
		mux.HandleFunc("POST /api/v1/projects/{project}/services", devProject(legacyServiceRoute(rh.serviceHandler.handleCreateService)))
	}
	if rh.inventoryHandler != nil {
		mux.HandleFunc("GET /api/v1/environments", rh.auth.requireAuth(legacyServiceRoute(rh.inventoryHandler.handleListEnvironments)))
		mux.HandleFunc("GET /api/v1/projects/{project}/services", viewProject(legacyServiceRoute(rh.inventoryHandler.handleListServices)))
		mux.HandleFunc("GET /api/v1/projects/{project}/services/{service}", viewProject(legacyServiceRoute(rh.inventoryHandler.handleGetService)))
	}
	if rh.previewHandler != nil {
		mux.HandleFunc("GET /api/v1/previews", rh.auth.requireAuth(legacyServiceRoute(rh.previewHandler.handleListPreviews)))
		mux.HandleFunc("POST /api/v1/previews", rh.auth.requireAuth(legacyServiceRoute(rh.previewHandler.handleCreatePreview)))
		mux.HandleFunc("DELETE /api/v1/previews/{name}", rh.auth.requireAuth(legacyServiceRoute(rh.previewHandler.handleDeletePreview)))
		mux.HandleFunc("GET /api/v1/projects/{project}/services/{service}/previews", viewProject(legacyServiceRoute(rh.previewHandler.handleListServicePreviews)))
	}
	if rh.promoteHandler != nil {
		mux.HandleFunc("POST /api/v1/projects/{project}/services/{service}/promote", manageProject(legacyServiceRoute(rh.promoteHandler.handlePromote)))
	}
	if rh.logsHandler != nil {
		mux.HandleFunc("GET /api/v1/projects/{project}/services/{service}/logs", viewProject(legacyServiceRoute(rh.logsHandler.handleGetLogs)))
	}
	// --- End legacy service-oriented routes ---

	if rh.appHandler != nil {
		mux.HandleFunc("GET /api/v1/projects/{project}/apps", viewProject(rh.appHandler.handleListApps))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}", viewProject(rh.appHandler.handleGetApp))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/environments", viewProject(rh.appHandler.handleListAppEnvironments))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/environments/{env}", viewProject(rh.appHandler.handleGetAppEnvironment))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/previews", viewProject(rh.appHandler.handleListAppPreviews))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/previews", devProject(rh.appHandler.handleCreateAppPreview))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}/previews/{name}", devProject(rh.appHandler.handleDeleteAppPreview))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/promote", manageProject(rh.appHandler.handlePromoteApp))
		if rh.appHandler.logsProvider != nil {
			mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/logs", viewProject(rh.appHandler.handleGetAppLogs))
		}
		if rh.appHandler.projectStore != nil {
			mux.HandleFunc("POST /api/v1/projects/{project}/apps", devProject(rh.appHandler.handleCreateApp))
		}
	}
}

// placeholderHandler returns 200 with a stub JSON body. It will be replaced
// once a real project-update handler is implemented (PUT /api/v1/projects/{project}).
func placeholderHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
