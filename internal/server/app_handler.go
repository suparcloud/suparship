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
	"github.com/suparcloud/suparship/internal/audit"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/registry"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/secrets"
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
	// vault is the org-configured secret store. Optional — when set, app rename
	// empties the old app's app-tier vault items best-effort. nil skips that.
	vault secrets.VaultStore
	// stackStore resolves an app's stack (Spec.Stack) so a shared-namespace stack
	// co-locates its apps. Optional — nil → apps never use a shared stack namespace.
	stackStore domain.StackStore
	// registryStore, when set, provisions the Kargo image-credential Secret in
	// the app's Kargo Project namespace after publish so Warehouses can pull tags.
	registryStore *registry.Store
	// gitopsConfigStore, when set, provisions the Kargo git-credential Secret in
	// the app's Kargo Project namespace so promotion git-clone/push steps auth.
	gitopsConfigStore *gitops.ConfigStore
	// auditor records app lifecycle events (create/delete/rename/promote).
	// Defaults to a Nop when unset.
	auditor audit.Auditor
}

// ensureKargoProjectCreds provisions/refreshes both Kargo credential Secrets in
// the project's kargo-{project} namespace: the image cred (Warehouse tag
// discovery) and the git cred (promotion git-clone/push). The cred path also
// ensures+labels the kargo-{project} namespace as a Kargo Project namespace.
// Best-effort: failures are logged, never surfaced (publish already succeeded).
// No-op when the respective store is not wired or the source config is
// disabled/unconfigured.
func (ah *appHandler) ensureKargoProjectCreds(ctx context.Context, projectName string) {
	ns := gitops.KargoNamespaceForProject(projectName)
	if ah.registryStore != nil {
		if err := ah.registryStore.EnsureKargoCred(ctx, ns); err != nil {
			slog.Warn("ensure kargo image cred", "project", projectName, "namespace", ns, "err", err)
		}
	}
	if ah.gitopsConfigStore != nil {
		if err := ah.gitopsConfigStore.EnsureKargoGitCred(ctx, ns); err != nil {
			slog.Warn("ensure kargo git cred", "project", projectName, "namespace", ns, "err", err)
		}
	}
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
//
// A cluster-fetch failure degrades to the built-ins (matching the gallery) but
// is logged via the default logger — otherwise a transient blip surfaces to the
// user as a misleading "template not found" with no server-side trace.
func (ah *appHandler) lookupTemplate(ctx context.Context, name string) (*tpl.Template, bool) {
	byName, err := ResolveTemplates(ctx, ah.builtin, ah.clusterLoader)
	if err != nil {
		slog.Warn("app: cluster template fetch failed; using built-ins only", "err", err)
	}
	t, ok := byName[name]
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
			Name:       c.Name,
			Type:       ct,
			Enabled:    c.Enabled,
			ExposeMode: mode,
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

	// A CD-managed app must have an image for Kargo to watch; reject early
	// rather than publishing a placeholder Warehouse that never pulls.
	if req.CD != nil && req.CD.Managed {
		if err := ah.validateCDImageSource(r.Context(), tmpl.Metadata.Name, tmpl, values); err != nil {
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
		CD:                 cdConfigFromDTO(req.CD),
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
		ah.ensureAppNamespaces(r.Context(), result.App, result.Environments)
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
			ah.ensureKargoProjectCreds(r.Context(), projectName)
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

	recordAudit(r.Context(), ah.auditor, "app.create", projectName, req.Name, audit.ResultSuccess,
		map[string]string{"template": req.Template})

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
	prevCD := app.Spec.CD
	prevPreviewsEnabled := app.Spec.PreviewsEnabled

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
		// Only enforce template inputs when values are actually provided — the
		// values-editor-first flow sends none (see creator.go). An empty values
		// map (explicit clear) is allowed without re-validating required inputs.
		if len(newValues) > 0 {
			if err := project.ValidateAppInputs(newValues, secretRefs, tmpl); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
				return
			}
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
	if req.CD != nil {
		cd := cdConfigFromDTO(req.CD)
		// Enabling CD-managed tag ownership requires a watchable image source
		// (template/override Images or app image_repository); reject otherwise so
		// we never publish a placeholder Warehouse that silently never promotes.
		// Validate before mutating the spec so a rejection leaves the app untouched.
		if cd.Managed {
			// Best-effort template resolve: a nil tmpl still validates against the
			// app's own image_repository and the template-name-keyed override.
			tmpl, _ := ah.lookupTemplate(r.Context(), app.Spec.Template.Name)
			if err := ah.validateCDImageSource(r.Context(), app.Spec.Template.Name, tmpl, app.Spec.Values); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
				return
			}
		}
		app.Spec.CD = cd
	}
	if req.PreviewsEnabled != nil {
		app.Spec.PreviewsEnabled = *req.PreviewsEnabled
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
		ah.ensureAppNamespaces(r.Context(), app, stableEnvs)
		if err := ah.gitOpsPublisher.PublishApp(r.Context(), app, stableEnvs); err != nil {
			app.Spec.Values, app.Spec.DisplayName, app.Spec.Description = prevValues, prevDisplay, prevDesc
			app.Spec.EnvironmentDefaults = prevEnvDefaults
			app.Spec.RawValues = prevRawValues
			app.Spec.Components = prevComponents
			app.Spec.CD = prevCD
			app.Spec.PreviewsEnabled = prevPreviewsEnabled
			_ = ah.appStore.SaveApp(r.Context(), projectName, app)
			slog.Error("update-app: publish failed; rolled back config change",
				"project", projectName, "app", appName, "err", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{
				Error: "publish failed; config change rolled back: " + err.Error(),
			})
			return
		}
		ah.ensureKargoProjectCreds(r.Context(), projectName)
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

	recordAudit(r.Context(), ah.auditor, "app.delete", projectName, appName, audit.ResultSuccess, nil)

	w.WriteHeader(http.StatusNoContent)
}

// renameAppRequest is the body for POST /api/v1/projects/{project}/apps/{app}/rename.
type renameAppRequest struct {
	NewName string `json:"newName"`
}

// handleRenameApp handles POST /api/v1/projects/{project}/apps/{app}/rename.
//
// An app name is the identity key across the store, gitops, ArgoCD/Kargo,
// namespaces, and secret items, so a rename is a recreate-under-the-new-name
// followed by teardown of the old. To minimise downtime for a live app the new
// name is created + published first (the old keeps running), then the old is
// torn down. App-tier secret VALUES cannot be migrated (write-only vault), so
// they must be re-entered under the new name — the UI warns.
func (ah *appHandler) handleRenameApp(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	oldName := r.PathValue("app")

	var req renameAppRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	newName := strings.TrimSpace(req.NewName)
	if newName == oldName {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "new name is the same as the current name"})
		return
	}
	if err := domain.ValidateAppName(newName); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	oldApp, err := ah.appStore.GetApp(r.Context(), projectName, oldName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + oldName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}
	if _, err := ah.appStore.GetApp(r.Context(), projectName, newName); err == nil {
		writeJSON(w, http.StatusConflict, errorResponse{
			Error: "app \"" + newName + "\" already exists in project \"" + projectName + "\"",
		})
		return
	}

	oldEnvs, _ := ah.appStore.ListAppEnvironments(r.Context(), projectName, oldName)

	// Build the renamed app + envs. Spec is carried over verbatim; only the
	// identity (name) and derived namespaces change.
	newApp := *oldApp
	newApp.Name = newName
	newEnvs := make([]*domain.AppEnvironment, 0, len(oldEnvs))
	for _, oldEnv := range oldEnvs {
		ne := *oldEnv
		ne.AppName = newName
		ne.Namespace = ""                                                    // recomputed below
		ne.Status = domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed} // fresh; live status refreshes
		ne.URLs = []string{}
		ne.Release = nil
		newEnvs = append(newEnvs, &ne)
	}
	ah.resolveEnvNamespaces(r.Context(), &newApp, newEnvs)

	// Persist the new app + envs.
	if err := ah.appStore.SaveApp(r.Context(), projectName, &newApp); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save renamed app"})
		return
	}
	for _, env := range newEnvs {
		if err := ah.appStore.SaveAppEnvironment(r.Context(), projectName, env); err != nil {
			slog.Warn("rename: failed to persist env for new app", "app", newName, "env", env.EnvName, "error", err)
		}
	}

	// Create-then-teardown: stand up the new name, then reclaim the old.
	ah.ensureAppNamespaces(r.Context(), &newApp, newEnvs)
	if ah.gitOpsPublisher != nil {
		if err := ah.gitOpsPublisher.PublishApp(r.Context(), &newApp, newEnvs); err != nil {
			// Roll back the new app so a failed rename leaves the old one intact.
			_ = ah.appStore.DeleteApp(r.Context(), projectName, newName)
			slog.Error("rename: publish of new app failed — rolled back, old app left intact",
				"project", projectName, "old", oldName, "new", newName, "error", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to publish renamed app: " + err.Error()})
			return
		}
	}

	// Teardown of the old app — best-effort; the rename has already succeeded.
	if ah.gitOpsPublisher != nil {
		if err := ah.gitOpsPublisher.UnpublishApp(r.Context(), projectName, oldName); err != nil {
			slog.Error("rename: gitops unpublish of old app failed", "project", projectName, "app", oldName, "error", err)
		}
	}
	if err := ah.appStore.DeleteApp(r.Context(), projectName, oldName); err != nil {
		slog.Error("rename: failed to delete old app from store", "project", projectName, "app", oldName, "error", err)
	}
	ah.deleteOldAppNamespaces(r.Context(), projectName, oldEnvs)
	ah.emptyAppVaultItems(r.Context(), projectName, oldName, oldEnvs)

	slog.Info("renamed app", "project", projectName, "old", oldName, "new", newName)
	recordAudit(r.Context(), ah.auditor, "app.rename", projectName, newName, audit.ResultSuccess,
		map[string]string{"from": oldName})
	writeJSON(w, http.StatusOK, AppDetailResponse{App: appToDetailDTO(&newApp, newEnvs)})
}

