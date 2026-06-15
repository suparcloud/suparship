package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"k8s.io/client-go/kubernetes"

	domainapp "github.com/suparcloud/suparship/internal/app"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/kube"
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
	appStore domain.AppStore
	// builtin + clusterLoader resolve templates live (cluster overrides built-in)
	// so externally-synced templates are usable for app creation/upgrade without
	// a restart. Use lookupTemplate; do not read a cached index.
	builtin                 []*tpl.Template
	clusterLoader           ClusterTemplateLoader
	projectStore            project.Store
	orgProvider             rbac.OrgProvider        // optional: provides org env fallback for sync
	runtimeProvider         runtime.Provider        // optional: enriches env responses with live K8s status
	logsProvider            runtime.LogsProvider    // optional: enables GET .../apps/{app}/logs
	gitOpsPublisher         GitOpsPublisher         // optional: commits argocd manifests to gitops repo on create
	kargoPromoter           KargoPromoter           // optional: creates Kargo Promotion CRs on promote
	kargoStatusReader       KargoStatusReader       // optional: reads live Kargo Promotion status
	kargoPipelineReader     KargoPipelineReader     // optional: reads live Kargo Stage pipeline status
	deploymentHistoryReader DeploymentHistoryReader // optional: reads ArgoCD sync history
	diagnosticsReader       AppDiagnosticsReader    // optional: reads ArgoCD/ESO failure signals for app env status
	// kubeClient lets the upgrade-template handler validate that the
	// requested version actually exists in the cluster as an archive
	// ConfigMap before mutating + republishing. Optional — when nil
	// the upgrade endpoint trusts the request body and falls through.
	kubeClient kubernetes.Interface
	// clusterPool builds per-cluster Kubernetes clients from stored
	// kubeconfigs. In hub-spoke installs workloads run on remote registered
	// clusters, so live status + logs must be read from the env's workload
	// cluster (resolved via the org Environment's EffectiveClusterRef), not
	// suparship's own tooling cluster. Optional — nil falls back to the
	// locally-injected runtime/logs providers (single-cluster installs).
	clusterPool *k8s.ClusterClientPool
}

// newAppHandler creates an appHandler.
//
// templates and projectStore are optional. Passing non-nil values enables the
// POST /api/v1/projects/{project}/apps creation endpoint; the caller is
// responsible for registering the route only when both are present (see
// rbacHandler.registerRoutes).
func newAppHandler(store domain.AppStore, templates []*tpl.Template, clusterLoader ClusterTemplateLoader, projectStore project.Store) *appHandler {
	return &appHandler{
		appStore:      store,
		builtin:       templates,
		clusterLoader: clusterLoader,
		projectStore:  projectStore,
	}
}

