package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"

	domainapp "github.com/suparcloud/suparship/internal/app"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/tpl"
)

// appHandler serves app-oriented API endpoints. It is wired into the
// rbacHandler's route registration so that RBAC middleware is applied.
//
// Read-only routes (list, get, environments) are always registered when
// appHandler is non-nil. The create route is additionally registered when
// projectStore is non-nil. The logs route is registered when logsProvider
// is non-nil.
type appHandler struct {
	appStore        domain.AppStore
	templateIdx     map[string]*tpl.Template
	projectStore    project.Store
	orgProvider     rbac.OrgProvider     // optional: provides org env fallback for sync
	runtimeProvider runtime.Provider     // optional: enriches env responses with live K8s status
	logsProvider    runtime.LogsProvider // optional: enables GET .../apps/{app}/logs
	gitOpsPublisher   GitOpsPublisher      // optional: commits argocd manifests to gitops repo on create
	kargoPromoter     KargoPromoter        // optional: creates Kargo Promotion CRs on promote
	kargoStatusReader KargoStatusReader    // optional: reads live Kargo Promotion status
	kargoPipelineReader KargoPipelineReader // optional: reads live Kargo Stage pipeline status
	deploymentHistoryReader DeploymentHistoryReader // optional: reads ArgoCD sync history
}

// newAppHandler creates an appHandler.
//
// templates and projectStore are optional. Passing non-nil values enables the
// POST /api/v1/projects/{project}/apps creation endpoint; the caller is
// responsible for registering the route only when both are present (see
// rbacHandler.registerRoutes).
func newAppHandler(store domain.AppStore, templates []*tpl.Template, projectStore project.Store) *appHandler {
	idx := make(map[string]*tpl.Template, len(templates))
	for _, t := range templates {
		idx[t.Metadata.Name] = t
	}
	return &appHandler{
		appStore:     store,
		templateIdx:  idx,
		projectStore: projectStore,
	}
}

// handleCreateApp handles POST /api/v1/projects/{project}/apps.
//
// It validates the request body, resolves the referenced template, checks for
// duplicate app names, and delegates the full creation pipeline (input
// validation, component initialisation, AppSpec assembly, Helm value
// generation) to domainapp.Create. The generated Helm values are logged here
// for audit purposes; GitOps commit integration is a follow-up step.
func (ah *appHandler) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")

	var req createAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if err := domain.ValidateAppName(req.Name); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if req.Template == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "template name is required"})
		return
	}

	tmpl, ok := ah.templateIdx[req.Template]
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "template \"" + req.Template + "\" not found",
		})
		return
	}

	// Verify the project exists before attempting any writes.
	if _, err := ah.projectStore.Get(r.Context(), projectName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "project \"" + projectName + "\" not found",
		})
		return
	}

	// Reject duplicate app names within the same project.
	if _, err := ah.appStore.GetApp(r.Context(), projectName, req.Name); err == nil {
		writeJSON(w, http.StatusConflict, errorResponse{
			Error: "app \"" + req.Name + "\" already exists in project \"" + projectName + "\"",
		})
		return
	}

	// Convert secret refs from DTO to domain type for the Create pipeline.
	domainSecretRefs := make([]domain.AppSecretRef, len(req.SecretRefs))
	for i, s := range req.SecretRefs {
		domainSecretRefs[i] = domain.AppSecretRef{Name: s.Name, SecretRef: s.SecretRef}
	}

	// Build explicit component specs when the caller provides them (legacy
	// path). When absent, Create initialises components from the template.
	var explicitComponents []domain.ComponentSpec
	for i, c := range req.Components {
		ct, err := domain.ParseComponentType(c.Type)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
				Error: "components[" + itoa(i) + "]: " + err.Error(),
			})
			return
		}
		explicitComponents = append(explicitComponents, domain.ComponentSpec{
			Name:           c.Name,
			Type:           ct,
			Enabled:        c.Enabled,
			Expose:         c.Expose,
			PreviewEnabled: c.PreviewEnabled,
		})
	}
	if len(explicitComponents) > 0 {
		if err := domain.ValidateComponents(explicitComponents); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
	}

	values := req.Values
	if values == nil {
		values = map[string]any{}
	}

	result, err := domainapp.Create(domainapp.CreateRequest{
		ProjectName:        projectName,
		AppName:            req.Name,
		DisplayName:        req.DisplayName,
		Description:        req.Description,
		Template:           tmpl,
		Values:             values,
		SecretRefs:         domainSecretRefs,
		ComponentToggles:   req.ComponentToggles,
		ExplicitComponents: explicitComponents,
		NamespaceScope:     domain.NamespaceScope(req.NamespaceScope),
		NamespacePattern:   req.NamespacePattern,
	})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	// Verify at least one environment is registered in the org before creating
	// the app. Deploying to unregistered environments silently would produce
	// orphaned GitOps manifests pointing at clusters that don't exist.
	if ah.orgProvider != nil {
		org, orgErr := ah.orgProvider.GetOrg(r.Context())
		if orgErr != nil || org == nil || len(org.Environments) == 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: "no environments registered in the org; register at least one via POST /api/v1/org/environments before creating apps",
			})
			return
		}
	}

	// Replace the hardcoded staging+prod environments produced by DefaultEnvironments
	// with the actual environments registered in the org config. This ensures only
	// environments the operator has explicitly defined are created and published.
	// Falls back to the hardcoded defaults when no org provider is configured.
	result.Environments = ah.stableEnvsFromOrg(r.Context(), result.App)

	if err := ah.appStore.SaveApp(r.Context(), projectName, result.App); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save app"})
		return
	}
	for _, env := range result.Environments {
		if err := ah.appStore.SaveAppEnvironment(r.Context(), projectName, env); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save app environment"})
			return
		}
	}

	// Commit ArgoCD Application manifests to the gitops repository so that
	// ArgoCD picks up the new app automatically. This is a best-effort step:
	// a failure here does not roll back the store writes — the app is already
	// persisted and the operator can re-trigger the gitops publish separately.
	if ah.gitOpsPublisher != nil {
		slog.Info("publishing app to gitops repo",
			"project", projectName,
			"app", req.Name,
			"envs", len(result.Environments),
		)
		if err := ah.gitOpsPublisher.PublishApp(r.Context(), result.App, result.Environments); err != nil {
			slog.Error("gitops publish failed — app saved to store but not committed to git",
				"project", projectName,
				"app", req.Name,
				"error", err,
			)
		} else {
			slog.Info("app published to gitops repo — ArgoCD will sync shortly",
				"project", projectName,
				"app", req.Name,
			)
		}
	} else {
		slog.Debug("gitops publisher not configured — skipping git commit for app",
			"project", projectName,
			"app", req.Name,
		)
	}

	// Re-read from store to produce a canonical response.
	saved, _ := ah.appStore.GetApp(r.Context(), projectName, req.Name)
	savedEnvs, _ := ah.appStore.ListAppEnvironments(r.Context(), projectName, req.Name)

	writeJSON(w, http.StatusCreated, createAppResponse{
		App: appToDetailDTO(saved, savedEnvs),
	})
}

