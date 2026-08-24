package server

import (
	"net/http"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/audit"
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
	tokenHandler     *tokenHandler     // optional: enables project API token endpoints
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
	// auditor records project lifecycle events (create/delete). Defaults to a
	// Nop when unset.
	auditor audit.Auditor
	// authz decides role-based access. Optional: when nil the middleware falls
	// back to a default OrgAuthorizer over orgStore (see authorizer()), so the
	// core behaves identically. Enterprise builds inject a custom Authorizer.
	authz rbac.Authorizer
}

// authorizer returns the configured Authorizer, or a default OrgAuthorizer
// backed by orgStore when none was injected. This keeps the enforcement
// middleware behaviour-identical to the previous inline org permission check
// while allowing enterprise builds to override the policy.
func (rh *rbacHandler) authorizer() rbac.Authorizer {
	if rh.authz != nil {
		return rh.authz
	}
	return rbac.NewOrgAuthorizer(rh.orgStore)
}

// requireRole returns middleware that enforces authentication and checks that
// the session user has at least the given role for the project extracted from
// the request. Returns 401 for missing/invalid sessions and 403 for
// insufficient permissions.
func (rh *rbacHandler) requireRole(role rbac.Role, extractProject ProjectExtractor) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return rh.auth.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			project := extractProject(r)

			// API-token requests are authorized solely by the token's grant —
			// its fixed project and role — never by the minter's live org
			// membership. This keeps a token's authority stable and bounded.
			if tok := tokenFromContext(r.Context()); tok != nil {
				if tok.Project != project || rbac.RoleLevel(rbac.Role(tok.Role)) < rbac.RoleLevel(role) {
					writeJSON(w, http.StatusForbidden, errorResponse{Error: "insufficient permissions"})
					return
				}
				next(w, r)
				return
			}

			sess := sessionFromContext(r.Context())
			id := rbac.Identity{Username: sess.Username, Groups: sess.Groups}
			allowed, err := rh.authorizer().Authorize(r.Context(), id, project, role)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
				return
			}
			if !allowed {
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
	// Local users (basic-auth escape hatch, invite-provisioned) — org_admin only.
	mux.HandleFunc("GET /api/v1/org/users", requireOrgAdmin(rh.requireOrgAdmin(rh.handleListLocalUsers)))
	mux.HandleFunc("POST /api/v1/org/users", requireOrgAdmin(rh.requireOrgAdmin(rh.handleCreateLocalUser)))
	mux.HandleFunc("POST /api/v1/org/users/{username}/invite", requireOrgAdmin(rh.requireOrgAdmin(rh.handleReinviteLocalUser)))
	mux.HandleFunc("DELETE /api/v1/org/users/{username}", requireOrgAdmin(rh.requireOrgAdmin(rh.handleDeleteLocalUser)))

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

	// Org-level endpoint scheme (secure endpoints: https vs http on generated
	// app URLs) — reads for all; writes require org_admin.
	mux.HandleFunc("GET /api/v1/org/endpoints", rh.auth.requireAuth(rh.handleGetOrgEndpoints))
	mux.HandleFunc("PUT /api/v1/org/endpoints", requireOrgAdmin(rh.requireOrgAdmin(rh.handlePutOrgEndpoints)))

	// Org-level routing profiles (ingress class + cert-manager ClusterIssuer
	// per ExposeMode tier) — reads for all; writes require org_admin.
	mux.HandleFunc("GET /api/v1/org/routing-profiles", rh.auth.requireAuth(rh.handleListOrgRoutingProfiles))
	mux.HandleFunc("PUT /api/v1/org/routing-profiles/{name}", requireOrgAdmin(rh.requireOrgAdmin(rh.handlePutOrgRoutingProfile)))
	mux.HandleFunc("DELETE /api/v1/org/routing-profiles/{name}", requireOrgAdmin(rh.requireOrgAdmin(rh.handleDeleteOrgRoutingProfile)))


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
	if rh.appHandler != nil {
		// PR-grouped previews built from the real app-scoped preview environments:
		// one item per PR (preview name) with its per-app previews nested. Powers
		// the global Previews page (the legacy GET /previews is deprecated).
		mux.HandleFunc("GET /api/v1/preview-groups", rh.auth.requireAuth(rh.handleListPreviewGroups))
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
		mux.HandleFunc("GET /api/v1/projects/{project}/envconfig/env/{env}", viewProject(ec.handleGetProjectEnvEnvConfig))
		mux.HandleFunc("PUT /api/v1/projects/{project}/envconfig/env/{env}", manageProject(ec.handlePutProjectEnvEnvConfig))
		// Cluster level — org_admin writes, any-auth reads. Platform escape hatch.
		mux.HandleFunc("GET /api/v1/clusters/{cluster}/envconfig", requireOrgAdmin(rh.requireOrgAdmin(ec.handleGetClusterEnvConfig)))
		mux.HandleFunc("PUT /api/v1/clusters/{cluster}/envconfig", requireOrgAdmin(rh.requireOrgAdmin(ec.handlePutClusterEnvConfig)))
		// App level — developer writes (202 async), viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envconfig", viewProject(ec.handleGetAppEnvConfig))
		mux.HandleFunc("PUT /api/v1/projects/{project}/apps/{app}/envconfig", devProject(ec.handlePutAppEnvConfig))
		// App-Environment level — developer writes (202 async), viewer reads.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig", viewProject(ec.handleGetAppEnvEnvConfig))
		mux.HandleFunc("PUT /api/v1/projects/{project}/apps/{app}/envs/{env}/envconfig", devProject(ec.handlePutAppEnvEnvConfig))
		// Per-base-env preview band: env vars for previews whose base env is {env}.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/envs/{env}/preview-envconfig", viewProject(ec.handleGetAppPreviewEnvConfig))
		mux.HandleFunc("PUT /api/v1/projects/{project}/apps/{app}/envs/{env}/preview-envconfig", devProject(ec.handlePutAppPreviewEnvConfig))
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
		mux.HandleFunc("POST /api/v1/org/secret-backend/vault-token", requireOrgAdmin(rh.requireOrgAdmin(sh.handlePostVaultToken)))
		mux.HandleFunc("GET /api/v1/org/secret-backend/vaults", requireOrgAdmin(rh.requireOrgAdmin(sh.handleListVaults)))
		// Global vault: the 1Password vault holding global-scope items.
		mux.HandleFunc("PUT /api/v1/org/secret-backend/global-vault", requireOrgAdmin(rh.requireOrgAdmin(sh.handleSetGlobalVault)))
		// Env vault registration (vault ID only — cluster overrides live inside
		// env vaults, so there is no cluster vault registration).
		mux.HandleFunc("POST /api/v1/org/secret-backend/vaults/env/{env}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleRegisterEnvVault)))
		mux.HandleFunc("DELETE /api/v1/org/secret-backend/vaults/env/{env}", requireOrgAdmin(rh.requireOrgAdmin(sh.handleUnregisterEnvVault)))
		// Per-cluster Connect token: one token per cluster, covering the global
		// vault + the env vaults bound to that cluster; sealing publishes the
		// cluster's single unified ClusterSecretStore.
		mux.HandleFunc("POST /api/v1/org/secret-backend/clusters/{cluster}/connect-token", requireOrgAdmin(rh.requireOrgAdmin(sh.handleSetClusterConnectToken)))
		// Least-privilege Vault policy set: one read policy per scope prefix, and
		// per cluster the subset its env bindings entitle it to. Computed only —
		// the operator applies it against their own Vault.
		mux.HandleFunc("GET /api/v1/org/secret-backend/vault-policies", requireOrgAdmin(rh.requireOrgAdmin(sh.handleGetVaultPolicies)))

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

		// Preview band: app secrets applied to every preview on top of base env
		// {env}. Stored as the <app>-env-preview item inside the {env} vault.
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/secrets/env/{env}/preview", viewProject(sh.handleListSecrets))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/secrets/env/{env}/preview", devProject(sh.handleUpsertSecrets))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}/secrets/env/{env}/preview/{key}", devProject(sh.handleDeleteSecret))

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

		// Batch lifecycle (Phase 3): fan out over the stack's member apps.
		if rh.appHandler != nil {
			mux.HandleFunc("POST /api/v1/projects/{project}/stacks/{stack}/sync", manageProject(rh.handleSyncStack))
			mux.HandleFunc("POST /api/v1/projects/{project}/stacks/{stack}/promote", manageProject(rh.handlePromoteStack))
			mux.HandleFunc("POST /api/v1/projects/{project}/stacks/{stack}/clone", manageProject(rh.handleCloneStack))
			// Previews are developer-triggerable (CI-callable, once per PR for the
			// whole stack) — matching the per-app preview routes below.
			mux.HandleFunc("POST /api/v1/projects/{project}/stacks/{stack}/previews", devProject(rh.handleCreateStackPreview))
			mux.HandleFunc("DELETE /api/v1/projects/{project}/stacks/{stack}/previews/{name}", devProject(rh.handleDeleteStackPreview))
			// Pin/unpin a PR preview group to a stable env — matching the per-app
			// pin routes (manageProject).
			mux.HandleFunc("POST /api/v1/projects/{project}/stacks/{stack}/pin", manageProject(rh.handlePinStack))
			mux.HandleFunc("DELETE /api/v1/projects/{project}/stacks/{stack}/pin", manageProject(rh.handleUnpinStack))
			// Set per-env target clusters across the stack (same privilege as the
			// per-app PATCH that carries targetClusters).
			mux.HandleFunc("POST /api/v1/projects/{project}/stacks/{stack}/target-clusters", manageProject(rh.handleSetStackTargetClusters))
			// Suspend/resume are developer-triggerable (reversible, CI-callable).
			mux.HandleFunc("POST /api/v1/projects/{project}/stacks/{stack}/suspend", devProject(rh.handleSuspendStack))
			mux.HandleFunc("POST /api/v1/projects/{project}/stacks/{stack}/resume", devProject(rh.handleResumeStack))
		}

		// Stack-scope shared secrets (shared by every app in the stack).
		if rh.secretsHandler != nil {
			sh := rh.secretsHandler
			mux.HandleFunc("GET /api/v1/projects/{project}/stacks/{stack}/secrets/global", viewProject(sh.handleListSecrets))
			mux.HandleFunc("POST /api/v1/projects/{project}/stacks/{stack}/secrets/global", manageProject(sh.handleUpsertSecrets))
			mux.HandleFunc("DELETE /api/v1/projects/{project}/stacks/{stack}/secrets/global/{key}", manageProject(sh.handleDeleteSecret))
			mux.HandleFunc("GET /api/v1/projects/{project}/stacks/{stack}/secrets/env/{env}", viewProject(sh.handleListSecrets))
			mux.HandleFunc("POST /api/v1/projects/{project}/stacks/{stack}/secrets/env/{env}", manageProject(sh.handleUpsertSecrets))
			mux.HandleFunc("DELETE /api/v1/projects/{project}/stacks/{stack}/secrets/env/{env}/{key}", manageProject(sh.handleDeleteSecret))
		}
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
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/environments/{env}/undeploy", manageProject(rh.appHandler.handleUndeployAppEnv))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/environments/{env}/pin", manageProject(rh.appHandler.handlePinAppEnv))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/apps/{app}/environments/{env}/pin", manageProject(rh.appHandler.handleUnpinAppEnv))
		mux.HandleFunc("GET /api/v1/projects/{project}/apps/{app}/environments/{env}/rollback-candidates", viewProject(rh.appHandler.handleGetAppEnvRollbackCandidates))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/environments/{env}/rollback", manageProject(rh.appHandler.handleRollbackAppEnv))
		// Poll the result of an async (Prefer: respond-async) pin/unpin/preview.
		// Project-scoped read: same view privilege as any other project status read.
		// pin-tasks is a legacy alias kept so already-shipped CI keeps working.
		mux.HandleFunc("GET /api/v1/projects/{project}/tasks/{taskId}", viewProject(rh.handleGetTask))
		mux.HandleFunc("GET /api/v1/projects/{project}/pin-tasks/{taskId}", viewProject(rh.handleGetTask))
		// Suspend/resume an env (developer-triggerable, reversible, CI-callable).
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/environments/{env}/suspend", devProject(rh.appHandler.handleSuspendAppEnv))
		mux.HandleFunc("POST /api/v1/projects/{project}/apps/{app}/environments/{env}/resume", devProject(rh.appHandler.handleResumeAppEnv))
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

	// Project API tokens — minting/revoking a long-lived credential is
	// project_admin only. The tokens themselves authenticate as their granted
	// role (see requireRole's token branch).
	if rh.tokenHandler != nil {
		mux.HandleFunc("GET /api/v1/projects/{project}/tokens", manageProject(rh.tokenHandler.handleListTokens))
		mux.HandleFunc("POST /api/v1/projects/{project}/tokens", manageProject(rh.tokenHandler.handleCreateToken))
		mux.HandleFunc("DELETE /api/v1/projects/{project}/tokens/{id}", manageProject(rh.tokenHandler.handleDeleteToken))
	}
}

// requireOrgAdmin wraps a handler and enforces that the session user holds the
// org_admin role on the wildcard project ("*"). Used for org-level write operations.
func (rh *rbacHandler) requireOrgAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Project-scoped API tokens are never org admins, regardless of who
		// minted them.
		if tokenFromContext(r.Context()) != nil {
			writeJSON(w, http.StatusForbidden, errorResponse{Error: "org_admin role required"})
			return
		}
		sess := sessionFromContext(r.Context())
		id := rbac.Identity{Username: sess.Username, Groups: sess.Groups}
		allowed, err := rh.authorizer().Authorize(r.Context(), id, "*", rbac.RoleOrgAdmin)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
			return
		}
		if !allowed {
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