// lookupTemplate resolves a template by name live (cluster overrides built-in),
// so externally-synced templates are usable for app creation/upgrade without a
// server restart. Returns (nil, false) when the name is unknown.
func (ah *appHandler) lookupTemplate(ctx context.Context, name string) (*tpl.Template, bool) {
	t, ok := resolveTemplates(ctx, ah.builtin, ah.clusterLoader, nil)[name]
	return t, ok
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

	tmpl, ok := ah.lookupTemplate(r.Context(), req.Template)
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
		mode, err := domain.ParseExposeMode(c.ExposeMode)
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
			ExposeMode:     mode,
			PreviewEnabled: c.PreviewEnabled,
		})
	}
	if len(explicitComponents) > 0 {
		if err := domain.ValidateComponents(explicitComponents); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
	}

	// Translate addon claim DTOs → domain spec. domain.ValidateAddons
	// runs inside Create; surface its error verbatim if it fires.
	addons := make([]domain.AddonSpec, len(req.Addons))
	for i, a := range req.Addons {
		addons[i] = domain.AddonSpec{
			Name:    a.Name,
			Type:    a.Type,
			Size:    a.Size,
			Version: a.Version,
			Values:  a.Values,
		}
	}

	values := req.Values
	if values == nil {
		values = map[string]any{}
	}
	if repo, ok := values["image_repository"].(string); ok {
		if err := domain.ValidateImageRepository(repo); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
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
		Addons:             addons,
		NamespaceScope:     domain.NamespaceScope(req.NamespaceScope),
		NamespacePattern:   req.NamespacePattern,
		RawValues:          req.RawValues,
		ComponentConfigs:   req.ComponentConfigs,
		EnvComponents:      req.EnvComponents,
	})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	// Verify at least one environment is registered in the org before creating
	// the app. Deploying to unregistered environments silently would produce
	// orphaned GitOps manifests pointing at clusters that don't exist.
	//
	// While we have the org loaded, also validate that every component's
	// EffectiveExposeMode resolves against the configured RoutingProfiles.
	// Per-env overrides are checked against each env's override map so an
	// app declaring exposeMode=external doesn't slip through when one env
	// removes the external profile via override.
	if ah.orgProvider != nil {
		org, orgErr := ah.orgProvider.GetOrg(r.Context())
		if orgErr != nil || org == nil || len(org.Environments) == 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: "no environments registered in the org; register at least one via POST /api/v1/org/environments before creating apps",
			})
			return
		}
		if err := domain.ValidateExposeModes(result.App.Spec.Components, org.RoutingProfiles, nil); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
		for _, e := range org.Environments {
			if len(e.RoutingProfiles) == 0 {
				continue
			}
			if err := domain.ValidateExposeModes(result.App.Spec.Components, org.RoutingProfiles, e.RoutingProfiles); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
					Error: "environment " + e.Name + ": " + err.Error(),
				})
				return
			}
		}
		// Each addon claim must resolve at the org level OR at every
		// env's per-env override. A claim no env can resolve would
		// produce an orphan publish (silent skip). Catch it at save.
		for _, claim := range result.App.Spec.Addons {
			if _, err := domain.ResolveAddonProfile(org.AddonProfiles, nil, claim.Type); err == nil {
				continue
			}
			// Org has no profile for this type — every env must override.
			missing := []string{}
			for _, e := range org.Environments {
				if _, err := domain.ResolveAddonProfile(org.AddonProfiles, e.AddonProfiles, claim.Type); err != nil {
					missing = append(missing, e.Name)
				}
			}
			if len(missing) > 0 {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
					Error: "addon " + claim.Name + " (type " + claim.Type +
						"): no AddonProfile configured for envs " + strings.Join(missing, ", ") +
						" — set one via PUT /api/v1/org/addon-profiles/" + claim.Type,
				})
				return
			}
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