// handleDeleteApp handles DELETE /api/v1/projects/{project}/apps/{app}.
//
// It deletes the app and all its environment instances from the store, then
// removes the corresponding GitOps manifests from the repository. The GitOps
// removal is best-effort — a failure there does not roll back the store delete.
func (ah *appHandler) handleDeleteApp(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	if _, err := ah.appStore.GetApp(r.Context(), projectName, appName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	if err := ah.appStore.DeleteApp(r.Context(), projectName, appName); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete app"})
		return
	}

	if ah.gitOpsPublisher != nil {
		if err := ah.gitOpsPublisher.UnpublishApp(r.Context(), projectName, appName); err != nil {
			slog.Error("gitops unpublish failed — app deleted from store but git not updated",
				"project", projectName, "app", appName, "error", err,
			)
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

// itoa converts a small non-negative int to its decimal string representation
// without importing strconv (avoids an import just for error messages).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// handleListApps handles GET /api/v1/projects/{project}/apps.
// Returns 404 when the project does not exist.
func (ah *appHandler) handleListApps(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")

	apps, err := ah.appStore.ListApps(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "project \"" + projectName + "\" not found",
		})
		return
	}

	dtos := make([]AppSummaryDTO, 0, len(apps))
	for _, app := range apps {
		envs, _ := ah.appStore.ListAppEnvironments(r.Context(), projectName, app.Name)
		for _, env := range envs {
			ah.enrichEnvWithLiveStatus(r.Context(), app.Name, env)
		}
		dtos = append(dtos, appToSummaryDTO(app, envs))
	}

	writeJSON(w, http.StatusOK, AppListResponse{
		Project: projectName,
		Apps:    dtos,
	})
}

// handleGetApp handles GET /api/v1/projects/{project}/apps/{app}.
// Returns 404 when the project or app does not exist.
func (ah *appHandler) handleGetApp(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	app, err := ah.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	envs, _ := ah.appStore.ListAppEnvironments(r.Context(), projectName, appName)
	for _, env := range envs {
		ah.enrichEnvWithLiveStatus(r.Context(), appName, env)
	}

	writeJSON(w, http.StatusOK, AppDetailResponse{
		App: appToDetailDTO(app, envs),
	})
}

// handleListAppEnvironments handles GET /api/v1/projects/{project}/apps/{app}/environments.
// Verifies the app exists before listing its environments; returns 404 otherwise.
func (ah *appHandler) handleListAppEnvironments(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	if _, err := ah.appStore.GetApp(r.Context(), projectName, appName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	envs, err := ah.appStore.ListAppEnvironments(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list environments"})
		return
	}

	for _, env := range envs {
		ah.enrichEnvWithLiveStatus(r.Context(), appName, env)
	}

	dtos := make([]AppEnvironmentSummaryDTO, 0, len(envs))
	for _, env := range envs {
		dtos = append(dtos, appEnvToDTO(env))
	}

	writeJSON(w, http.StatusOK, AppEnvironmentsResponse{
		Project:      projectName,
		AppName:      appName,
		Environments: dtos,
	})
}

// handleGetAppEnvironment handles GET /api/v1/projects/{project}/apps/{app}/environments/{env}.
// Returns 404 when the project, app, or environment does not exist.
func (ah *appHandler) handleGetAppEnvironment(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	env, err := ah.appStore.GetAppEnvironment(r.Context(), projectName, appName, envName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "environment \"" + envName + "\" not found for app \"" + appName + "\" in project \"" + projectName + "\"",
		})
		return
	}

	ah.enrichEnvWithLiveStatus(r.Context(), appName, env)

	writeJSON(w, http.StatusOK, AppEnvironmentResponse{
		Environment: appEnvToDTO(env),
	})
}