// republishApp re-resolves the app's stable environments and republishes them.
// Used when a higher layer changes the app's effective config (e.g. a stack
// override or stack membership change). Best-effort; no-op without a publisher.
func (ah *appHandler) republishApp(ctx context.Context, app *domain.App) error {
	if ah.gitOpsPublisher == nil {
		return nil
	}
	envs, _ := ah.appStore.ListAppEnvironments(ctx, app.ProjectName, app.Name)
	var stable []*domain.AppEnvironment
	for _, e := range envs {
		if e.EnvType != domain.AppEnvPreview {
			stable = append(stable, e)
		}
	}
	if len(stable) == 0 {
		stable = ah.stableEnvsFromOrg(ctx, app)
	}
	ah.resolveEnvNamespaces(ctx, app, stable)
	ah.ensureAppNamespaces(ctx, app, stable)
	return ah.gitOpsPublisher.PublishApp(ctx, app, stable)
}

// copyAppAs creates a copy of src under newName, reassigned to stackName,
// recomputes its stable-env namespaces, persists, ensures the namespaces, and
// publishes. Unlike rename it does NOT tear down the source — the source stack
// and its apps stay intact (this is the recreate half of the rename path, used
// by stack clone). Preview envs are skipped (ephemeral). App-tier secret VALUES
// are not migrated (write-only vault): the clone's app secrets start empty and
// must be re-entered. Rolls the copy back if the publish fails.
func (ah *appHandler) copyAppAs(ctx context.Context, src *domain.App, newName, stackName string) error {
	if _, err := ah.appStore.GetApp(ctx, src.ProjectName, newName); err == nil {
		return fmt.Errorf("app %q already exists in project %q", newName, src.ProjectName)
	}
	oldEnvs, _ := ah.appStore.ListAppEnvironments(ctx, src.ProjectName, src.Name)

	newApp := *src
	newApp.Name = newName
	newApp.Spec.Stack = stackName
	newEnvs := make([]*domain.AppEnvironment, 0, len(oldEnvs))
	for _, oldEnv := range oldEnvs {
		if oldEnv.EnvType == domain.AppEnvPreview {
			continue // previews are ephemeral — don't clone them
		}
		ne := *oldEnv
		ne.AppName = newName
		ne.Namespace = ""                                                    // recomputed below
		ne.Status = domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed} // fresh; live status refreshes
		ne.URLs = []string{}
		ne.Release = nil
		newEnvs = append(newEnvs, &ne)
	}
	ah.resolveEnvNamespaces(ctx, &newApp, newEnvs)

	if err := ah.appStore.SaveApp(ctx, src.ProjectName, &newApp); err != nil {
		return fmt.Errorf("save cloned app: %w", err)
	}
	for _, env := range newEnvs {
		if err := ah.appStore.SaveAppEnvironment(ctx, src.ProjectName, env); err != nil {
			slog.Warn("clone: persist env failed", "app", newName, "env", env.EnvName, "err", err)
		}
	}
	ah.ensureAppNamespaces(ctx, &newApp, newEnvs)
	if ah.gitOpsPublisher != nil {
		if err := ah.gitOpsPublisher.PublishApp(ctx, &newApp, newEnvs); err != nil {
			_ = ah.appStore.DeleteApp(ctx, src.ProjectName, newName) // roll back so a failed clone leaves no half-app
			return fmt.Errorf("publish cloned app: %w", err)
		}
	}
	return nil
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

	detail := appToDetailDTO(app, envs)
	// BYO/passthrough apps don't use the canonical component model — the chart
	// owns its workloads — so the declared "components" topology is meaningless
	// (older apps were created with a phantom "web" entry before this was
	// fixed). Suppress it so the UI's Runtime-components panel disappears.
	if t, ok := ah.lookupTemplate(r.Context(), app.Spec.Template.Name); ok && !t.Spec.CanonicalValues() {
		detail.Components = []ComponentSummaryDTO{} // empty (not nil) → JSON [], UI renders nothing
	}

	writeJSON(w, http.StatusOK, AppDetailResponse{App: detail})
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

	// Resolve the base env the preview clones: the requested baseEnv when given
	// (must be a real stable env), else the first stable env by promotion order
	// (conventionally staging). The preview reuses this env's cluster + vault.
	stableEnvs := ah.stableEnvsFromOrg(r.Context(), a)
	baseEnv := req.BaseEnv
	if baseEnv == "" {
		if len(stableEnvs) > 0 {
			baseEnv = stableEnvs[0].EnvName
		}
	} else {
		valid := false
		for _, e := range stableEnvs {
			if e.EnvName == baseEnv {
				valid = true
				break
			}
		}
		if !valid {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
				Error: "invalid baseEnv \"" + baseEnv + "\": not a stable environment of this app",
			})
			return
		}
	}
	if baseEnv == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "no stable environment available to base the preview on",
		})
		return
	}

	// Run the full preview creation pipeline: EnvironmentInstance + Helm values
	// + ArgoCD Application. Errors when the app has previews disabled or no
	// enabled components.
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

	// Publish the preview to GitOps (values + ConfigMap + ExternalSecret reusing
	// the base env vault). Roll back the saved env on failure so a retry is clean.
	if ah.gitOpsPublisher != nil {
		if err := ah.gitOpsPublisher.PublishAppPreview(r.Context(), a, previewResult.Instance, baseEnv); err != nil {
			_ = ah.appStore.DeleteAppEnvironment(r.Context(), projectName, appName, sanitized)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to publish preview: " + err.Error()})
			return
		}
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
	ah.ensureAppNamespaces(r.Context(), app, stableEnvs)
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

	ah.ensureAppNamespaces(r.Context(), app, stableEnvs)
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

	// Resolve the app's stack once: a shared-namespace stack co-locates its apps.
	var stackName, stackPattern string
	var stackShared bool
	if app.Spec.Stack != "" && ah.stackStore != nil {
		if st, err := ah.stackStore.GetStack(ctx, app.ProjectName, app.Spec.Stack); err == nil && st != nil && st.Spec.SharedNamespace {
			stackName = st.Name
			stackShared = true
			stackPattern = st.Spec.NamespacePattern
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
			StackName:         stackName,
			StackShared:       stackShared,
			StackPattern:      stackPattern,
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

	resp, err := ah.promoteAppEnv(r.Context(), projectName, appName, req.TargetEnvironment)
	if err != nil {
		writeJSON(w, statusForPromoteErr(err), errorResponse{Error: err.Error()})
		return
	}
	recordAudit(r.Context(), ah.auditor, "app.promote", projectName, appName, audit.ResultSuccess,
		map[string]string{"to": req.TargetEnvironment})
	writeJSON(w, http.StatusOK, resp)
}

// Sentinels let the per-app HTTP handler map promoteAppEnv failures back to the
// status codes it returned before the core was extracted; the stack batch path
// just surfaces err.Error() per app.
var (
	errPromoteAppNotFound = errors.New("app not found")
	errPromoteBadRequest  = errors.New("invalid promotion")
)

// statusForPromoteErr maps a promoteAppEnv error to an HTTP status.
func statusForPromoteErr(err error) int {
	switch {
	case errors.Is(err, errPromoteAppNotFound):
		return http.StatusNotFound
	case errors.Is(err, errPromoteBadRequest), errors.Is(err, domainapp.ErrNoRelease):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// promoteAppEnv promotes a single app to targetEnvName: it resolves the source
// env, publishes the target env's GitOps files (best-effort), then either
// creates a Kargo Promotion CR (when Kargo is wired) or copies the release
// bundle in the local store. It is the shared core of the per-app promote
// handler and the stack batch promote. Returns the populated response.
func (ah *appHandler) promoteAppEnv(ctx context.Context, projectName, appName, targetEnvName string) (*AppPromoteResponse, error) {
	app, err := ah.appStore.GetApp(ctx, projectName, appName)
	if err != nil {
		return nil, fmt.Errorf("%w: app %q not found in project %q", errPromoteAppNotFound, appName, projectName)
	}

	targetEnv, err := ah.appStore.GetAppEnvironment(ctx, projectName, appName, targetEnvName)
	if err != nil {
		return nil, fmt.Errorf("%w: environment %q not found for app %q", errPromoteBadRequest, targetEnvName, appName)
	}
	if targetEnv.EnvType == domain.AppEnvPreview {
		return nil, fmt.Errorf("%w: cannot promote to a preview environment", errPromoteBadRequest)
	}

	// Resolve the source: the stable env with the highest Order strictly below
	// the target's Order (closest predecessor). Falls back to preview envs when
	// no stable predecessor exists.
	sourceEnv, err := ah.findPromotionSource(ctx, projectName, appName, targetEnv)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errPromoteBadRequest, err.Error())
	}

	// Write GitOps files for the target environment before triggering the
	// promotion. This ensures ArgoCD can find app.yaml + values.yaml when
	// Kargo (or the store fallback) signals a sync. Best-effort: a publish
	// failure is logged but does not abort the promotion.
	if ah.gitOpsPublisher != nil {
		ah.ensureAppNamespace(ctx, app, targetEnv)
		if pubErr := ah.gitOpsPublisher.PublishAppEnv(ctx, app, targetEnv); pubErr != nil {
			slog.Warn("promote: failed to publish env files — proceeding with promotion",
				"project", projectName, "app", appName,
				"env", targetEnvName, "err", pubErr)
		}
	}

	// When Kargo is configured, create a Kargo Promotion CR. The Promotion CR
	// drives the actual release copy through the Kargo pipeline; suparship
	// then returns the Promotion details rather than the local release copy.
	if ah.kargoPromoter != nil {
		kargoResult, err := ah.kargoPromoter.CreatePromotion(ctx, projectName, appName, sourceEnv.EnvName, targetEnvName)
		if err != nil {
			slog.Error("kargo promotion failed",
				"project", projectName, "app", appName,
				"from", sourceEnv.EnvName, "to", targetEnvName, "error", err)
			return nil, fmt.Errorf("failed to create Kargo promotion: %w", err)
		}
		slog.Info("kargo promotion created",
			"promotion", kargoResult.Name, "stage", kargoResult.Stage, "freight", kargoResult.Freight)
		return &AppPromoteResponse{
			Project:     projectName,
			App:         appName,
			Source:      sourceEnv.EnvName,
			Destination: targetEnvName,
			Namespace:   targetEnv.Namespace,
			Mechanism:   "kargo",
			Message:     fmt.Sprintf("Kargo promotion %q created — freight %q is being promoted to %s", kargoResult.Name, kargoResult.Freight, targetEnvName),
			KargoPromotion: &KargoPromotionDTO{
				Name:    kargoResult.Name,
				Stage:   kargoResult.Stage,
				Freight: kargoResult.Freight,
				Phase:   kargoResult.Phase,
			},
		}, nil
	}

	// Fallback: copy the release bundle directly in the store — no Kargo pipeline.
	// This is the expected path for local/dev installs without Kargo. When a
	// GitOps publisher IS configured, though, a missing Kargo promoter usually
	// means a misconfigured install: the copy won't flow through the CD pipeline,
	// so warn loudly rather than let a "successful" promotion mislead.
	if ah.gitOpsPublisher != nil {
		slog.Warn("promote: no Kargo promoter wired on a GitOps-enabled install — performing a direct in-store release copy that does NOT flow through the CD pipeline; wire Kargo for pipeline-driven promotion",
			"project", projectName, "app", appName, "from", sourceEnv.EnvName, "to", targetEnvName)
	}
	result, err := domainapp.Promote(ctx, ah.appStore, domainapp.PromoteRequest{
		ProjectName: projectName,
		AppName:     appName,
		FromEnv:     sourceEnv.EnvName,
		ToEnv:       targetEnvName,
	})
	if err != nil {
		if errors.Is(err, domainapp.ErrNoRelease) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to promote app: %w", err)
	}

	return &AppPromoteResponse{
		Project:     projectName,
		App:         appName,
		Source:      sourceEnv.EnvName,
		Destination: targetEnvName,
		Namespace:   targetEnv.Namespace,
		Mechanism:   "in-store",
		Message:     "Promoted " + appName + " from " + sourceEnv.EnvName + " to " + targetEnvName + " (direct in-store release copy — no Kargo pipeline configured)",
		Release: &AppReleaseRefDTO{
			Image:  result.Release.Image,
			Tag:    result.Release.Tag,
			Commit: result.Release.Commit,
		},
	}, nil
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

	// instance is the Helm release name suparship sets on every app
	// Application/ApplicationSet (ReleaseName: app.Name), which Helm stamps as
	// the app.kubernetes.io/instance label on every rendered resource. It's the
	// handle the label-based discovery selects on, so BYO charts (whose
	// Deployments are named by their own fullname template, not the app name)
	// still report real status. NOTE: this is the app name, NOT the ArgoCD
	// Application name {project}-{app}-{env}.
	instance := appName

	// No remote routing (single-cluster / fake mode): query the local provider.
	if !routed {
		if ah.runtimeProvider == nil {
			return
		}
		// Prefer label-based app-native discovery when the provider is a real
		// K8s provider; fakes/tests only implement the name-based interface.
		if kp, ok := ah.runtimeProvider.(*runtime.K8sProvider); ok {
			if info, err := kp.GetAppRuntime(ctx, env.Namespace, instance, appName); err == nil {
				ah.applyRuntimeInfo(env, info)
			}
			return
		}
		if info, err := ah.runtimeProvider.GetServiceRuntime(ctx, env.Namespace, appName); err == nil {
			ah.applyRuntimeInfo(env, info)
		}
		return
	}

	for _, name := range unreachable {
		env.Status.Diagnostics = append(env.Status.Diagnostics, domain.Diagnostic{
			Source:  "runtime",
			Level:   domain.DiagnosticWarning,
			Title:   "Workload cluster unreachable",
			Detail:  fmt.Sprintf("cluster %q could not be reached", name),
			Hint:    "Live replica count for this cluster is unavailable (ArgoCD may still show it Healthy). Check the cluster's kubeconfig/credentials under Clusters and that its API server is reachable from suparShip.",
			Cluster: name,
		})
	}
	if len(clients) == 0 {
		return // all targets unreachable; diagnostics above explain
	}

	// Aggregate across reachable clusters: worst-of phase, summed replicas.
	agg := &runtime.RuntimeInfo{Status: runtime.StatusHealthy}
	got := false
	for _, nc := range clients {
		info, err := runtime.NewK8sProvider(nc.client).GetAppRuntime(ctx, env.Namespace, instance, appName)
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
				Source:  "runtime",
				Level:   domain.DiagnosticInfo,
				Title:   fmt.Sprintf("Cluster %s: %s", nc.name, info.Status),
				Detail:  fmt.Sprintf("%d/%d replicas available", info.Available, info.Replicas),
				Cluster: nc.name,
			})
		}
	}
	if !got {
		return
	}
	ah.applyRuntimeInfo(env, agg)
}

