package server

import (
	"net/http"

	"k8s.io/client-go/kubernetes"

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
	auth             *authHandler
	orgStore         rbac.OrgStore
	projectStore     project.Store     // optional: merges project store into project listing
	serviceHandler   *serviceHandler   // optional: enables POST .../services
	inventoryHandler *inventoryHandler // optional: enables inventory endpoints
	previewHandler   *previewHandler   // optional: enables preview endpoints
	promoteHandler   *promoteHandler   // optional: enables promote endpoint
	logsHandler      *logsHandler      // optional: enables logs endpoint
	appHandler       *appHandler       // optional: enables app read endpoints
	stackStore       domain.StackStore // optional: enables stack grouping endpoints
	envConfigHandler *envConfigHandler // optional: enables env config endpoints
	secretsHandler   *secretsHandler   // optional: enables simple secret management
	// storeReconciler republishes ESO ClusterSecretStores when an environment
	// is created/changed. Optional; nil disables the hook.
	storeReconciler SecretStoreReconciler
	// projectAppCounter sequences the two phases of project deletion: the
	// AppProject is removed only after the project's generated Applications
	// are pruned. Optional; nil falls back to a fixed grace delay.
	projectAppCounter ProjectAppCounter
	// stuckApps detects + unsticks ArgoCD Applications wedged in Terminating.
	// Optional; nil disables the platform stuck-apps endpoints.
	stuckApps StuckAppManager
	// kubeClient is used to upsert the OIDC client-secret Secret in the
	// suparship-system namespace (the org ConfigMap stays credential-free).
	// Optional; when nil the OIDC PUT rejects requests that carry a secret.
	kubeClient kubernetes.Interface
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
			if !org.HasPermissionForIdentity(sess.Username, sess.Groups, project, role) {
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
	mux.HandleFunc("GET /api/v1/teams", requireOrgAdmin(rh.requireOrgAdmin(rh.handleGetTeams)))
	mux.HandleFunc("GET /api/v1/projects", rh.auth.requireAuth(rh.handleGetProjects))

	// Team management — reads open to all authenticated users; writes org_admin.
	mux.HandleFunc("POST /api/v1/teams", requireOrgAdmin(rh.requireOrgAdmin(rh.handleCreateTeam)))
	mux.HandleFunc("PUT /api/v1/teams/{team}", requireOrgAdmin(rh.requireOrgAdmin(rh.handleUpdateTeam)))
	mux.HandleFunc("DELETE /api/v1/teams/{team}", requireOrgAdmin(rh.requireOrgAdmin(rh.handleDeleteTeam)))

	// Role bindings (team/group → role on a project, "*" = all). org_admin only.
	mux.HandleFunc("GET /api/v1/role-bindings", requireOrgAdmin(rh.requireOrgAdmin(rh.handleListRoleBindings)))
	mux.HandleFunc("POST /api/v1/role-bindings", requireOrgAdmin(rh.requireOrgAdmin(rh.handleCreateRoleBinding)))
	mux.HandleFunc("DELETE /api/v1/role-bindings", requireOrgAdmin(rh.requireOrgAdmin(rh.handleDeleteRoleBinding)))

	// OIDC SSO config — read open to all authenticated users (secret never
	// exposed); write org_admin. The client secret lives in a k8s Secret.
	mux.HandleFunc("GET /api/v1/org/auth", requireOrgAdmin(rh.requireOrgAdmin(rh.handleGetAuthConfig)))
	mux.HandleFunc("PUT /api/v1/org/auth", requireOrgAdmin(rh.requireOrgAdmin(rh.handlePutAuthConfig)))

	// Project lifecycle — org_admin only.
	mux.HandleFunc("POST /api/v1/projects", rh.auth.requireAuth(rh.orgAdminOnly(rh.handleCreateProject)))
	mux.HandleFunc("DELETE /api/v1/projects/{project}", rh.auth.requireAuth(rh.orgAdminOnly(rh.handleDeleteProject)))

	// Platform ops: detect + unstick ArgoCD Applications wedged in Terminating.
	// org_admin only (unstick mutates cluster state). Enabled when wired.
	if rh.stuckApps != nil {
		mux.HandleFunc("GET /api/v1/platform/stuck-apps", rh.auth.requireAuth(rh.orgAdminOnly(rh.handleListStuckApps)))
		mux.HandleFunc("POST /api/v1/platform/stuck-apps/{name}/unstick", rh.auth.requireAuth(rh.orgAdminOnly(rh.handleUnstickApp)))
	}

	// Org-level environment management (canonical pipeline definition).
	// Reads are open to all authenticated users; writes require org_admin.
	mux.HandleFunc("GET /api/v1/org/environments", rh.auth.requireAuth(rh.handleListOrgEnvironments))
	mux.HandleFunc("POST /api/v1/org/environments", requireOrgAdmin(rh.requireOrgAdmin(rh.handleCreateOrgEnvironment)))
	mux.HandleFunc("PUT /api/v1/org/environments/{env}", requireOrgAdmin(rh.requireOrgAdmin(rh.handleUpdateOrgEnvironment)))
	mux.HandleFunc("DELETE /api/v1/org/environments/{env}", requireOrgAdmin(rh.requireOrgAdmin(rh.handleDeleteOrgEnvironment)))

	// Org-level namespace naming patterns — reads for all; writes require org_admin.
	mux.HandleFunc("GET /api/v1/org/naming", rh.auth.requireAuth(rh.handleGetOrgNaming))
	mux.HandleFunc("PUT /api/v1/org/naming", requireOrgAdmin(rh.requireOrgAdmin(rh.handlePutOrgNaming)))

	// Org-level routing profiles (ingress class + cert-manager ClusterIssuer
	// per ExposeMode tier) — reads for all; writes require org_admin.
	mux.HandleFunc("GET /api/v1/org/routing-profiles", rh.auth.requireAuth(rh.handleListOrgRoutingProfiles))
	mux.HandleFunc("PUT /api/v1/org/routing-profiles/{name}", requireOrgAdmin(rh.requireOrgAdmin(rh.handlePutOrgRoutingProfile)))
	mux.HandleFunc("DELETE /api/v1/org/routing-profiles/{name}", requireOrgAdmin(rh.requireOrgAdmin(rh.handleDeleteOrgRoutingProfile)))

	// Org-level addon profiles (which wrapper chart + provider serves
	// each addon type). Reads for all; writes require org_admin.
	mux.HandleFunc("GET /api/v1/org/addon-profiles", rh.auth.requireAuth(rh.handleListOrgAddonProfiles))
	mux.HandleFunc("PUT /api/v1/org/addon-profiles/{type}", requireOrgAdmin(rh.requireOrgAdmin(rh.handlePutOrgAddonProfile)))
	mux.HandleFunc("DELETE /api/v1/org/addon-profiles/{type}", requireOrgAdmin(rh.requireOrgAdmin(rh.handleDeleteOrgAddonProfile)))

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
		mux.HandleFunc("GET /api/v1/org/envconfig", requireOrgAdmin(rh.requireOrgAdmin(ec.handleGetOrgEnvConfig)))
		mux.HandleFunc("PUT /api/v1/org/envconfig", requireOrgAdmin(rh.requireOrgAdmin(ec.handlePutOrgEnvConfig)))
		mux.HandleFunc("GET /api/v1/org/envconfig/{envtype}", requireOrgAdmin(rh.requireOrgAdmin(ec.handleGetEnvTypeEnvConfig)))
		mux.HandleFunc("PUT /api/v1/org/envconfig/{envtype}", requireOrgAdmin(rh.requireOrgAdmin(ec.handlePutEnvTypeEnvConfig)))
		// Project level — project_admin writes, viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/envconfig", viewProject(ec.handleGetProjectEnvConfig))
		mux.HandleFunc("PUT /api/v1/projects/{project}/envconfig", manageProject(ec.handlePutProjectEnvConfig))
		// Cluster level — org_admin writes, any-auth reads. Platform escape hatch.
		mux.HandleFunc("GET /api/v1/clusters/{cluster}/envconfig", requireOrgAdmin(rh.requireOrgAdmin(ec.handleGetClusterEnvConfig)))
		mux.HandleFunc("PUT /api/v1/clusters/{cluster}/envconfig", requireOrgAdmin(rh.requireOrgAdmin(ec.handlePutClusterEnvConfig)))
		// App level — developer writes (202 async), viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envconfig", viewProject(ec.handleGetAppEnvConfig))
		mux.HandleFunc("PUT /api/v1/projects/{project}/apps/{app}/envconfig", devProject(ec.handlePutAppEnvConfig))
		// App-Environment level — developer writes (202 async), viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig", viewProject(ec.handleGetAppEnvEnvConfig))
		mux.HandleFunc("PUT /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig", devProject(ec.handlePutAppEnvEnvConfig))
		// Resolved view — any viewer.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig/resolved", viewProject(ec.handleGetResolvedEnvConfig))
		// Variable catalog for the UI picker — any viewer.
		mux.HandleFunc("GET /api/v1/projects/{project}/config-variables", viewProject(ec.handleGetConfigVariables))
		// Project-agnostic catalog (platform tokens + org/env/cluster vars) for the
		// template-level platform-overrides editor, which has no project context.
		mux.HandleFunc("GET /api/v1/platform/config-variables", rh.auth.requireAuth(ec.handleGetPlatformConfigVariables))
	}

	if rh.secretsHandler != nil {
		sh := rh.secretsHandler
		// Org-level secrets backend config — org_admin only.
		mux.HandleFunc("GET /api/v1/org/secrets-backend", rh.auth.requireAuth(sh.handleGetSecretsBackend))
		mux.HandleFunc("PUT /api/v1/org/secrets-backend", requireOrgAdmin(rh.requireOrgAdmin(sh.handlePutSecretsBackend)))
		// Full backend config (new schema).
		mux.HandleFunc("GET /api/v1/org/secret-backend", requireOrgAdmin(rh.requireOrgAdmin(sh.handleGetSecretsBackendFull)))
		mux.HandleFunc("PUT /api/v1/org/secret-backend", requireOrgAdmin(rh.requireOrgAdmin(sh.handlePutSecretsBackendFull)))
		// SA token + vault listing.
		mux.HandleFunc("POST /api/v1/org/secret-backend/sa-token", requireOrgAdmin(rh.requireOrgAdmin(sh.handlePostSAToken)))
		mux.HandleFunc("GET /api/v1/org/secret-backend/vaults", requireOrgAdmin(rh.requireOrgAdmin(sh.handleListVaults)))
		// Global vault: the 1Password vault holding global-scope items.
		mux.HandleFunc("PUT /api/v1/org/secret-backend/global-vault", requireOrgAdmin(rh.requireOrgAdmin(sh.handleSetGlobalVault)))
		// Env vault registration (vault ID only — cluster overrides live inside
		// env vaults, so there is no cluster vault registration).
		mux.HandleFunc("POST /api/v1/org/secret-backend/vaults/env/{env}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleRegisterEnvVault)))
		// Per-cluster Connect token: one token per cluster, covering the global
		// vault + the env vaults bound to that cluster; sealing publishes the
		// cluster's single unified ClusterSecretStore.
		mux.HandleFunc("POST /api/v1/org/secret-backend/clusters/{cluster}/connect-token", requireOrgAdmin(rh.requireOrgAdmin(sh.handleSetClusterConnectToken)))

		// ── Shared-tier secrets (org-admin) across the 3 scopes ──
		// Cluster scope is per-(env, cluster): routes nest cluster under env.
		mux.HandleFunc("GET /api/v1/org/secrets/global", requireOrgAdmin(rh.requireOrgAdmin(sh.handleListSecrets)))
		mux.HandleFunc("POST /api/v1/org/secrets/global", requireOrgAdmin(rh.requireOrgAdmin(sh.handleUpsertSecrets)))
		mux.HandleFunc("DELETE /api/v1/org/secrets/global/{key}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleDeleteSecret)))
		mux.HandleFunc("GET /api/v1/org/secrets/env/{env}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleListSecrets)))
		mux.HandleFunc("POST /api/v1/org/secrets/env/{env}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleUpsertSecrets)))
		mux.HandleFunc("DELETE /api/v1/org/secrets/env/{env}/{key}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleDeleteSecret)))
		mux.HandleFunc("GET /api/v1/org/secrets/env/{env}/cluster/{cluster}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleListSecrets)))
		mux.HandleFunc("POST /api/v1/org/secrets/env/{env}/cluster/{cluster}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleUpsertSecrets)))
		mux.HandleFunc("DELETE /api/v1/org/secrets/env/{env}/cluster/{cluster}/{key}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleDeleteSecret)))

		// ── Project-scope shared secrets (shared by every app in the project) ──
		// Global (all envs) + per-env. Project-scope items live in the org
		// global / env vaults; scopeFromPath returns the project scope because
		// {project} is set without {app}.
		mux.HandleFunc("GET /api/v1/projects/{project}/secrets/global", viewProject(sh.handleListSecrets))
		mux.HandleFunc("POST /api/v1/projects/{project}/secrets/global", manageProject(sh.handleUpsertSecrets))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/secrets/global/{key}", manageProject(sh.handleDeleteSecret))
		mux.HandleFunc("GET /api/v1/projects/{project}/secrets/env/{env}", viewProject(sh.handleListSecrets))
		mux.HandleFunc("POST /api/v1/projects/{project}/secrets/env/{env}", manageProject(sh.handleUpsertSecrets))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/secrets/env/{env}/{key}", manageProject(sh.handleDeleteSecret))

		// ── App-tier secrets (project devs) across the 3 scopes ──
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/secrets/global", viewProject(sh.handleListSecrets))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/secrets/global", devProject(sh.handleUpsertSecrets))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}/secrets/global/{key}", devProject(sh.handleDeleteSecret))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/secrets/env/{env}", viewProject(sh.handleListSecrets))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/secrets/env/{env}", devProject(sh.handleUpsertSecrets))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}/secrets/env/{env}/{key}", devProject(sh.handleDeleteSecret))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/secrets/env/{env}/cluster/{cluster}", viewProject(sh.handleListSecrets))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/secrets/env/{env}/cluster/{cluster}", devProject(sh.handleUpsertSecrets))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}/secrets/env/{env}/cluster/{cluster}/{key}", devProject(sh.handleDeleteSecret))

		// Resolved secrets — merged view (global<env<cluster, shared<app).
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envs/{env}/secrets/resolved", viewProject(sh.handleGetResolvedSecrets))
		// Force-sync: triggers ESO re-pull.
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/secrets/sync", devProject(sh.handleSecretSync))
	}

	// ── Stacks (logical grouping of apps within a project) ──
	if rh.stackStore != nil {
		mux.HandleFunc("GET /api/v1/projects/{project}/stacks", viewProject(rh.handleListStacks))
		mux.HandleFunc("POST /api/v1/projects/{project}/stacks", manageProject(rh.handleCreateStack))
		mux.HandleFunc("GET /api/v1/projects/{project}/stacks/{stack}", viewProject(rh.handleGetStack))
		mux.HandleFunc("PATCH /api/v1/projects/{project}/stacks/{stack}", manageProject(rh.handlePatchStack))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/stacks/{stack}", manageProject(rh.handleDeleteStack))
	}

	if rh.appHandler != nil {
		mux.HandleFunc("GET /api/v1/projects/{project}/apps", viewProject(rh.appHandler.handleListApps))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}", viewProject(rh.appHandler.handleGetApp))
		mux.HandleFunc("PATCH /api/v1/projects/{project}/apps/{app}", manageProject(rh.appHandler.handleUpdateApp))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}", manageProject(rh.appHandler.handleDeleteApp))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/rename", manageProject(rh.appHandler.handleRenameApp))
		if rh.stackStore != nil {
			mux.HandleFunc("PUT /api/v1/projects/{project}/apps/{app}/stack", manageProject(rh.handleSetAppStack))
		}
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/environments", viewProject(rh.appHandler.handleListAppEnvironments))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/environments/{env}", viewProject(rh.appHandler.handleGetAppEnvironment))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/previews", viewProject(rh.appHandler.handleListAppPreviews))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/previews", devProject(rh.appHandler.handleCreateAppPreview))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}/previews/{name}", devProject(rh.appHandler.handleDeleteAppPreview))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/promote", manageProject(rh.appHandler.handlePromoteApp))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/promotions/{name}", viewProject(rh.appHandler.handleGetKargoPromotion))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/kargo/stages", viewProject(rh.appHandler.handleGetKargoStages))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/environments/{env}/history", viewProject(rh.appHandler.handleGetAppDeploymentHistory))
		// Read-only effective-values preview for the values editor (computes the
		// merged chart⊕platform⊕overrides document for an env; never mutates).
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/envs/{env}/values/preview", viewProject(rh.appHandler.handleAppValuesPreview))
		// Sync re-triggers the gitops publish for an existing app. Registered
		// unconditionally — returns 503 when publisher is not configured so the
		// UI can show a clear error rather than a 404.
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/sync", devProject(rh.appHandler.handleSyncApp))
		// Pin an app to a different template version + re-publish. Mirrors
		// /sync's "manage" tier — bumping the chart bytes Argo deploys is a
		// publishable change, not a read-only one.
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/upgrade-template", manageProject(rh.appHandler.handleUpgradeAppTemplate))
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
		if !org.HasPermissionForIdentity(sess.Username, sess.Groups, "*", rbac.RoleOrgAdmin) {
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