// handleListAppPreviews handles GET /api/v1/projects/{project}/apps/{app}/previews.
// Verifies the app exists before listing; returns 404 otherwise.
func (ah *appHandler) handleListAppPreviews(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	if _, err := ah.appStore.GetApp(r.Context(), projectName, appName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	previews, err := ah.appStore.ListAppPreviews(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list previews"})
		return
	}

	dtos := make([]AppPreviewSummaryDTO, 0, len(previews))
	for _, env := range previews {
		dtos = append(dtos, appPreviewToDTO(env))
	}

	writeJSON(w, http.StatusOK, AppPreviewsResponse{
		Project:  projectName,
		AppName:  appName,
		Previews: dtos,
	})
}

// handleCreateAppPreview handles POST /api/v1/projects/{project}/apps/{app}/previews.
//
// The raw preview name from the request body is sanitized via
// domain.SanitizePreviewName (deterministic, branch-name-friendly) before
// validation and storage. Only apps with at least one preview-enabled component
// may create previews; the handler returns 422 otherwise.
//
// Internally, the handler delegates to domainapp.CreatePreview which builds
// the EnvironmentInstance, generates Helm values (respecting preview_enabled
// components), and generates the ArgoCD Application manifest as a pure
// function. Persistence to the AppStore is handled by projecting the
// EnvironmentInstance back onto an AppEnvironment (compat layer).
func (ah *appHandler) handleCreateAppPreview(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	var req CreateAppPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "preview name is required"})
		return
	}

	sanitized := domain.SanitizePreviewName(req.Name)
	if err := domain.ValidatePreviewName(sanitized); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid preview name: " + err.Error()})
		return
	}

	a, err := ah.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	// Run the full preview creation pipeline: EnvironmentInstance + Helm values
	// + ArgoCD Application, respecting preview_enabled components.
	previewResult, err := domainapp.CreatePreview(domainapp.PreviewRequest{
		App:         a,
		PreviewName: sanitized,
	})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	// Reject duplicate preview names within the same app.
	if _, err := ah.appStore.GetAppEnvironment(r.Context(), projectName, appName, sanitized); err == nil {
		writeJSON(w, http.StatusConflict, errorResponse{
			Error: "preview \"" + sanitized + "\" already exists for app \"" + appName + "\"",
		})
		return
	}

	// Project the EnvironmentInstance onto AppEnvironment for the compat store.
	inst := previewResult.Instance
	env := &domain.AppEnvironment{
		AppName:     inst.AppName,
		ProjectName: inst.ProjectName,
		EnvName:     inst.EnvName,
		EnvType:     inst.EnvType,
		Namespace:   inst.Namespace,
		URLs:        []string{inst.URL},
		Status:      inst.Status,
	}

	if err := ah.appStore.SaveAppEnvironment(r.Context(), projectName, env); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save preview"})
		return
	}

	writeJSON(w, http.StatusCreated, appPreviewToDTO(env))
}

// handleDeleteAppPreview handles DELETE /api/v1/projects/{project}/apps/{app}/previews/{name}.
// Returns 404 when the preview does not exist and 400 if the named environment
// is not a preview (guards against accidental deletion of stable environments).
func (ah *appHandler) handleDeleteAppPreview(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	previewName := r.PathValue("name")

	env, err := ah.appStore.GetAppEnvironment(r.Context(), projectName, appName, previewName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "preview \"" + previewName + "\" not found for app \"" + appName + "\"",
		})
		return
	}
	if env.EnvType != domain.AppEnvPreview {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "environment \"" + previewName + "\" is not a preview environment",
		})
		return
	}

	if err := ah.appStore.DeleteAppEnvironment(r.Context(), projectName, appName, previewName); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete preview"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleSyncApp handles POST /api/v1/projects/{project}/apps/{app}/sync.