// argoClusterApp pairs a destination cluster with the app's ArgoCD Application
// name on that cluster (the chart Application; its platform companion is
// name+"-platform").
type argoClusterApp struct {
	cluster string
	name    string
}

// argoAppNamesForEnv resolves the per-cluster ArgoCD Application names for an app
// in an env: the org ArgoAppName pattern rendered once per deploy-target cluster.
// A single-cluster env yields one entry; a fan-out ("all") env yields one per
// cluster, so status/diagnostics can read every cluster's Application. Falls back
// to a single "in-cluster" entry when the org or its clusters can't be resolved,
// mirroring the publisher's gitops.appSetClusterTargets fallback.
func (ah *appHandler) argoAppNamesForEnv(ctx context.Context, projectName, appName, envName string) []argoClusterApp {
	var pattern string
	var clusters []string
	if ah.orgProvider != nil {
		if org, err := ah.orgProvider.GetOrg(ctx); err == nil && org != nil {
			pattern = org.ResourceNaming.EffectiveArgoAppName()
			for _, e := range org.Environments {
				if e.Name == envName {
					clusters = e.ResolveDeployTargets()
					break
				}
			}
		}
	}
	if len(clusters) == 0 {
		clusters = []string{"in-cluster"}
	}
	out := make([]argoClusterApp, 0, len(clusters))
	for _, c := range clusters {
		out = append(out, argoClusterApp{
			cluster: c,
			name:    gitops.RenderArgoAppName(pattern, projectName, appName, envName, c),
		})
	}
	return out
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
// a stuck or "not deployed" env explains itself. For a fan-out ("all") env it
// reads EVERY cluster's Application — so a failed sync on a secondary cluster is
// surfaced, not just the active one — tagging each diagnostic with its cluster.
// For each cluster it reads both the chart Application and its platform companion
// (name+"-platform"), the latter owning the ConfigMap + ExternalSecret so ESO
// "not ready" errors surface through its health. No-op when no diagnostics reader
// is wired (fake/local mode) or project is unknown.
func (ah *appHandler) enrichEnvWithDiagnostics(ctx context.Context, appName string, env *domain.AppEnvironment) {
	if ah.diagnosticsReader == nil || env.ProjectName == "" {
		return
	}
	apps := ah.argoAppNamesForEnv(ctx, env.ProjectName, appName, env.EnvName)
	// Only tag diagnostics with a cluster when the env actually fans out, to keep
	// single-cluster output unchanged.
	multi := len(apps) > 1
	for _, ca := range apps {
		cluster := ""
		if multi {
			cluster = ca.cluster
		}
		for _, t := range []struct{ app, source string }{
			{ca.name, "argocd"},
			{ca.name + "-platform", "external-secrets"},
		} {
			diags, err := ah.diagnosticsReader.GetAppDiagnostics(ctx, t.app, t.source)
			if err != nil {
				// Don't silently drop a read failure (RBAC, throttling, API down) —
				// that would falsely present a broken env as having no problems.
				// Surface it as a warning so the operator knows status is unknown.
				env.Status.Diagnostics = append(env.Status.Diagnostics, domain.Diagnostic{
					Source:  t.source,
					Level:   domain.DiagnosticWarning,
					Title:   "Delivery status unavailable",
					Detail:  err.Error(),
					Hint:    "Could not read the ArgoCD Application status; the env's real health is unknown. Check suparShip's access to the ArgoCD namespace and that ArgoCD is reachable.",
					Cluster: cluster,
				})
				continue
			}
			for i := range diags {
				diags[i].Cluster = cluster
			}
			env.Status.Diagnostics = append(env.Status.Diagnostics, diags...)
		}
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

// cdConfigFromDTO converts the optional wire CD config into the domain type.
// A nil DTO yields the zero CDConfig (external-CD ownership disabled).
func cdConfigFromDTO(dto *CDConfigDTO) domain.CDConfig {
	if dto == nil {
		return domain.CDConfig{}
	}
	return domain.CDConfig{Managed: dto.Managed}
}

// validateCDImageSource rejects enabling CD-managed tag ownership (cd.managed) on
// an app that has no image for Kargo to watch. Without an Images mapping on the
// template (or a sync-safe override set from the template UI for a BYO chart),
// and without an app-level image_repository, publish would seed a placeholder
// ghcr.io/{project}/{app} Warehouse that never pulls — so the CD pipeline would
// silently never promote. This mirrors the publisher's resolveKargoImages
// precedence (override images → template images → image_repository) and catches
// the gap at the API so the operator gets immediate, actionable feedback rather
// than a quietly broken Warehouse.
func (ah *appHandler) validateCDImageSource(ctx context.Context, templateName string, tmpl *tpl.Template, values map[string]any) error {
	// An app-level image_repository is a concrete source on its own — checkable
	// without resolving the template, so this still guards correctly when the
	// template loader is unavailable.
	if repo, ok := values["image_repository"].(string); ok && strings.TrimSpace(repo) != "" {
		return nil
	}
	if tmpl != nil && len(tmpl.Spec.Images) > 0 {
		return nil
	}
	// A sync-safe override mapping replaces / substitutes for the template's own
	// Images at publish (keyed by template name), so honour it here too.
	if ah.kubeClient != nil && templateName != "" {
		if ov, err := kube.LoadTemplateOverride(ctx, ah.kubeClient, templateName); err == nil && ov != nil && len(ov.Images) > 0 {
			return nil
		}
	}
	return fmt.Errorf("continuous delivery (cd.managed) needs an image for Kargo to watch: declare an Images mapping on template %q (Template detail → Images) or set image_repository on the app", templateName)
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
		CD:               CDConfigDTO{Managed: app.Spec.CD.Managed},
		PreviewsEnabled:  app.Spec.PreviewsEnabled,
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
			Source:  d.Source,
			Level:   string(d.Level),
			Title:   d.Title,
			Detail:  d.Detail,
			Hint:    d.Hint,
			Cluster: d.Cluster,
		})
	}
	return dto
}