// handleUpdateApp handles PATCH /api/v1/projects/{project}/apps/{app}.
//
// Edits an existing app's display name, description, and template input Values.
// It is create's validate→persist→publish tail applied to an existing record:
// Values are re-validated against the template's input schema (the same
// project.ValidateAppInputs create uses) and the image repository checked, then
// the spec is saved and re-published so values.yaml regenerates. The template
// name is immutable; the version is changed via upgrade-template. On a publish
// failure the spec change is rolled back so the store never drifts from gitops.
func (ah *appHandler) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	var req updateAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	app, err := ah.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	if req.Template != "" && req.Template != app.Spec.Template.Name {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
			Error: "cannot change an app's template; use the upgrade-template endpoint to change the version",
		})
		return
	}

	// Snapshot the editable fields so a failed publish rolls back cleanly.
	prevValues, prevDisplay, prevDesc := app.Spec.Values, app.Spec.DisplayName, app.Spec.Description
	prevEnvDefaults := app.Spec.EnvironmentDefaults
	prevRawValues := app.Spec.RawValues
	prevComponents := append([]domain.ComponentSpec(nil), app.Spec.Components...)

	if req.Values != nil {
		newValues := *req.Values
		if newValues == nil {
			newValues = map[string]any{}
		}
		if repo, ok := newValues["image_repository"].(string); ok {
			if err := domain.ValidateImageRepository(repo); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
				return
			}
		}
		tmpl, ok := ah.lookupTemplate(r.Context(), app.Spec.Template.Name)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, errorResponse{
				Error: "app's template \"" + app.Spec.Template.Name + "\" is no longer available; cannot re-validate values",
			})
			return
		}
		secretRefs := make([]project.SecretRef, len(app.Spec.SecretRefs))
		for i, sr := range app.Spec.SecretRefs {
			secretRefs[i] = project.SecretRef{Name: sr.Name, SecretRef: sr.SecretRef}
		}
		if err := project.ValidateAppInputs(newValues, secretRefs, tmpl); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
		app.Spec.Values = newValues
	}
	if req.DisplayName != nil {
		app.Spec.DisplayName = *req.DisplayName
	}
	if req.Description != nil {
		app.Spec.Description = *req.Description
	}
	if req.ClusterOverrides != nil {
		// Replace per-(env, cluster) value overrides. Fold each into the app's
		// EnvironmentDefaults so they ride the existing per-env override record.
		ed := app.Spec.EnvironmentDefaults
		if ed == nil {
			ed = map[string]domain.EnvironmentOverride{}
		}
		for envName, byCluster := range req.ClusterOverrides {
			ov := ed[envName]
			if len(byCluster) == 0 {
				ov.ClusterOverrides = nil
			} else {
				ov.ClusterOverrides = byCluster
			}
			ed[envName] = ov
		}
		app.Spec.EnvironmentDefaults = ed
	}
	if req.RawValues != nil {
		app.Spec.RawValues = *req.RawValues
	}
	if req.ComponentConfigs != nil {
		// Apply app-level per-component config onto the matching ComponentSpec.
		for i := range app.Spec.Components {
			cfg, ok := req.ComponentConfigs[app.Spec.Components[i].Name]
			if !ok {
				continue
			}
			applyComponentConfig(&app.Spec.Components[i], cfg)
		}
	}
	if req.EnvComponents != nil {
		ed := app.Spec.EnvironmentDefaults
		if ed == nil {
			ed = map[string]domain.EnvironmentOverride{}
		}
		for envName, byComp := range req.EnvComponents {
			ov := ed[envName]
			if len(byComp) == 0 {
				ov.Components = nil
			} else {
				ov.Components = byComp
			}
			ed[envName] = ov
		}
		app.Spec.EnvironmentDefaults = ed
	}
	if req.EnvRawValues != nil {
		// Replace per-env raw-values overlays, folding into EnvironmentDefaults.
		ed := app.Spec.EnvironmentDefaults
		if ed == nil {
			ed = map[string]domain.EnvironmentOverride{}
		}
		for envName, rv := range req.EnvRawValues {
			ov := ed[envName]
			if len(rv) == 0 {
				ov.RawValues = nil
			} else {
				ov.RawValues = rv
			}
			ed[envName] = ov
		}
		app.Spec.EnvironmentDefaults = ed
	}

	if err := ah.appStore.SaveApp(r.Context(), projectName, app); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save app"})
		return
	}

	// Re-publish so values.yaml reflects the new inputs (best-effort with
	// rollback, mirroring upgrade-template). Skip cleanly when no publisher.
	if ah.gitOpsPublisher != nil {
		allEnvs, err := ah.appStore.ListAppEnvironments(r.Context(), projectName, appName)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list app environments"})
			return
		}
		var stableEnvs []*domain.AppEnvironment
		for _, env := range allEnvs {
			if env.EnvType != domain.AppEnvPreview {
				stableEnvs = append(stableEnvs, env)
			}
		}
		if len(stableEnvs) == 0 {
			stableEnvs = ah.stableEnvsFromOrg(r.Context(), app)
		}
		if err := ah.gitOpsPublisher.PublishApp(r.Context(), app, stableEnvs); err != nil {
			app.Spec.Values, app.Spec.DisplayName, app.Spec.Description = prevValues, prevDisplay, prevDesc
			app.Spec.EnvironmentDefaults = prevEnvDefaults
			app.Spec.RawValues = prevRawValues
			app.Spec.Components = prevComponents
			_ = ah.appStore.SaveApp(r.Context(), projectName, app)
			slog.Error("update-app: publish failed; rolled back config change",
				"project", projectName, "app", appName, "err", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{
				Error: "publish failed; config change rolled back: " + err.Error(),
			})
			return
		}
	}

	saved, _ := ah.appStore.GetApp(r.Context(), projectName, appName)
	savedEnvs, _ := ah.appStore.ListAppEnvironments(r.Context(), projectName, appName)
	writeJSON(w, http.StatusOK, updateAppResponse{App: appToDetailDTO(saved, savedEnvs)})
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