//
// It re-runs the gitops publish pipeline for an existing app — useful to
// recover apps that were created before the gitops publisher was configured,
// or that failed to push during creation due to a transient error.
//
// Only stable environments (staging, prod) are synced; preview environments
// are intentionally excluded since they have their own lifecycle.
//
// Returns 503 when the gitops publisher is not configured, 404 when the app
// does not exist, and 500 when the publish step fails.
func (ah *appHandler) handleSyncApp(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	if ah.gitOpsPublisher == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{
			Error: "gitops publisher is not configured on this server — set SUPARSHIP_GITOPS_REPO_URL to enable",
		})
		return
	}

	app, err := ah.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	allEnvs, err := ah.appStore.ListAppEnvironments(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list app environments"})
		return
	}

	// Only publish stable environments — preview envs have their own lifecycle.
	var stableEnvs []*domain.AppEnvironment
	for _, env := range allEnvs {
		if env.EnvType != domain.AppEnvPreview {
			stableEnvs = append(stableEnvs, env)
		}
	}
	if len(stableEnvs) == 0 {
		// No stable environments found in the store. This happens for apps that
		// were seeded from the legacy project.Service model (before the domain
		// AppEnvironment model was introduced) or for apps whose environments
		// have not been persisted yet.
		//
		// Fall back to synthesising environments from the org config so that
		// the gitops publish step always writes at least staging and prod entries.
		stableEnvs = ah.stableEnvsFromOrg(r.Context(), app)
	}

	// Re-resolve namespaces using the current org settings so that stale
	// namespace values (e.g. from apps created before the namespace pattern
	// feature existed) are corrected before publishing. This also persists
	// the corrected namespaces back to the store for future reads.
	ah.resolveEnvNamespaces(r.Context(), app, stableEnvs)
	for _, env := range stableEnvs {
		if err := ah.appStore.SaveAppEnvironment(r.Context(), app.ProjectName, env); err != nil {
			slog.Warn("sync: failed to persist corrected namespace — publishing with in-memory value",
				"project", app.ProjectName,
				"app", app.Name,
				"env", env.EnvName,
				"namespace", env.Namespace,
				"error", err,
			)
		}
	}

	slog.Info("syncing app to gitops repo",
		"project", projectName,
		"app", appName,
		"envs", len(stableEnvs),
	)
	if err := ah.gitOpsPublisher.PublishApp(r.Context(), app, stableEnvs); err != nil {
		slog.Error("gitops sync failed",
			"project", projectName,
			"app", appName,
			"error", err,
		)
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Error: "gitops sync failed: " + err.Error(),
		})
		return
	}

	slog.Info("app synced to gitops repo — ArgoCD will sync shortly",
		"project", projectName,
		"app", appName,
	)
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "app synced to gitops repo — ArgoCD will pick it up shortly",
		"project": projectName,
		"app":     appName,
	})
}

// resolveEnvNamespaces overwrites each environment's Namespace field with the
// value produced by domain.ResolveNamespace, which honours the org-level
// ResourceNaming patterns and cluster topology. Called after domainapp.Create
// so that both persisted data and the GitOps commit use the operator-configured
// namespace pattern rather than the hardcoded fallback.
//
// If orgProvider is nil or the org cannot be loaded, the function is a no-op
// (caller's existing Namespace values are preserved).
func (ah *appHandler) resolveEnvNamespaces(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) {
	if ah.orgProvider == nil {
		return
	}
	org, err := ah.orgProvider.GetOrg(ctx)
	if err != nil || org == nil {
		return
	}

	// Determine whether stable environments each run on their own cluster so
	// the {env} token can be omitted from namespace names for uniqueness.
	stableRefs := make([]string, 0, len(org.Environments))
	for _, e := range org.Environments {
		stableRefs = append(stableRefs, e.ClusterRef)
	}
	dedicated := domain.IsDedicatedClusterTopology(stableRefs)

	// Per-environment namespace pattern overrides defined at the org level.
	orgEnvPatterns := make(map[string]string, len(org.Environments))
	for _, e := range org.Environments {
		if e.NamespacePattern != "" {
			orgEnvPatterns[e.Name] = e.NamespacePattern
		}
	}

	for _, env := range envs {
		ns, resolveErr := domain.ResolveNamespace(domain.NamespaceResolveInput{
			AppName:           app.Name,
			EnvName:           env.EnvName,
			ProjectName:       app.ProjectName,
			OrgName:           org.Name,
			Scope:             app.Spec.NamespaceScope,
			Dedicated:         dedicated,
			AppPattern:        app.Spec.NamespacePattern,
			OrgEnvPattern:     orgEnvPatterns[env.EnvName],
			OrgAppDefault:     org.ResourceNaming.EffectiveAppNamespace(),
			OrgProjectDefault: org.ResourceNaming.EffectiveProjectNamespace(),
		})
		if resolveErr == nil {
			env.Namespace = ns
		}
	}
}