func componentDTOs(components []domain.ComponentSpec) []ComponentSummaryDTO {
	dtos := make([]ComponentSummaryDTO, 0, len(components))
	for _, c := range components {
		dtos = append(dtos, ComponentSummaryDTO{
			Name:       c.Name,
			Type:       string(c.Type),
			Enabled:    c.Enabled,
			ExposeMode: string(c.ExposeMode),
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
		// No Kargo wired (e.g. local/dev). Degrade gracefully: 200 with
		// available=false so the polling UI shows "unavailable" instead of
		// erroring on a repeated 501.
		writeJSON(w, http.StatusOK, KargoPromotionStatusResponse{Available: false})
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
		Available: true,
		Name:      result.Name,
		Stage:     result.Stage,
		Freight:   result.Freight,
		Phase:     result.Phase,
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
		// No Kargo wired: 200 with available=false so the pipeline bar degrades
		// to a plain env switcher instead of erroring on its 3s poll.
		writeJSON(w, http.StatusOK, KargoAppPipelineResponse{Available: false, Stages: []KargoStageStatusDTO{}})
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

	writeJSON(w, http.StatusOK, KargoAppPipelineResponse{Available: true, Stages: dtos})
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
		// No ArgoCD history reader wired (e.g. fake/local dev): 200 with
		// available=false so the UI shows "history unavailable" rather than an error.
		writeJSON(w, http.StatusOK, AppDeploymentHistoryResponse{Available: false, History: []AppDeploymentHistoryEntryDTO{}})
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
		Available:   true,
		Project:     projectName,
		App:         appName,
		Environment: envName,
		History:     dtos,
	})
}
