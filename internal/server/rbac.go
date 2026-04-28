package server

import (
	"net/http"

	"github.com/suparcloud/suparship/internal/domain"
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

// rbacHandler provides RBAC enforcement middleware and project/org routes.
// It composes authentication (via authHandler) with authorization
// (via rbac.OrgStore).
type rbacHandler struct {
	auth         *authHandler
	orgStore     rbac.OrgStore
	projectStore project.Store // optional: merges project store into project listing
	serviceHandler   *serviceHandler   // optional: enables POST .../services
	inventoryHandler *inventoryHandler // optional: enables inventory endpoints
	previewHandler   *previewHandler   // optional: enables preview endpoints
	promoteHandler   *promoteHandler   // optional: enables promote endpoint
	logsHandler      *logsHandler      // optional: enables logs endpoint
	appHandler       *appHandler       // optional: enables app read endpoints
	envConfigHandler *envConfigHandler // optional: enables env config endpoints
	secretsHandler   *secretsHandler   // optional: enables simple secret management
	// vaultItemWriter and appStore are optional — used to backfill vault items
	// when an env is first bound to a cluster (so existing apps get their items).
	vaultItemWriter VaultItemWriter  // optional: backfill vault items on env bind
	vaultAppStore   domain.AppStore // optional: list apps for backfill
}

// requireRole returns middleware that enforces authentication and checks that
// the session user has at least the given role for the project extracted from
// the request. Returns 401 for missing/invalid sessions and 403 for
// insufficient permissions.
func (rh *rbacHandler) requireRole(role rbac.Role, extractProject ProjectExtractor) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return rh.auth.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			sess := sessionFromContext(r.Context())

			org, err := rh.orgStore.GetOrg(r.Context())
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

	requireOrgAdmin := rh.auth.requireAuth // further RBAC check inside handler

	// Org-level read endpoints — authenticated users only.
	mux.HandleFunc("GET /api/v1/org", rh.auth.requireAuth(rh.handleGetOrg))
	mux.HandleFunc("GET /api/v1/teams", rh.auth.requireAuth(rh.handleGetTeams))
	mux.HandleFunc("GET /api/v1/projects", rh.auth.requireAuth(rh.handleGetProjects))

	// Project lifecycle — org_admin only.
	mux.HandleFunc("POST /api/v1/projects", rh.auth.requireAuth(rh.orgAdminOnly(rh.handleCreateProject)))
	mux.HandleFunc("DELETE /api/v1/projects/{project}", rh.auth.requireAuth(rh.orgAdminOnly(rh.handleDeleteProject)))

	// Org-level environment management (canonical pipeline definition).
	// Reads are open to all authenticated users; writes require org_admin.
	mux.HandleFunc("GET /api/v1/org/environments", rh.auth.requireAuth(rh.handleListOrgEnvironments))
	mux.HandleFunc("POST /api/v1/org/environments", requireOrgAdmin(rh.requireOrgAdmin(rh.handleCreateOrgEnvironment)))
	mux.HandleFunc("PUT /api/v1/org/environments/{env}", requireOrgAdmin(rh.requireOrgAdmin(rh.handleUpdateOrgEnvironment)))
	mux.HandleFunc("DELETE /api/v1/org/environments/{env}", requireOrgAdmin(rh.requireOrgAdmin(rh.handleDeleteOrgEnvironment)))

	// Org-level namespace naming patterns — reads for all; writes require org_admin.
	mux.HandleFunc("GET /api/v1/org/naming", rh.auth.requireAuth(rh.handleGetOrgNaming))
	mux.HandleFunc("PUT /api/v1/org/naming", requireOrgAdmin(rh.requireOrgAdmin(rh.handlePutOrgNaming)))

	// Project-scoped endpoints — role-based access.
	mux.HandleFunc("GET /api/v1/projects/{project}", viewProject(rh.handleGetProject))
	mux.HandleFunc("GET /api/v1/projects/{project}/rbac", viewProject(rh.handleGetProjectRBAC))
	mux.HandleFunc("PUT /api/v1/projects/{project}", manageProject(placeholderHandler))

	// Project environment management.
	mux.HandleFunc("GET /api/v1/projects/{project}/environments", viewProject(rh.handleListProjectEnvironments))
	mux.HandleFunc("POST /api/v1/projects/{project}/environments", manageProject(rh.handleCreateProjectEnvironment))
	mux.HandleFunc("PUT /api/v1/projects/{project}/environments/{env}", manageProject(rh.handleUpdateProjectEnvironment))
	mux.HandleFunc("DELETE /api/v1/projects/{project}/environments/{env}", manageProject(rh.handleDeleteProjectEnvironment))

	// Project namespace naming pattern.
	mux.HandleFunc("GET /api/v1/projects/{project}/naming", viewProject(rh.handleGetProjectNaming))
	mux.HandleFunc("PUT /api/v1/projects/{project}/naming", manageProject(rh.handlePutProjectNaming))
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

	if rh.envConfigHandler != nil {
		ec := rh.envConfigHandler
		// Org and Environment levels — org_admin writes, any-auth reads.
		mux.HandleFunc("GET /api/v1/org/envconfig", rh.auth.requireAuth(ec.handleGetOrgEnvConfig))
		mux.HandleFunc("PUT /api/v1/org/envconfig", requireOrgAdmin(rh.requireOrgAdmin(ec.handlePutOrgEnvConfig)))
		mux.HandleFunc("GET /api/v1/org/envconfig/{envtype}", rh.auth.requireAuth(ec.handleGetEnvTypeEnvConfig))
		mux.HandleFunc("PUT /api/v1/org/envconfig/{envtype}", requireOrgAdmin(rh.requireOrgAdmin(ec.handlePutEnvTypeEnvConfig)))
		// Project level — project_admin writes, viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/envconfig", viewProject(ec.handleGetProjectEnvConfig))
		mux.HandleFunc("PUT /api/v1/projects/{project}/envconfig", manageProject(ec.handlePutProjectEnvConfig))
		// Cluster level — org_admin writes, any-auth reads. Platform escape hatch.
		mux.HandleFunc("GET /api/v1/clusters/{cluster}/envconfig", rh.auth.requireAuth(ec.handleGetClusterEnvConfig))
		mux.HandleFunc("PUT /api/v1/clusters/{cluster}/envconfig", requireOrgAdmin(rh.requireOrgAdmin(ec.handlePutClusterEnvConfig)))
		// App level — developer writes (202 async), viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envconfig", viewProject(ec.handleGetAppEnvConfig))
		mux.HandleFunc("PUT /api/v1/projects/{project}/apps/{app}/envconfig", devProject(ec.handlePutAppEnvConfig))
		// App-Environment level — developer writes (202 async), viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig", viewProject(ec.handleGetAppEnvEnvConfig))
		mux.HandleFunc("PUT /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig", devProject(ec.handlePutAppEnvEnvConfig))
		// Resolved view — any viewer.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig/resolved", viewProject(ec.handleGetResolvedEnvConfig))
	}

	if rh.secretsHandler != nil {
		sh := rh.secretsHandler
		// Org-level secrets backend config — org_admin only.
		mux.HandleFunc("GET /api/v1/org/secrets-backend", rh.auth.requireAuth(sh.handleGetSecretsBackend))
		mux.HandleFunc("PUT /api/v1/org/secrets-backend", requireOrgAdmin(rh.requireOrgAdmin(sh.handlePutSecretsBackend)))
		// Full backend config (new schema).
		mux.HandleFunc("GET /api/v1/org/secret-backend", rh.auth.requireAuth(sh.handleGetSecretsBackendFull))
		mux.HandleFunc("PUT /api/v1/org/secret-backend", requireOrgAdmin(rh.requireOrgAdmin(sh.handlePutSecretsBackendFull)))
		// SA token, vault listing, and binding endpoints.
		mux.HandleFunc("POST /api/v1/org/secret-backend/sa-token", requireOrgAdmin(rh.requireOrgAdmin(sh.handlePostSAToken)))
		mux.HandleFunc("GET /api/v1/org/secret-backend/vaults", requireOrgAdmin(rh.requireOrgAdmin(sh.handleListVaults)))
		mux.HandleFunc("POST /api/v1/org/secret-backend/bindings", requireOrgAdmin(rh.requireOrgAdmin(sh.handleAddBinding)))
		mux.HandleFunc("DELETE /api/v1/org/secret-backend/bindings/{env}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleRemoveBinding)))
		// Platform-shared vault: operator picks the vault they created manually
		// in 1Password (SAs can't create vaults). Org and project secret
		// writes route here.
		mux.HandleFunc("PUT /api/v1/org/secret-backend/platform-vault", requireOrgAdmin(rh.requireOrgAdmin(sh.handleSetPlatformVault)))
		// One-shot migration: copy upper-level K8s Secrets into the 1Password
		// vaults after flipping the org backend. Idempotent.
		mux.HandleFunc("POST /api/v1/org/secret-backend/migrate-to-onepassword", requireOrgAdmin(rh.requireOrgAdmin(sh.handleMigrateToOnePassword)))
		// Org-level secrets CRUD — org_admin writes, any-auth reads.
		mux.HandleFunc("GET /api/v1/org/secrets", rh.auth.requireAuth(sh.handleListOrgSecrets))
		mux.HandleFunc("POST /api/v1/org/secrets", requireOrgAdmin(rh.requireOrgAdmin(sh.handleUpsertOrgSecrets)))
		mux.HandleFunc("DELETE /api/v1/org/secrets/{key}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleDeleteOrgSecret)))
		// Env-type-level secrets CRUD — org_admin writes, any-auth reads.
		mux.HandleFunc("GET /api/v1/org/secrets/envtype/{envtype}", rh.auth.requireAuth(sh.handleListEnvTypeSecrets))
		mux.HandleFunc("POST /api/v1/org/secrets/envtype/{envtype}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleUpsertEnvTypeSecrets)))
		mux.HandleFunc("DELETE /api/v1/org/secrets/envtype/{envtype}/{key}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleDeleteEnvTypeSecret)))
		// Project-level secrets CRUD — project_admin writes, viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/secrets", viewProject(sh.handleListProjectSecrets))
		mux.HandleFunc("POST /api/v1/projects/{project}/secrets", manageProject(sh.handleUpsertProjectSecrets))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/secrets/{key}", manageProject(sh.handleDeleteProjectSecret))
		// Cluster-level secrets CRUD — org_admin writes, any-auth reads.
		mux.HandleFunc("GET /api/v1/clusters/{cluster}/secrets", rh.auth.requireAuth(sh.handleListClusterSecrets))
		mux.HandleFunc("POST /api/v1/clusters/{cluster}/secrets", requireOrgAdmin(rh.requireOrgAdmin(sh.handleUpsertClusterSecrets)))
		mux.HandleFunc("DELETE /api/v1/clusters/{cluster}/secrets/{key}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleDeleteClusterSecret)))
		// App-level secrets CRUD — developer writes, viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/secrets", viewProject(sh.handleListAppSecrets))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/secrets", devProject(sh.handleUpsertAppSecrets))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}/secrets/{key}", devProject(sh.handleDeleteAppSecret))
		// App-env secret key/value CRUD — developer writes, viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envs/{env}/secrets", viewProject(sh.handleListSecrets))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/envs/{env}/secrets", devProject(sh.handleUpsertSecrets))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}/envs/{env}/secrets/{key}", devProject(sh.handleDeleteSecret))
		// Resolved secrets — merged view across all 5 levels.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envs/{env}/secrets/resolved", viewProject(sh.handleGetResolvedSecrets))
		// Force-sync: bumps ExternalSecret annotation to trigger ESO re-pull.
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/secrets/sync", devProject(sh.handleSecretSync))
	}

	if rh.appHandler != nil {
		mux.HandleFunc("GET /api/v1/projects/{project}/apps", viewProject(rh.appHandler.handleListApps))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}", viewProject(rh.appHandler.handleGetApp))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}", manageProject(rh.appHandler.handleDeleteApp))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/environments", viewProject(rh.appHandler.handleListAppEnvironments))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/environments/{env}", viewProject(rh.appHandler.handleGetAppEnvironment))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/previews", viewProject(rh.appHandler.handleListAppPreviews))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/previews", devProject(rh.appHandler.handleCreateAppPreview))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}/previews/{name}", devProject(rh.appHandler.handleDeleteAppPreview))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/promote", manageProject(rh.appHandler.handlePromoteApp))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/promotions/{name}", viewProject(rh.appHandler.handleGetKargoPromotion))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/kargo/stages", viewProject(rh.appHandler.handleGetKargoStages))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/environments/{env}/history", viewProject(rh.appHandler.handleGetAppDeploymentHistory))
		// Sync re-triggers the gitops publish for an existing app. Registered
		// unconditionally — returns 503 when publisher is not configured so the
		// UI can show a clear error rather than a 404.
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/sync", devProject(rh.appHandler.handleSyncApp))
		if rh.appHandler.logsProvider != nil {
			mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/logs", viewProject(rh.appHandler.handleGetAppLogs))
		}
		if rh.appHandler.projectStore != nil {
			mux.HandleFunc("POST /api/v1/projects/{project}/apps", devProject(rh.appHandler.handleCreateApp))
		}
	}
}

// requireOrgAdmin wraps a handler and enforces that the session user holds the
// org_admin role on the wildcard project ("*"). Used for org-level write operations.
func (rh *rbacHandler) requireOrgAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := sessionFromContext(r.Context())
		org, err := rh.orgStore.GetOrg(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
			return
		}
		if !org.HasPermission(sess.Username, "*", rbac.RoleOrgAdmin) {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "org_admin role required"})
			return
		}
		next(w, r)
	}
}

// placeholderHandler returns 200 with a stub JSON body. It will be replaced
// once a real project-update handler is implemented (PUT /api/v1/projects/{project}).
func placeholderHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