// stableEnvsFromOrg synthesises a minimal set of stable AppEnvironments from
// the org-level environment definitions. Used as a fallback in handleSyncApp
// when the app's environments have not been persisted to the store (e.g. for
// legacy apps seeded from the project.Service model).
//
// Environments are sorted by their org Order (then Name for determinism) so
// the resulting slice is in pipeline order. The Order field is propagated from
// the org definition into each AppEnvironment.
func (ah *appHandler) stableEnvsFromOrg(ctx context.Context, app *domain.App) []*domain.AppEnvironment {
	if ah.orgProvider == nil {
		return domainapp.DefaultEnvironments(app)
	}

	org, err := ah.orgProvider.GetOrg(ctx)
	if err != nil || org == nil || len(org.Environments) == 0 {
		return domainapp.DefaultEnvironments(app)
	}

	// Sort org envs by Order (then Name) for a deterministic pipeline sequence.
	sortedOrgEnvs := make([]rbac.OrgEnvironment, len(org.Environments))
	copy(sortedOrgEnvs, org.Environments)
	sort.Slice(sortedOrgEnvs, func(i, j int) bool {
		if sortedOrgEnvs[i].Order != sortedOrgEnvs[j].Order {
			return sortedOrgEnvs[i].Order < sortedOrgEnvs[j].Order
		}
		return sortedOrgEnvs[i].Name < sortedOrgEnvs[j].Name
	})

	// Reuse the same topology and pattern lookup built inside resolveEnvNamespaces.
	stableRefs := make([]string, 0, len(sortedOrgEnvs))
	for _, e := range sortedOrgEnvs {
		stableRefs = append(stableRefs, e.ClusterRef)
	}
	dedicated := domain.IsDedicatedClusterTopology(stableRefs)

	orgEnvPatterns := make(map[string]string, len(sortedOrgEnvs))
	for _, e := range sortedOrgEnvs {
		if e.NamespacePattern != "" {
			orgEnvPatterns[e.Name] = e.NamespacePattern
		}
	}

	envs := make([]*domain.AppEnvironment, 0, len(sortedOrgEnvs))
	for _, orgEnv := range sortedOrgEnvs {
		envType := domain.AppEnvStaging
		if orgEnv.Name == "prod" || orgEnv.Name == "production" {
			envType = domain.AppEnvProd
		}

		ns, resolveErr := domain.ResolveNamespace(domain.NamespaceResolveInput{
			AppName:           app.Name,
			EnvName:           orgEnv.Name,
			ProjectName:       app.ProjectName,
			OrgName:           org.Name,
			Scope:             app.Spec.NamespaceScope,
			Dedicated:         dedicated,
			AppPattern:        app.Spec.NamespacePattern,
			OrgEnvPattern:     orgEnvPatterns[orgEnv.Name],
			OrgAppDefault:     org.ResourceNaming.EffectiveAppNamespace(),
			OrgProjectDefault: org.ResourceNaming.EffectiveProjectNamespace(),
		})
		if resolveErr != nil {
			ns = domain.GenerateProjectNamespace(app.ProjectName, app.Name, orgEnv.Name)
		}

		envs = append(envs, &domain.AppEnvironment{
			AppName:     app.Name,
			ProjectName: app.ProjectName,
			EnvName:     orgEnv.Name,
			EnvType:     envType,
			Order:       orgEnv.Order,
			Namespace:   ns,
			URLs:        []string{},
			Status:      domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
		})
	}
	return envs
}

