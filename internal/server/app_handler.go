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

// appPromotionOrder defines the canonical MVP promotion chain.
// preview → staging → prod; higher order = later in the chain.
var appPromotionOrder = map[domain.AppEnvironmentType]int{
	domain.AppEnvPreview: 0,
	domain.AppEnvStaging: 1,
	domain.AppEnvProd:    2,
}

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

// stableEnvsFromOrg synthesises a minimal set of stable AppEnvironments from
// the org-level environment definitions. Used as a fallback in handleSyncApp
// when the app's environments have not been persisted to the store (e.g. for
// legacy apps seeded from the project.Service model).
func (ah *appHandler) stableEnvsFromOrg(ctx context.Context, app *domain.App) []*domain.AppEnvironment {
	if ah.orgProvider == nil {
		return domainapp.DefaultEnvironments(app)
	}

	org, err := ah.orgProvider.GetOrg(ctx)
	if err != nil || org == nil || len(org.Environments) == 0 {
		return domainapp.DefaultEnvironments(app)
	}

	envs := make([]*domain.AppEnvironment, 0, len(org.Environments))
	for _, orgEnv := range org.Environments {
		envType := domain.AppEnvStaging
		if orgEnv.Name == "prod" || orgEnv.Name == "production" {
			envType = domain.AppEnvProd
		}
		envs = append(envs, &domain.AppEnvironment{
			AppName:     app.Name,
			ProjectName: app.ProjectName,
			EnvName:     orgEnv.Name,
			EnvType:     envType,
			Namespace:   domain.GenerateNamespace(app.Name, orgEnv.Name, envType),
			URLs:        []string{},
			Status:      domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed},
		})
	}
	return envs
}

// handlePromoteApp handles POST /api/v1/projects/{project}/apps/{app}/promote.
//
// Promotion path is preview → staging → prod. Promoting to a preview
// environment is rejected. The handler resolves the source environment
// deterministically (lexicographically first candidate at the tier immediately
// below the target), then delegates the actual release-copy to
// domainapp.Promote which performs the all-or-nothing write.
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

	targetOrder, ok := appPromotionOrder[targetEnv.EnvType]
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "environment \"" + req.TargetEnvironment + "\" has an unrecognised type",
		})
		return
	}

	// Resolve the source: the lexicographically first environment at the tier
	// immediately below the target in the promotion chain.
	sourceEnv, err := ah.findPromotionSource(r.Context(), projectName, appName, targetOrder)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
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
// the given target order. It expects an environment of the immediately lower
// tier in the MVP chain (preview < staging < prod). When multiple candidates
// exist (e.g. several preview environments), the lexicographically first is
// chosen for determinism.
func (ah *appHandler) findPromotionSource(ctx context.Context, projectName, appName string, targetOrder int) (*domain.AppEnvironment, error) {
	envs, err := ah.appStore.ListAppEnvironments(ctx, projectName, appName)
	if err != nil {
		return nil, fmt.Errorf("failed to list app environments")
	}

	wantOrder := targetOrder - 1
	var candidates []*domain.AppEnvironment
	for _, env := range envs {
		if order, ok := appPromotionOrder[env.EnvType]; ok && order == wantOrder {
			candidates = append(candidates, env)
		}
	}

	if len(candidates) == 0 {
		var sourceTier string
		for t, o := range appPromotionOrder {
			if o == wantOrder {
				sourceTier = string(t)
				break
			}
		}
		if sourceTier == "" {
			sourceTier = "previous"
		}
		return nil, fmt.Errorf("no %s environment found to promote from", sourceTier)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].EnvName < candidates[j].EnvName
	})
	return candidates[0], nil
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