// upgradeAppTemplateRequest is the body for POST .../upgrade-template.
type upgradeAppTemplateRequest struct {
	// Version is the target template version. Must be one of the
	// versions returned by GET /api/v1/templates/{name}/versions.
	Version string `json:"version"`
}

// handleUpgradeAppTemplate pins an app to a specific template version
// and re-publishes via the existing gitops flow. The publisher's
// version-aware ChartFetcher then resolves to the per-version archive
// ConfigMap, so the chart bytes Argo deploys actually change.
//
// This does NOT migrate values when the new version's input schema
// differs from the old one — operators are expected to check the
// template's input shape before upgrading and adjust values via the
// existing app-edit flow if needed. Argo will surface render errors
// loudly enough for now; a values-migration prompt is a follow-up.
func (ah *appHandler) handleUpgradeAppTemplate(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	var req upgradeAppTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Version) == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "version is required"})
		return
	}

	if ah.gitOpsPublisher == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{
			Error: "gitops publisher not configured — set SUPARSHIP_GITOPS_REPO_URL to enable",
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

	// Validate the requested version exists as an archive ConfigMap.
	// Skip the check when no kubeClient is wired (test harnesses,
	// fake mode) — caller's responsibility to pass a real version.
	if ah.kubeClient != nil {
		versions, err := kube.ListTemplateVersions(r.Context(), ah.kubeClient, app.Spec.Template.Name)
		if err != nil {
			slog.Error("upgrade-template: list versions failed", "template", app.Spec.Template.Name, "err", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list template versions"})
			return
		}
		var found bool
		for _, v := range versions {
			if v.Version == req.Version {
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: fmt.Sprintf("template %q has no archived version %q — call GET /api/v1/templates/%s/versions to see what's available",
					app.Spec.Template.Name, req.Version, app.Spec.Template.Name),
			})
			return
		}
	}

	// No-op early-return: re-pinning to the same version is fine but
	// don't pretend we did work. The UI shouldn't surface this button
	// when versions match, but be safe.
	if app.Spec.Template.Version == req.Version {
		writeJSON(w, http.StatusOK, map[string]string{
			"message": "app already pinned to " + req.Version,
			"project": projectName,
			"app":     appName,
			"version": req.Version,
		})
		return
	}

	prevVersion := app.Spec.Template.Version
	app.Spec.Template.Version = req.Version
	if err := ah.appStore.SaveApp(r.Context(), projectName, app); err != nil {
		slog.Error("upgrade-template: save app failed", "project", projectName, "app", appName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist version pin"})
		return
	}

	// Re-publish via the same path as /sync. PublishApp's syncChart
	// honours app.Spec.Template.Version (PR5.1), so the chart bytes in
	// the gitops repo actually change to the new version's archive.
	allEnvs, err := ah.appStore.ListAppEnvironments(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list app environments"})
		return
	}
	var stableEnvs []*domain.AppEnvironment
	for _, env := range allEnvs {
		if env.EnvType != domain.AppEnvPreview {
			stableEnvs = append(stableEnvs, env)
		}
	}
	if len(stableEnvs) == 0 {
		stableEnvs = ah.stableEnvsFromOrg(r.Context(), app)
	}

	if err := ah.gitOpsPublisher.PublishApp(r.Context(), app, stableEnvs); err != nil {
		// Roll the version pin back so the operator can retry without
		// the saved state being stuck on a version that didn't publish.
		app.Spec.Template.Version = prevVersion
		_ = ah.appStore.SaveApp(r.Context(), projectName, app)
		slog.Error("upgrade-template: publish failed; rolled back version pin",
			"project", projectName, "app", appName, "from", prevVersion, "to", req.Version, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Error: "publish failed; version pin rolled back: " + err.Error(),
		})
		return
	}

	slog.Info("app upgraded to template version",
		"project", projectName, "app", appName,
		"from", prevVersion, "to", req.Version,
	)
	writeJSON(w, http.StatusOK, map[string]string{
		"message":     "app upgraded — ArgoCD will sync the new chart bytes shortly",
		"project":     projectName,
		"app":         appName,
		"fromVersion": prevVersion,
		"toVersion":   req.Version,
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
		stableRefs = append(stableRefs, e.EffectiveClusterRef())
	}
	dedicated := domain.IsDedicatedClusterTopology(stableRefs)

	// Per-environment namespace pattern overrides defined at the org level.
	// AppNamespacePattern (with legacy NamespacePattern fallback) drives
	// scope=app; ProjectNamespacePattern drives scope=project — the two are
	// resolved independently so a pattern like "{app}" set for app-scope
	// doesn't bleed into project-scope resolution.
	orgEnvAppPatterns := make(map[string]string, len(org.Environments))
	orgEnvProjPatterns := make(map[string]string, len(org.Environments))
	for _, e := range org.Environments {
		if p := e.EffectiveAppNamespacePattern(); p != "" {
			orgEnvAppPatterns[e.Name] = p
		}
		if p := e.EffectiveProjectNamespacePattern(); p != "" {
			orgEnvProjPatterns[e.Name] = p
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
			OrgEnvAppPattern:  orgEnvAppPatterns[env.EnvName],
			OrgEnvProjPattern: orgEnvProjPatterns[env.EnvName],
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
	return StableEnvsFromOrg(ctx, ah.orgProvider, app)
}

// StableEnvsFromOrg derives an app's stable environments from the org's
// canonical environment pipeline, resolving each env's namespace from the org's
// topology + naming patterns. It is the publish-time fallback used when an app
// has no persisted AppEnvironment records (the create path and the startup
// republish both rely on it). Falls back to domainapp.DefaultEnvironments when
// no org / no environments are configured.
func StableEnvsFromOrg(ctx context.Context, orgProvider rbac.OrgProvider, app *domain.App) []*domain.AppEnvironment {
	if orgProvider == nil {
		return domainapp.DefaultEnvironments(app)
	}

	org, err := orgProvider.GetOrg(ctx)
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
		stableRefs = append(stableRefs, e.EffectiveClusterRef())
	}
	dedicated := domain.IsDedicatedClusterTopology(stableRefs)

	orgEnvAppPatterns := make(map[string]string, len(sortedOrgEnvs))
	orgEnvProjPatterns := make(map[string]string, len(sortedOrgEnvs))
	for _, e := range sortedOrgEnvs {
		if p := e.EffectiveAppNamespacePattern(); p != "" {
			orgEnvAppPatterns[e.Name] = p
		}
		if p := e.EffectiveProjectNamespacePattern(); p != "" {
			orgEnvProjPatterns[e.Name] = p
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
			OrgEnvAppPattern:  orgEnvAppPatterns[orgEnv.Name],
			OrgEnvProjPattern: orgEnvProjPatterns[orgEnv.Name],
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

// workloadClusterClient resolves the cluster an environment's workloads run on
// and returns a Kubernetes client for it. In a hub-spoke install the workload
// lives on a remote registered cluster (the env's EffectiveClusterRef), so live
// status and logs must be read from THAT cluster — suparship's own (tooling)
// cluster has no such Deployment/pods, which is why an otherwise-Healthy app
// reads back as "not deployed" with no logs.
//
// Return contract:
//   - (nil, nil): no remote routing applies (no pool/org wired, or the env has
//     no cluster binding) — the caller should use its locally-injected provider
//     (single-cluster installs where workloads share suparship's cluster).
//   - (client, nil): use this remote workload-cluster client.
//   - (nil, err): the env IS bound to a remote cluster but it's unreachable
//     (kubeconfig missing/bad) — the caller must NOT silently fall back to the
//     local cluster, since that would falsely report "not deployed".
func (ah *appHandler) workloadClusterClient(ctx context.Context, envName string) (kubernetes.Interface, error) {
	return workloadClusterClientForEnv(ctx, ah.orgProvider, ah.clusterPool, envName)
}

// workloadClusterClientForEnv resolves the cluster an environment's workloads
// run on and returns a Kubernetes client for it, shared by the app, inventory,
// and (future) preview status paths. See workloadClusterClient for the return
// contract; nil pool or orgProvider means "use the local provider".
func workloadClusterClientForEnv(ctx context.Context, orgProvider rbac.OrgProvider, pool *k8s.ClusterClientPool, envName string) (kubernetes.Interface, error) {
	if pool == nil || orgProvider == nil {
		return nil, nil
	}
	org, err := orgProvider.GetOrg(ctx)
	if err != nil {
		return nil, nil // org unreadable — degrade to the local provider
	}
	var ref string
	for _, e := range org.Environments {
		if e.Name == envName {
			ref = e.EffectiveClusterRef()
			break
		}
	}
	if ref == "" {
		return nil, nil // env not bound to a cluster — single-cluster/local mode
	}
	client, err := pool.Get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("workload cluster %q: %w", ref, err)
	}
	return client, nil
}

// namedClient pairs a resolved workload-cluster client with its cluster name.
type namedClient struct {
	name   string
	client kubernetes.Interface
}

// workloadClustersForEnv resolves the env's deploy-target clusters (one in
// "active" mode, all bound clusters in "all" mode) to Kubernetes clients.
// routed=false means no remote routing applies (no pool/org wired, or the env
// is unbound) — the caller uses its locally-injected provider. When routed, it
// returns a client per reachable target plus the names of targets it couldn't
// reach (surfaced as diagnostics, never silently dropped).
func (ah *appHandler) workloadClustersForEnv(ctx context.Context, envName string) (clients []namedClient, unreachable []string, routed bool) {
	if ah.clusterPool == nil || ah.orgProvider == nil {
		return nil, nil, false
	}
	org, err := ah.orgProvider.GetOrg(ctx)
	if err != nil {
		return nil, nil, false
	}
	var targets []string
	for _, e := range org.Environments {
		if e.Name == envName {
			targets = e.ResolveDeployTargets()
			break
		}
	}
	if len(targets) == 0 {
		return nil, nil, false // unbound — single-cluster/local mode
	}
	for _, ref := range targets {
		c, err := ah.clusterPool.Get(ctx, ref)
		if err != nil {
			unreachable = append(unreachable, ref)
			continue
		}
		clients = append(clients, namedClient{name: ref, client: c})
	}
	return clients, unreachable, true
}

// runtimeStatusRank ranks phases by severity so a fan-out env surfaces its
// worst cluster: an env is only "healthy" when every cluster is healthy.
var runtimeStatusRank = map[string]int{
	runtime.StatusHealthy:     0,
	runtime.StatusProgressing: 1,
	runtime.StatusUnknown:     2,
	runtime.StatusNotDeployed: 3,
	runtime.StatusDegraded:    4,
}

// enrichEnvWithLiveStatus overwrites the stored status fields in env with freshly
// queried Kubernetes data, read from the env's workload cluster(s). In "all"
// deploy mode it aggregates across every target cluster (worst-of phase, summed
// replicas, per-cluster breakdown in diagnostics). On any error the stored values
// are kept so callers always get a valid response. No-op in fake/local mode.
func (ah *appHandler) enrichEnvWithLiveStatus(ctx context.Context, appName string, env *domain.AppEnvironment) {
	// Diagnostics run regardless of the runtime lookup: a failed sync produces
	// no workload, so GetServiceRuntime errors — but that "not deployed" case
	// is exactly when the operator needs the ArgoCD/ESO failure reason.
	defer ah.enrichEnvWithDiagnostics(ctx, appName, env)

	clients, unreachable, routed := ah.workloadClustersForEnv(ctx, env.EnvName)

	// No remote routing (single-cluster / fake mode): query the local provider.
	if !routed {
		if ah.runtimeProvider == nil {
			return
		}
		if info, err := ah.runtimeProvider.GetServiceRuntime(ctx, env.Namespace, appName); err == nil {
			ah.applyRuntimeInfo(env, info)
		}
		return
	}

	for _, name := range unreachable {
		env.Status.Diagnostics = append(env.Status.Diagnostics, domain.Diagnostic{
			Source: "runtime",
			Level:  domain.DiagnosticWarning,
			Title:  "Workload cluster unreachable",
			Detail: fmt.Sprintf("cluster %q could not be reached", name),
			Hint:   "Live replica count for this cluster is unavailable (ArgoCD may still show it Healthy). Check the cluster's kubeconfig/credentials under Clusters and that its API server is reachable from suparShip.",
		})
	}
	if len(clients) == 0 {
		return // all targets unreachable; diagnostics above explain
	}

	// Aggregate across reachable clusters: worst-of phase, summed replicas.
	agg := &runtime.RuntimeInfo{Status: runtime.StatusHealthy}
	got := false
	for _, nc := range clients {
		info, err := runtime.NewK8sProvider(nc.client).GetServiceRuntime(ctx, env.Namespace, appName)
		if err != nil {
			continue
		}
		got = true
		if runtimeStatusRank[info.Status] >= runtimeStatusRank[agg.Status] {
			agg.Status = info.Status
		}
		agg.Replicas += info.Replicas
		agg.Available += info.Available
		if info.LastDeployed > agg.LastDeployed {
			agg.LastDeployed = info.LastDeployed
		}
		if agg.Image == "" {
			agg.Image = info.Image
		}
		agg.IngressURLs = append(agg.IngressURLs, info.IngressURLs...)
		if len(clients) > 1 {
			env.Status.Diagnostics = append(env.Status.Diagnostics, domain.Diagnostic{
				Source: "runtime",
				Level:  domain.DiagnosticInfo,
				Title:  fmt.Sprintf("Cluster %s: %s", nc.name, info.Status),
				Detail: fmt.Sprintf("%d/%d replicas available", info.Available, info.Replicas),
			})
		}
	}
	if !got {
		return
	}
	ah.applyRuntimeInfo(env, agg)
}

// applyRuntimeInfo folds a RuntimeInfo into the env's stored status/urls/release.
func (ah *appHandler) applyRuntimeInfo(env *domain.AppEnvironment, info *runtime.RuntimeInfo) {
	env.Status.Phase = info.Status
	env.Status.Replicas = info.Replicas
	env.Status.Available = info.Available
	env.Status.LastDeployed = info.LastDeployed
	if len(info.IngressURLs) > 0 {
		env.URLs = info.IngressURLs
	}
	if info.Image != "" && env.Release == nil {
		env.Release = &domain.AppReleaseRef{Image: info.Image}
	}
}

// enrichEnvWithDiagnostics appends ArgoCD/ESO failure signals to env.Status so
// a stuck or "not deployed" env explains itself. It reads both the chart
// Application ({project}-{app}-{env}) and its platform companion
// ({project}-{app}-{env}-platform) — the latter owns the ConfigMap +
// ExternalSecret, so ESO "not ready" errors surface through its health. No-op
// when no diagnostics reader is wired (fake/local mode) or project is unknown.
func (ah *appHandler) enrichEnvWithDiagnostics(ctx context.Context, appName string, env *domain.AppEnvironment) {
	if ah.diagnosticsReader == nil || env.ProjectName == "" {
		return
	}
	base := env.ProjectName + "-" + appName + "-" + env.EnvName
	for _, t := range []struct{ app, source string }{
		{base, "argocd"},
		{base + "-platform", "external-secrets"},
	} {
		diags, err := ah.diagnosticsReader.GetAppDiagnostics(ctx, t.app, t.source)
		if err != nil {
			// Don't silently drop a read failure (RBAC, throttling, API down) —
			// that would falsely present a broken env as having no problems.
			// Surface it as a warning so the operator knows status is unknown.
			env.Status.Diagnostics = append(env.Status.Diagnostics, domain.Diagnostic{
				Source: t.source,
				Level:  domain.DiagnosticWarning,
				Title:  "Delivery status unavailable",
				Detail: err.Error(),
				Hint:   "Could not read the ArgoCD Application status; the env's real health is unknown. Check suparShip's access to the ArgoCD namespace and that ArgoCD is reachable.",
			})
			continue
		}
		env.Status.Diagnostics = append(env.Status.Diagnostics, diags...)
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
		Values:           values,
		SecretRefs:       secretRefs,
		Components:       componentDTOs(app.Spec.Components),
		Addons:           addonDTOs(app.Spec.Addons),
		Environments:     envDTOs,
		ClusterOverrides: clusterOverridesDTO(app.Spec.EnvironmentDefaults),
		RawValues:        app.Spec.RawValues,
		EnvRawValues:     envRawValuesDTO(app.Spec.EnvironmentDefaults),
		ComponentConfigs: componentConfigsDTO(app.Spec.Components),
		EnvComponents:    envComponentsDTO(app.Spec.EnvironmentDefaults),
	}
}

// applyComponentConfig writes per-component config (resources, envFrom, scaling,
// env) onto an app-level ComponentSpec. Env replaces the component's Config.
func applyComponentConfig(spec *domain.ComponentSpec, cfg domain.ComponentConfig) {
	spec.Resources = cfg.Resources
	spec.EnvFromSecrets = cfg.EnvFromSecrets
	spec.EnvFromConfigMaps = cfg.EnvFromConfigMaps
	spec.Scaling = cfg.Scaling
	if cfg.Env != nil {
		spec.Config = cfg.Env
	}
}

// componentConfigsDTO projects each ComponentSpec into the editable
// ComponentConfig shape (env mirrors the spec's Config). Returns nil when there
// are no components.
func componentConfigsDTO(specs []domain.ComponentSpec) map[string]domain.ComponentConfig {
	if len(specs) == 0 {
		return nil
	}
	out := make(map[string]domain.ComponentConfig, len(specs))
	for _, c := range specs {
		out[c.Name] = domain.ComponentConfig{
			Resources:         c.Resources,
			EnvFromSecrets:    c.EnvFromSecrets,
			EnvFromConfigMaps: c.EnvFromConfigMaps,
			Scaling:           c.Scaling,
			Env:               c.Config,
		}
	}
	return out
}

// envComponentsDTO extracts per-(env, component) overrides from
// EnvironmentDefaults. Returns nil when no env has any.
func envComponentsDTO(defaults map[string]domain.EnvironmentOverride) map[string]map[string]domain.ComponentConfig {
	var out map[string]map[string]domain.ComponentConfig
	for envName, ov := range defaults {
		if len(ov.Components) == 0 {
			continue
		}
		if out == nil {
			out = map[string]map[string]domain.ComponentConfig{}
		}
		out[envName] = ov.Components
	}
	return out
}

// envRawValuesDTO extracts per-env raw-values overlays from EnvironmentDefaults,
// keyed by env name. Returns nil when no env has one.
func envRawValuesDTO(defaults map[string]domain.EnvironmentOverride) map[string]map[string]any {
	var out map[string]map[string]any
	for envName, ov := range defaults {
		if len(ov.RawValues) == 0 {
			continue
		}
		if out == nil {
			out = map[string]map[string]any{}
		}
		out[envName] = ov.RawValues
	}
	return out
}

// clusterOverridesDTO extracts the per-(env, cluster) value overrides from the
// app's EnvironmentDefaults into the env → cluster map the API exposes. Returns
// nil when no env has any override.
func clusterOverridesDTO(defaults map[string]domain.EnvironmentOverride) map[string]map[string]domain.ClusterValueOverride {
	var out map[string]map[string]domain.ClusterValueOverride
	for envName, ov := range defaults {
		if len(ov.ClusterOverrides) == 0 {
			continue
		}
		if out == nil {
			out = map[string]map[string]domain.ClusterValueOverride{}
		}
		out[envName] = ov.ClusterOverrides
	}
	return out
}

func addonDTOs(addons []domain.AddonSpec) []AddonClaimDTO {
	dtos := make([]AddonClaimDTO, 0, len(addons))
	for _, a := range addons {
		dtos = append(dtos, AddonClaimDTO{
			Name:    a.Name,
			Type:    a.Type,
			Size:    a.Size,
			Version: a.Version,
			Values:  a.Values,
		})
	}
	return dtos
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
	dto := AppStatusSummaryDTO{
		Phase:        s.Phase,
		Replicas:     s.Replicas,
		Available:    s.Available,
		LastDeployed: s.LastDeployed,
	}
	for _, d := range s.Diagnostics {
		dto.Diagnostics = append(dto.Diagnostics, DiagnosticDTO{
			Source: d.Source,
			Level:  string(d.Level),
			Title:  d.Title,
			Detail: d.Detail,
			Hint:   d.Hint,
		})
	}
	return dto
}

func componentDTOs(components []domain.ComponentSpec) []ComponentSummaryDTO {
	dtos := make([]ComponentSummaryDTO, 0, len(components))
	for _, c := range components {
		dtos = append(dtos, ComponentSummaryDTO{
			Name:           c.Name,
			Type:           string(c.Type),
			Enabled:        c.Enabled,
			ExposeMode:     string(c.ExposeMode),
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

	history, err := ah.deploymentHistoryReader.GetAppDeploymentHistory(r.Context(), projectName, appName, envName)
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