// handlePromoteApp handles POST /api/v1/projects/{project}/apps/{app}/promote.
//
// Promotion path is driven by OrgEnvironment.Order: the source is resolved as
// the stable env with the highest Order strictly below the target's Order. If
// no stable predecessor exists, preview environments are considered. Promoting
// to a preview environment is always rejected.
//
// All components in the app share a single AppReleaseRef, so there is no
// possibility of partial component promotion: the entire release bundle moves
// together or not at all.
func (ah *appHandler) handlePromoteApp(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	var req AppPromoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.TargetEnvironment == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "targetEnvironment is required"})
		return
	}

	if _, err := ah.appStore.GetApp(r.Context(), projectName, appName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	targetEnv, err := ah.appStore.GetAppEnvironment(r.Context(), projectName, appName, req.TargetEnvironment)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "environment \"" + req.TargetEnvironment + "\" not found for app \"" + appName + "\"",
		})
		return
	}

	if targetEnv.EnvType == domain.AppEnvPreview {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "cannot promote to a preview environment",
		})
		return
	}

	// Resolve the source: the stable env with the highest Order strictly below
	// the target's Order (closest predecessor). Falls back to preview envs when
	// no stable predecessor exists.
	sourceEnv, err := ah.findPromotionSource(r.Context(), projectName, appName, targetEnv)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	// Write GitOps files for the target environment before triggering the
	// promotion. This ensures ArgoCD can find app.yaml + values.yaml when
	// Kargo (or the store fallback) signals a sync. Best-effort: a publish
	// failure is logged but does not abort the promotion response.
	if ah.gitOpsPublisher != nil {
		app, appErr := ah.appStore.GetApp(r.Context(), projectName, appName)
		if appErr == nil {
			if pubErr := ah.gitOpsPublisher.PublishAppEnv(r.Context(), app, targetEnv); pubErr != nil {
				slog.Warn("promote: failed to publish env files — proceeding with promotion",
					"project", projectName, "app", appName,
					"env", req.TargetEnvironment, "err", pubErr)
			}
		}
	}

	// When Kargo is configured, create a Kargo Promotion CR. The Promotion CR
	// drives the actual release copy through the Kargo pipeline; suparship
	// then returns the Promotion details rather than the local release copy.
	if ah.kargoPromoter != nil {
		kargoResult, err := ah.kargoPromoter.CreatePromotion(
			r.Context(),
			projectName, // Kargo namespace = suparship project name by convention
			appName,
			sourceEnv.EnvName,
			req.TargetEnvironment,
		)
		if err != nil {
			slog.Error("kargo promotion failed",
				"project", projectName, "app", appName,
				"from", sourceEnv.EnvName, "to", req.TargetEnvironment,
				"error", err,
			)
			writeJSON(w, http.StatusInternalServerError, errorResponse{
				Error: "failed to create Kargo promotion: " + err.Error(),
			})
			return
		}
		slog.Info("kargo promotion created",
			"promotion", kargoResult.Name,
			"stage", kargoResult.Stage,
			"freight", kargoResult.Freight,
		)
		writeJSON(w, http.StatusOK, AppPromoteResponse{
			Project:     projectName,
			App:         appName,
			Source:      sourceEnv.EnvName,
			Destination: req.TargetEnvironment,
			Namespace:   targetEnv.Namespace,
			Message:     fmt.Sprintf("Kargo promotion %q created — freight %q is being promoted to %s", kargoResult.Name, kargoResult.Freight, req.TargetEnvironment),
			KargoPromotion: &KargoPromotionDTO{
				Name:    kargoResult.Name,
				Stage:   kargoResult.Stage,
				Freight: kargoResult.Freight,
				Phase:   kargoResult.Phase,
			},
		})
		return
	}

	// Fallback: copy the release bundle in the local store (MVP stub, no Kargo).
	result, err := domainapp.Promote(r.Context(), ah.appStore, domainapp.PromoteRequest{
		ProjectName: projectName,
		AppName:     appName,
		FromEnv:     sourceEnv.EnvName,
		ToEnv:       req.TargetEnvironment,
	})
	if err != nil {
		if errors.Is(err, domainapp.ErrNoRelease) {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to promote app"})
		return
	}

	resp := AppPromoteResponse{
		Project:     projectName,
		App:         appName,
		Source:      sourceEnv.EnvName,
		Destination: req.TargetEnvironment,
		Namespace:   targetEnv.Namespace,
		Message:     "Promotion of " + appName + " from " + sourceEnv.EnvName + " to " + req.TargetEnvironment + " succeeded",
		Release: &AppReleaseRefDTO{
			Image:  result.Release.Image,
			Tag:    result.Release.Tag,
			Commit: result.Release.Commit,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// findPromotionSource returns the best source environment for a promotion to
// the given target. It prefers the stable env with the highest Order strictly
// below target.Order (i.e. the closest predecessor in the pipeline). When no
// stable predecessor exists, the lexicographically first preview env is
// returned so that preview→first-stable promotions still work.
func (ah *appHandler) findPromotionSource(ctx context.Context, projectName, appName string, target *domain.AppEnvironment) (*domain.AppEnvironment, error) {
	envs, err := ah.appStore.ListAppEnvironments(ctx, projectName, appName)
	if err != nil {
		return nil, fmt.Errorf("failed to list app environments")
	}

	// Find stable candidates (non-preview, Order < target.Order).
	var stableCandidates []*domain.AppEnvironment
	var previewCandidates []*domain.AppEnvironment
	for _, env := range envs {
		if env.EnvName == target.EnvName {
			continue
		}
		if env.EnvType == domain.AppEnvPreview {
			previewCandidates = append(previewCandidates, env)
		} else if env.Order < target.Order {
			stableCandidates = append(stableCandidates, env)
		}
	}

	if len(stableCandidates) > 0 {
		// Pick the stable env with the highest Order (closest predecessor).
		// Tie-break lexicographically for determinism.
		sort.Slice(stableCandidates, func(i, j int) bool {
			if stableCandidates[i].Order != stableCandidates[j].Order {
				return stableCandidates[i].Order > stableCandidates[j].Order // descending: pick highest Order
			}
			return stableCandidates[i].EnvName < stableCandidates[j].EnvName
		})
		return stableCandidates[0], nil
	}

	// No stable predecessor — fall back to preview environments.
	if len(previewCandidates) > 0 {
		sort.Slice(previewCandidates, func(i, j int) bool {
			return previewCandidates[i].EnvName < previewCandidates[j].EnvName
		})
		return previewCandidates[0], nil
	}

	return nil, fmt.Errorf("no environment found to promote from; ensure an earlier environment has been deployed")
}

// enrichEnvWithLiveStatus overwrites the stored status fields in env with
// freshly queried Kubernetes data. The Deployment is looked up by the app name
// (workload) inside env.Namespace. On any error the stored values are kept so
// callers always get a valid response. The method is a no-op when
// runtimeProvider is nil (fake/local mode).
func (ah *appHandler) enrichEnvWithLiveStatus(ctx context.Context, appName string, env *domain.AppEnvironment) {
	if ah.runtimeProvider == nil {
		return
	}
	info, err := ah.runtimeProvider.GetServiceRuntime(ctx, env.Namespace, appName)
	if err != nil {
		return
	}
	env.Status = domain.AppRuntimeStatus{
		Phase:        info.Status,
		Replicas:     info.Replicas,
		Available:    info.Available,
		LastDeployed: info.LastDeployed,
	}
	if len(info.IngressURLs) > 0 {
		env.URLs = info.IngressURLs
	}
	// Populate release image from the live Deployment when not already set.
	if info.Image != "" && env.Release == nil {
		env.Release = &domain.AppReleaseRef{Image: info.Image}
	}
}

// --- DTO mapping helpers ---

func appToSummaryDTO(app *domain.App, envs []*domain.AppEnvironment) AppSummaryDTO {
	dto := AppSummaryDTO{
		Name:        app.Name,
		Project:     app.ProjectName,
		DisplayName: app.Spec.DisplayName,
		Description: app.Spec.Description,
		Template: AppTemplateRefDTO{
			Name:    app.Spec.Template.Name,
			Version: app.Spec.Template.Version,
		},
		URLs:       []string{},
		Components: componentDTOs(app.Spec.Components),
		Status: AppStatusSummaryDTO{
			Phase: domain.StatusNotDeployed,
		},
	}

	// Use the first stable environment (staging or prod) for summary status and URLs.
	for _, env := range envs {
		if env.EnvType == domain.AppEnvStaging || env.EnvType == domain.AppEnvProd {
			dto.Status = appRuntimeStatusDTO(env.Status)
			dto.URLs = append(dto.URLs, env.URLs...)
			break
		}
	}

	if dto.URLs == nil {
		dto.URLs = []string{}
	}
	return dto
}

func appToDetailDTO(app *domain.App, envs []*domain.AppEnvironment) AppDetailDTO {
	secretRefs := make([]AppSecretRefDTO, 0, len(app.Spec.SecretRefs))
	for _, ref := range app.Spec.SecretRefs {
		secretRefs = append(secretRefs, AppSecretRefDTO{
			Name:      ref.Name,
			SecretRef: ref.SecretRef,
		})
	}

	values := app.Spec.Values
	if values == nil {
		values = map[string]any{}
	}

	envDTOs := make([]AppEnvironmentSummaryDTO, 0, len(envs))
	for _, env := range envs {
		envDTOs = append(envDTOs, appEnvToDTO(env))
	}

	return AppDetailDTO{
		Name:        app.Name,
		Project:     app.ProjectName,
		DisplayName: app.Spec.DisplayName,
		Description: app.Spec.Description,
		Template: AppTemplateRefDTO{
			Name:    app.Spec.Template.Name,
			Version: app.Spec.Template.Version,
		},
		Values:       values,
		SecretRefs:   secretRefs,
		Components:   componentDTOs(app.Spec.Components),
		Environments: envDTOs,
	}
}

func appEnvToDTO(env *domain.AppEnvironment) AppEnvironmentSummaryDTO {
	urls := env.URLs
	if urls == nil {
		urls = []string{}
	}

	dto := AppEnvironmentSummaryDTO{
		EnvName:   env.EnvName,
		EnvType:   string(env.EnvType),
		Namespace: env.Namespace,
		URLs:      urls,
		Status:    appRuntimeStatusDTO(env.Status),
	}

	if env.Release != nil {
		dto.Release = &AppReleaseRefDTO{
			Image:  env.Release.Image,
			Tag:    env.Release.Tag,
			Commit: env.Release.Commit,
		}
	}

	if env.EnvType == domain.AppEnvPreview {
		dto.Preview = &PreviewMetaDTO{
			PreviewName: env.EnvName,
		}
	}

	return dto
}

func appRuntimeStatusDTO(s domain.AppRuntimeStatus) AppStatusSummaryDTO {
	return AppStatusSummaryDTO{
		Phase:        s.Phase,
		Replicas:     s.Replicas,
		Available:    s.Available,
		LastDeployed: s.LastDeployed,
	}
}

func componentDTOs(components []domain.ComponentSpec) []ComponentSummaryDTO {
	dtos := make([]ComponentSummaryDTO, 0, len(components))
	for _, c := range components {
		dtos = append(dtos, ComponentSummaryDTO{
			Name:           c.Name,
			Type:           string(c.Type),
			Enabled:        c.Enabled,
			Expose:         c.Expose,
			PreviewEnabled: c.PreviewEnabled,
		})
	}
	return dtos
}

func appPreviewToDTO(env *domain.AppEnvironment) AppPreviewSummaryDTO {
	urls := env.URLs
	if urls == nil {
		urls = []string{}
	}
	dto := AppPreviewSummaryDTO{
		Name:      env.EnvName,
		AppName:   env.AppName,
		Project:   env.ProjectName,
		Namespace: env.Namespace,
		Status:    appRuntimeStatusDTO(env.Status),
		URLs:      urls,
	}
	if env.Release != nil {
		dto.Release = &AppReleaseRefDTO{
			Image:  env.Release.Image,
			Tag:    env.Release.Tag,
			Commit: env.Release.Commit,
		}
	}
	return dto
}

// handleGetKargoPromotion handles GET /api/v1/projects/{project}/apps/{app}/promotions/{name}.
// It returns the current observed phase of a Kargo Promotion CR so the UI can
// poll for live status updates after triggering a promotion.
//
// The endpoint is only active when kargoStatusReader is configured; otherwise
// it returns 501 Not Implemented.
func (ah *appHandler) handleGetKargoPromotion(w http.ResponseWriter, r *http.Request) {
	if ah.kargoStatusReader == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "kargo status reader not configured"})
		return
	}

	projectName := r.PathValue("project")
	promotionName := r.PathValue("name")

	if projectName == "" || promotionName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "project and name are required"})
		return
	}

	result, err := ah.kargoStatusReader.GetPromotionStatus(r.Context(), projectName, promotionName)
	if err != nil {
		slog.Error("failed to get kargo promotion status",
			"project", projectName, "promotion", promotionName, "error", err,
		)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to get promotion status"})
		return
	}

	slog.Debug("kargo promotion status polled",
		"project", projectName,
		"promotion", promotionName,
		"stage", result.Stage,
		"freight", result.Freight,
		"phase", result.Phase,
	)

	writeJSON(w, http.StatusOK, KargoPromotionStatusResponse{
		Name:    result.Name,
		Stage:   result.Stage,
		Freight: result.Freight,
		Phase:   result.Phase,
	})
}

// handleGetKargoStages handles GET /api/v1/projects/{project}/apps/{app}/kargo/stages.
// It returns the live Kargo Stage statuses for all stages belonging to the app
// (using the "{appName}-{envName}" naming convention). The UI uses this to show
// pipeline progress — stage phase, health, current freight, and how many new
// freights are waiting to be promoted.
//
// Returns 501 when kargoPipelineReader is not configured.
func (ah *appHandler) handleGetKargoStages(w http.ResponseWriter, r *http.Request) {
	if ah.kargoPipelineReader == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "kargo pipeline reader not configured"})
		return
	}

	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	if projectName == "" || appName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "project and app are required"})
		return
	}

	stages, err := ah.kargoPipelineReader.ListAppStageStatuses(r.Context(), projectName, appName)
	if err != nil {
		slog.Error("failed to list kargo stage statuses",
			"project", projectName, "app", appName, "error", err,
		)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list kargo stage statuses"})
		return
	}

	slog.Debug("kargo pipeline stages returned",
		"project", projectName,
		"app", appName,
		"count", len(stages),
	)
	for _, s := range stages {
		slog.Debug("kargo pipeline stage",
			"project", projectName,
			"app", appName,
			"stage", s.StageName,
			"env", s.EnvName,
			"phase", s.Phase,
			"health", s.Health,
			"currentFreight", s.CurrentFreight,
			"availableFreightCount", s.AvailableFreightCount,
		)
	}

	dtos := make([]KargoStageStatusDTO, 0, len(stages))
	for _, s := range stages {
		dtos = append(dtos, KargoStageStatusDTO{
			StageName:             s.StageName,
			EnvName:               s.EnvName,
			Phase:                 s.Phase,
			Health:                s.Health,
			CurrentFreight:        s.CurrentFreight,
			AvailableFreightCount: s.AvailableFreightCount,
		})
	}

	writeJSON(w, http.StatusOK, KargoAppPipelineResponse{Stages: dtos})
}

// handleGetAppDeploymentHistory handles
// GET /api/v1/projects/{project}/apps/{app}/environments/{env}/history.
//
// It returns the ArgoCD sync history for the Application CR named
// "{appName}-{envName}", in reverse-chronological order (most recent first).
// Returns 501 when the deploymentHistoryReader is not configured (e.g. in
// fake/local dev mode without an ArgoCD integration).
// Returns an empty history slice (not an error) when the Application exists
// but has no sync events yet.
func (ah *appHandler) handleGetAppDeploymentHistory(w http.ResponseWriter, r *http.Request) {
	if ah.deploymentHistoryReader == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "deployment history reader not configured"})
		return
	}

	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	if projectName == "" || appName == "" || envName == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "project, app, and env are required"})
		return
	}

	// Verify the app and environment exist before querying ArgoCD.
	if _, err := ah.appStore.GetApp(r.Context(), projectName, appName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}
	if _, err := ah.appStore.GetAppEnvironment(r.Context(), projectName, appName, envName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "environment \"" + envName + "\" not found for app \"" + appName + "\"",
		})
		return
	}

	history, err := ah.deploymentHistoryReader.GetAppDeploymentHistory(r.Context(), appName, envName)
	if err != nil {
		slog.Error("failed to get deployment history",
			"project", projectName, "app", appName, "env", envName, "error", err,
		)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to get deployment history"})
		return
	}

	dtos := make([]AppDeploymentHistoryEntryDTO, 0, len(history))
	for _, h := range history {
		dtos = append(dtos, AppDeploymentHistoryEntryDTO{
			ID:              h.ID,
			Revision:        h.Revision,
			DeployedAt:      h.DeployedAt,
			DeployStartedAt: h.DeployStartedAt,
			RepoURL:         h.RepoURL,
			Path:            h.Path,
			TargetRevision:  h.TargetRevision,
		})
	}

	writeJSON(w, http.StatusOK, AppDeploymentHistoryResponse{
		Project:     projectName,
		App:         appName,
		Environment: envName,
		History:     dtos,
	})
}
