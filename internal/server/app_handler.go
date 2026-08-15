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
	"sync"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	domainapp "github.com/suparcloud/suparship/internal/app"
	"github.com/suparcloud/suparship/internal/audit"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
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
	// autoPromoteAttempts backs the auto-promotion reconciler's retry cooldown
	// (see auto_promote.go), keyed "{project}/{app}/{env}".
	autoPromoteMu       sync.Mutex
	autoPromoteAttempts map[string]autoPromoteAttempt
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
	argoAppGate             ArgoAppGate             // optional: refuses Kargo promotions until the target Application exists
	argoAppWaitTimeout      time.Duration           // how long promoteAppEnv polls the gate (defaulted in newAppHandler)
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
	// statusCache memoizes enriched env live-status for a short TTL so the list
	// path (dashboard/project/stack) doesn't re-hit K8s + ArgoCD on every load.
	// nil is a valid no-op cache (test handlers built as literals skip caching).
	statusCache *statusCache
	// templateVersionCache memoizes per-template archive listings for the app
	// detail page's upgrade hints, so a composed app doesn't re-LIST ConfigMaps
	// once per component on every render. nil is a valid no-op cache.
	templateVersionCache *templateVersionCache
	// async runs slow pin/unpin work in the background when a caller opts in
	// (Prefer: respond-async), returning 202 + a task id instead of blocking the
	// request on git round-trips. nil disables async (handlers stay synchronous).
	async *asyncRunner
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
		appStore:             store,
		builtin:              templates,
		clusterLoader:        clusterLoader,
		projectStore:         projectStore,
		statusCache:          newStatusCache(statusCacheTTL),
		templateVersionCache: newTemplateVersionCache(templateVersionsTTL),
		argoAppWaitTimeout:   defaultArgoAppWaitTimeout,
		autoPromoteAttempts:  map[string]autoPromoteAttempt{},
	}
}

// defaultArgoAppWaitTimeout bounds how long a promotion waits for ArgoCD to
// generate the target env's Application before giving up with a retryable
// error. Long enough to cover the common case (the Application already exists,
// or a webhook-triggered appset reconcile lands quickly); deliberately shorter
// than the ApplicationSet git generator's default requeue (~3 min) — a
// first-ever promotion to an env may need one retry, which beats holding an
// HTTP request open for minutes.
const defaultArgoAppWaitTimeout = 20 * time.Second

// templateDisabled reports whether the template is retired via its sync-safe
// override. Gates NEW app creation only — existing apps keep every other
// operation (values editing, publish, upgrade), because disabling means "stop
// offering this", not "break what's running on it". Fail-open on any read
// problem or when no cluster client is wired (fake mode): an unreadable
// override must not block creation.
func (ah *appHandler) templateDisabled(ctx context.Context, name string) bool {
	if ah.kubeClient == nil {
		return false
	}
	ov, err := kube.LoadTemplateOverride(ctx, ah.kubeClient, name)
	if err != nil || ov == nil {
		return false
	}
	return ov.Disabled
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
	// Reject a name that would collide with another app's ArgoCD Application name
	// once the project prefix is folded (e.g. "bar" when "foo-bar" exists in "foo").
	if other := ah.argoNameCollision(r.Context(), projectName, req.Name, ""); other != "" {
		writeJSON(w, http.StatusConflict, errorResponse{
			Error: "app \"" + req.Name + "\" would produce the same ArgoCD Application name as \"" + other + "\" (both resolve to \"" + secrets.DedupProjectPrefix(projectName, req.Name) + "\"); rename one",
		})
		return
	}

	// Convert secret refs from DTO to domain type for the Create pipeline.
	domainSecretRefs := make([]domain.AppSecretRef, len(req.SecretRefs))
	for i, s := range req.SecretRefs {
		domainSecretRefs[i] = domain.AppSecretRef{Name: s.Name, SecretRef: s.SecretRef}
	}

	// Build explicit component specs when the caller provides them. Each may
	// carry its OWN template (composed apps): resolve every referenced template
	// so an unknown one 400s here, and pin its version. When no component carries
	// a template, this is the legacy single-template path. When absent entirely,
	// Create initialises components from the app-level template.
	var explicitComponents []domain.ComponentSpec
	var firstComponentTemplate *tpl.Template
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
		cs := domain.ComponentSpec{
			Name:           c.Name,
			Type:           ct,
			Enabled:        c.Enabled,
			ExposeMode:     mode,
			Values:         c.Values,
			InheritAppVars: c.InheritAppVars,
			Images:         componentImagesFromDTO(c.Images),
			Stateful:       c.Stateful,
			PreviewEnabled: c.PreviewEnabled,
		}
		for _, e := range c.EnvVars {
			cs.EnvVars = append(cs.EnvVars, domain.ComponentEnvVar{
				Name:       e.Name,
				Value:      e.Value,
				FromConfig: e.FromConfig,
				FromSecret: e.FromSecret,
			})
		}
		if c.Template != nil && c.Template.Name != "" {
			ctmpl, ok := ah.lookupTemplate(r.Context(), c.Template.Name)
			if !ok {
				writeJSON(w, http.StatusBadRequest, errorResponse{
					Error: "components[" + itoa(i) + "]: template \"" + c.Template.Name + "\" not found",
				})
				return
			}
			if ah.templateDisabled(r.Context(), c.Template.Name) {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
					Error: "components[" + itoa(i) + "]: template \"" + c.Template.Name + "\" is disabled — an org admin can re-enable it under Templates",
				})
				return
			}
			version := c.Template.Version
			if version == "" {
				version = ctmpl.Metadata.Version
			}
			cs.Template = &domain.AppTemplateRef{Name: ctmpl.Metadata.Name, Version: version}
			if firstComponentTemplate == nil {
				firstComponentTemplate = ctmpl
			}
		}
		explicitComponents = append(explicitComponents, cs)
	}
	if len(explicitComponents) > 0 {
		if err := domain.ValidateComponents(explicitComponents); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
	}

	// Resolve the app-level template: the caller's req.Template when given, else
	// (a composed canvas app that names no app-level template) the first
	// component's template as the app "primary". This keeps AppSpec.Template
	// populated for readers while the publisher renders each component's own
	// chart; the composed branch ignores AppSpec.Template at publish.
	var tmpl *tpl.Template
	switch {
	case req.Template != "":
		var ok bool
		tmpl, ok = ah.lookupTemplate(r.Context(), req.Template)
		if !ok {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: "template \"" + req.Template + "\" not found",
			})
			return
		}
		if ah.templateDisabled(r.Context(), req.Template) {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
				Error: "template \"" + req.Template + "\" is disabled — an org admin can re-enable it under Templates",
			})
			return
		}
	case firstComponentTemplate != nil:
		tmpl = firstComponentTemplate
	default:
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "template name is required"})
		return
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

	imageBindings := appImageBindingsFromDTO(req.Images)
	if err := validateImageBindings(imageBindings); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	// Resolve the delivery mode: explicit request wins, else a sync-safe template
	// override, else the template's declared default, else pipeline. Direct apps
	// skip Kargo/promotion.
	deliveryMode := domain.DeliveryMode(strings.TrimSpace(req.DeliveryMode))
	if deliveryMode == "" && ah.kubeClient != nil {
		if ov, err := kube.LoadTemplateOverride(r.Context(), ah.kubeClient, tmpl.Metadata.Name); err == nil && ov != nil {
			deliveryMode = domain.DeliveryMode(strings.TrimSpace(ov.DeliveryMode))
		}
	}
	if deliveryMode == "" {
		deliveryMode = domain.DeliveryMode(strings.TrimSpace(tmpl.Spec.DeliveryMode))
	}

	// NB: CD-managed apps are NOT required to have an image source at creation.
	// Image discovery needs the app's effective values (the canonical base is only
	// computed for an existing app+env), so a canonical template's component images
	// can't be discovered or selected until the app exists. The operator selects
	// which images Kargo manages from the app's Overview after create, where
	// discovery is live; validateCDImageSource still guards that selection on the
	// edit path.

	// Non-secret env vars set at creation (create wizard "Environment variables").
	// Committed to Git, so they ride along in the create → single atomic publish.
	var envConfig envconfig.EnvConfig
	if req.EnvConfig != nil {
		envConfig = fromEnvConfigDTO(*req.EnvConfig)
	}
	var envConfigByEnv map[string]envconfig.EnvConfig
	if len(req.EnvConfigByEnv) > 0 {
		envConfigByEnv = make(map[string]envconfig.EnvConfig, len(req.EnvConfigByEnv))
		for env, dto := range req.EnvConfigByEnv {
			envConfigByEnv[env] = fromEnvConfigDTO(dto)
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
		NamespaceScope:     domain.NamespaceScope(req.NamespaceScope),
		NamespacePattern:   req.NamespacePattern,
		RawValues:          req.RawValues,
		ComponentConfigs:   req.ComponentConfigs,
		EnvComponents:      req.EnvComponents,
		EnvConfig:          envConfig,
		EnvConfigByEnv:     envConfigByEnv,
		CD:                 cdConfigFromDTO(req.CD),
		Images:             imageBindings,
		DeliveryMode:       deliveryMode,
	})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	// Fold the per-env cluster-targeting selection into the app's
	// EnvironmentDefaults (mirrors ClusterOverrides), rejecting any cluster that
	// is not registered on the env first.
	if req.TargetClusters != nil {
		if err := ah.validateTargetClusters(r.Context(), req.TargetClusters); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
		result.App.Spec.EnvironmentDefaults = foldTargetClusters(result.App.Spec.EnvironmentDefaults, req.TargetClusters)
	}

	// Fold per-(env, component) values overlays set at creation into
	// EnvironmentDefaults[env].ComponentValues — component values are per-env only
	// (no all-envs base at creation). Mirrors the update path; unknown component
	// names are rejected, empty overlays are skipped.
	if len(req.EnvComponentValues) > 0 {
		compNames := make(map[string]bool, len(result.App.Spec.Components))
		for _, c := range result.App.Spec.Components {
			compNames[c.Name] = true
		}
		ed := result.App.Spec.EnvironmentDefaults
		if ed == nil {
			ed = map[string]domain.EnvironmentOverride{}
		}
		for envName, byComp := range req.EnvComponentValues {
			ov := ed[envName]
			for name, vals := range byComp {
				if !compNames[name] {
					writeJSON(w, http.StatusBadRequest, errorResponse{Error: "unknown component: " + name})
					return
				}
				if len(vals) == 0 {
					continue
				}
				if ov.ComponentValues == nil {
					ov.ComponentValues = map[string]map[string]any{}
				}
				ov.ComponentValues[name] = vals
			}
			if len(ov.ComponentValues) == 0 {
				ov.ComponentValues = nil
			}
			ed[envName] = ov
		}
		result.App.Spec.EnvironmentDefaults = ed
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
	// Edit-composed: replace the component list (add / remove / retemplate).
	// Applied FIRST so the per-component config/values blocks below operate on the
	// new set. The publisher prunes the stale-mode tree if this flips composed↔single.
	if req.Components != nil {
		// Hand the resolver the current components so an edit preserves each
		// existing pin instead of re-pinning to whatever the registry now holds.
		prevByName := make(map[string]domain.ComponentSpec, len(prevComponents))
		for _, c := range prevComponents {
			prevByName[c.Name] = c
		}
		specs, _, err := ah.resolveComponentSpecs(r.Context(), req.Components, prevByName)
		if err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
		app.Spec.Components = specs
		// Keep AppSpec.Template (the "primary") in sync so single-component readers
		// and the single-source render path resolve the right chart. Mirrored from
		// the resolved component's PIN — reading it off the live template would
		// re-pin the app to latest on every unrelated component edit.
		app.Spec.SyncPrimaryTemplate()
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
	if req.TargetClusters != nil {
		// Reject any selection naming a cluster not registered on the env before
		// folding it into the app's per-env override record.
		if err := ah.validateTargetClusters(r.Context(), req.TargetClusters); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
		app.Spec.EnvironmentDefaults = foldTargetClusters(app.Spec.EnvironmentDefaults, req.TargetClusters)
	}
	if req.RawValues != nil {
		app.Spec.RawValues = *req.RawValues
	}
	// Apply the image selection before the CD check so cd.managed validates against
	// the selection in this same request.
	if req.Images != nil {
		bindings := appImageBindingsFromDTO(*req.Images)
		if err := validateImageBindings(bindings); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
		app.Spec.Images = bindings
	}
	// Per-component image selections from the app-level Images panel (composed apps).
	if req.ComponentImages != nil {
		for i := range app.Spec.Components {
			if imgs, ok := req.ComponentImages[app.Spec.Components[i].Name]; ok {
				app.Spec.Components[i].Images = componentImagesFromDTO(imgs)
			}
		}
	}
	// Per-component env-var settings from the variables drawer — a focused patch
	// so a variables tweak never resends (and can't clobber) component structure.
	if len(req.ComponentEnvVars) > 0 {
		known := make(map[string]bool, len(app.Spec.Components))
		for _, c := range app.Spec.Components {
			known[c.Name] = true
		}
		for name := range req.ComponentEnvVars {
			if !known[name] {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "unknown component: " + name})
				return
			}
		}
		// Apply on a COPY and validate before assigning — a rejected patch must
		// leave the (store-shared) spec untouched.
		next := append([]domain.ComponentSpec(nil), app.Spec.Components...)
		for i := range next {
			patch, ok := req.ComponentEnvVars[next[i].Name]
			if !ok {
				continue
			}
			c := &next[i]
			if patch.InheritAppVars != nil {
				v := *patch.InheritAppVars
				c.InheritAppVars = &v
			}
			if patch.EnvVars != nil {
				var evs []domain.ComponentEnvVar
				for _, e := range *patch.EnvVars {
					evs = append(evs, domain.ComponentEnvVar{
						Name:       e.Name,
						Value:      e.Value,
						FromConfig: e.FromConfig,
						FromSecret: e.FromSecret,
					})
				}
				c.EnvVars = evs
			}
		}
		if err := domain.ValidateComponents(next); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
		app.Spec.Components = next
	}
	if req.CD != nil {
		cd := cdConfigFromDTO(req.CD)
		// Preserve the image-config intent across a CD-only edit (managed/autoPromote
		// toggle) — the DTO doesn't carry it, so a bare replacement would silently
		// reset it and re-enable template auto-bind.
		cd.ImagesConfigured = app.Spec.CD.ImagesConfigured
		// Enabling CD-managed tag ownership requires a watchable image source;
		// reject otherwise so we never publish a Warehouse that silently never
		// promotes. Validate before mutating CD so a rejection leaves it as-is.
		// imagesConfigured is computed as it will be AFTER this request: a
		// same-request explicit selection (req.Images / req.ComponentImages,
		// already applied to app.Spec above) flips it below this block.
		if cd.Managed {
			imagesConfigured := app.Spec.CD.ImagesConfigured || req.Images != nil || req.ComponentImages != nil
			if err := ah.validateCDImageSource(r.Context(), app, imagesConfigured); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
				return
			}
		}
		app.Spec.CD = cd
	}
	// A submitted image selection (either shape) means the user has explicitly
	// reviewed CD image selection: from now on an empty selection means "watch
	// nothing", not "auto-bind the template defaults". Set after the CD block so it
	// survives a same-request CD replacement.
	if req.Images != nil || req.ComponentImages != nil {
		app.Spec.CD.ImagesConfigured = true
	}
	if dm := domain.DeliveryMode(strings.TrimSpace(req.DeliveryMode)); dm != "" {
		app.Spec.DeliveryMode = dm
	}
	if len(req.DeployEnvs) > 0 {
		if app.Spec.EnvironmentDefaults == nil {
			app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
		}
		for envName, deploy := range req.DeployEnvs {
			ov := app.Spec.EnvironmentDefaults[envName]
			d := deploy
			ov.Deploy = &d
			app.Spec.EnvironmentDefaults[envName] = ov
		}
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
	// Set of valid component names, used to reject unknown names in the
	// per-component values updates below.
	compNames := make(map[string]bool, len(app.Spec.Components))
	for _, c := range app.Spec.Components {
		compNames[c.Name] = true
	}
	if req.ComponentValues != nil {
		// Base (all-env) overlay per component. Only the named components change;
		// an empty overlay clears that component's base Values.
		for name, vals := range req.ComponentValues {
			if !compNames[name] {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "unknown component: " + name})
				return
			}
			for i := range app.Spec.Components {
				if app.Spec.Components[i].Name == name {
					if len(vals) == 0 {
						app.Spec.Components[i].Values = nil
					} else {
						app.Spec.Components[i].Values = vals
					}
				}
			}
		}
	}
	if req.EnvComponentValues != nil {
		// Per-(env, component) overlay overrides. Only the named pairs change; an
		// empty overlay clears that pair's override.
		ed := app.Spec.EnvironmentDefaults
		if ed == nil {
			ed = map[string]domain.EnvironmentOverride{}
		}
		for envName, byComp := range req.EnvComponentValues {
			ov := ed[envName]
			for name, vals := range byComp {
				if !compNames[name] {
					writeJSON(w, http.StatusBadRequest, errorResponse{Error: "unknown component: " + name})
					return
				}
				if ov.ComponentValues == nil {
					ov.ComponentValues = map[string]map[string]any{}
				}
				if len(vals) == 0 {
					delete(ov.ComponentValues, name)
				} else {
					ov.ComponentValues[name] = vals
				}
			}
			if len(ov.ComponentValues) == 0 {
				ov.ComponentValues = nil
			}
			ed[envName] = ov
		}
		app.Spec.EnvironmentDefaults = ed
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

	// The slow half — persist + full gitops publish — runs through dispatchOp so
	// a caller can opt into async (Prefer: respond-async / ?async=1) and poll
	// .../tasks/{id} for the result instead of holding the request open through
	// the git round-trips: a many-component manage save (chart syncs + composed
	// render per env + Kargo CRs) routinely outlives gateway timeouts. All
	// request validation already ran synchronously above, so bad input still
	// fails fast even in async mode.
	op := func(ctx context.Context) (int, any, error) {
		if err := ah.appStore.SaveApp(ctx, projectName, app); err != nil {
			return http.StatusInternalServerError, nil, errors.New("failed to save app")
		}

		// Re-publish so values.yaml reflects the new inputs (best-effort with
		// rollback, mirroring upgrade-template). Skip cleanly when no publisher.
		if ah.gitOpsPublisher != nil {
			allEnvs, err := ah.appStore.ListAppEnvironments(ctx, projectName, appName)
			if err != nil {
				return http.StatusInternalServerError, nil, errors.New("failed to list app environments")
			}
			var stableEnvs []*domain.AppEnvironment
			for _, env := range allEnvs {
				if env.EnvType != domain.AppEnvPreview {
					stableEnvs = append(stableEnvs, env)
				}
			}
			if len(stableEnvs) == 0 {
				stableEnvs = ah.stableEnvsFromOrg(ctx, app)
			}
			ah.ensureAppNamespaces(ctx, app, stableEnvs)
			if err := ah.gitOpsPublisher.PublishApp(ctx, app, stableEnvs); err != nil {
				app.Spec.Values, app.Spec.DisplayName, app.Spec.Description = prevValues, prevDisplay, prevDesc
				app.Spec.EnvironmentDefaults = prevEnvDefaults
				app.Spec.RawValues = prevRawValues
				app.Spec.Components = prevComponents
				app.Spec.CD = prevCD
				app.Spec.PreviewsEnabled = prevPreviewsEnabled
				_ = ah.appStore.SaveApp(ctx, projectName, app)
				slog.Error("update-app: publish failed; rolled back config change",
					"project", projectName, "app", appName, "err", err)
				return http.StatusInternalServerError, nil, fmt.Errorf("publish failed; config change rolled back: %w", err)
			}
			ah.ensureKargoProjectCreds(ctx, projectName)
		}

		saved, _ := ah.appStore.GetApp(ctx, projectName, appName)
		savedEnvs, _ := ah.appStore.ListAppEnvironments(ctx, projectName, appName)
		return http.StatusOK, updateAppResponse{App: appToDetailDTO(saved, savedEnvs)}, nil
	}
	dispatchOp(w, r, ah.async, "update-app", projectName, op)
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
	// Reject a rename that would collide with another app's folded ArgoCD name
	// (exclude the app being renamed).
	if other := ah.argoNameCollision(r.Context(), projectName, newName, oldName); other != "" {
		writeJSON(w, http.StatusConflict, errorResponse{
			Error: "app \"" + newName + "\" would produce the same ArgoCD Application name as \"" + other + "\" (both resolve to \"" + secrets.DedupProjectPrefix(projectName, newName) + "\"); pick another name",
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

	// Optional ?stack= scopes live enrichment to that stack's member apps. The
	// stack detail page needs live status only for members but still wants every
	// app's name (for the "add app" picker), so non-members are returned with
	// their stored status un-enriched rather than paying the per-env K8s/ArgoCD
	// cost. Empty (the default) enriches every app, as the dashboard/project
	// pages require.
	stackFilter := r.URL.Query().Get("stack")
	enrichApp := func(app *domain.App) bool {
		return stackFilter == "" || app.Spec.Stack == stackFilter
	}

	// Resolve org + apps once per request (shared across the concurrent env
	// fan-out below) instead of re-reading them ~2× per env.
	ctx := withEnrichMemos(r.Context())
	m := appMemoFrom(ctx)
	for _, app := range apps {
		m.seed(projectName, app)
	}

	// Gather each app's environments (cheap store reads), then enrich every
	// (app, env) pair with bounded concurrency. Each task owns its own distinct
	// *AppEnvironment, so no shared-state races. The list path reads through the
	// short-TTL status cache; DTOs are assembled in app order afterwards.
	envsByApp := make([][]*domain.AppEnvironment, len(apps))
	for i, app := range apps {
		envsByApp[i], _ = ah.appStore.ListAppEnvironments(ctx, projectName, app.Name)
	}

	type enrichTask struct {
		appName string
		env     *domain.AppEnvironment
	}
	var tasks []enrichTask
	for i, app := range apps {
		if !enrichApp(app) {
			continue
		}
		for _, env := range envsByApp[i] {
			tasks = append(tasks, enrichTask{appName: app.Name, env: env})
		}
	}
	runBounded(len(tasks), appEnrichConcurrency, func(i int) {
		t := tasks[i]
		ah.enrichCachedOrLive(ctx, projectName, t.appName, t.env)
	})

	dtos := make([]AppSummaryDTO, 0, len(apps))
	for i, app := range apps {
		dtos = append(dtos, appToSummaryDTO(app, envsByApp[i]))
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

	// Detail view: always recompute live (and refresh the cache) so an operator
	// on an app page never sees stale status. Memo the org + this app so the
	// per-env fan-out doesn't re-read them.
	ctx := withEnrichMemos(r.Context())
	appMemoFrom(ctx).seed(projectName, app)
	envs, _ := ah.appStore.ListAppEnvironments(ctx, projectName, appName)
	runBounded(len(envs), appEnrichConcurrency, func(i int) {
		ah.enrichAndCache(ctx, projectName, appName, envs[i])
	})

	detail := appToDetailDTO(app, envs)
	// Upgrade hints are detail-only: the picker needs each component's own
	// template's archived versions, which the list view can't afford to fan out.
	ah.decorateTemplateUpgrades(ctx, &detail)
	writeJSON(w, http.StatusOK, AppDetailResponse{App: detail})
}

// handleListAppEnvironments handles GET /api/v1/projects/{project}/apps/{app}/environments.
// Verifies the app exists before listing its environments; returns 404 otherwise.
func (ah *appHandler) handleListAppEnvironments(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	app, err := ah.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	ctx := withEnrichMemos(r.Context())
	appMemoFrom(ctx).seed(projectName, app)
	envs, err := ah.appStore.ListAppEnvironments(ctx, projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list environments"})
		return
	}

	runBounded(len(envs), appEnrichConcurrency, func(i int) {
		ah.enrichAndCache(ctx, projectName, appName, envs[i])
	})

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

	ah.enrichAndCache(withEnrichMemos(r.Context()), projectName, appName, env)

	dto := appEnvToDTO(env)
	// Surface pin state (lives in the app spec, not the env record) so the UI's
	// single-env view can show the pinned badge + Unpin.
	if app, err := ah.appStore.GetApp(r.Context(), projectName, appName); err == nil {
		if ov := app.Spec.EnvironmentDefaults[envName]; ov.PinnedFrom != "" {
			dto.PinnedTag = ov.PinnedImageTag
			dto.PinnedFrom = ov.PinnedFrom
		}
		if ov := app.Spec.EnvironmentDefaults[envName]; ov.Suspend != nil && *ov.Suspend {
			dto.Suspended = true
		}
	}

	writeJSON(w, http.StatusOK, AppEnvironmentResponse{
		Environment: dto,
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
// validation and storage. Previews are gated only by the app's PreviewsEnabled
// opt-in (an app-level concept); an app that has opted out returns 422.
//
// Internally, the handler delegates to domainapp.CreatePreview which builds
// the EnvironmentInstance, generates Helm values (the same mapping the base env
// uses), and generates the ArgoCD Application manifest as a pure function.
// Persistence to the AppStore is handled by projecting the EnvironmentInstance
// back onto an AppEnvironment (compat layer).

// errPreviewsDisabled is returned by upsertAppPreview when the app opts out of
// previews (AppSpec.PreviewsEnabled=false); the HTTP handler maps it to 422.
var errPreviewsDisabled = errors.New("previews disabled")

// upsertAppPreview creates or re-publishes (upsert) a preview environment for one
// app: it clones baseEnv via PublishAppPreview and applies imageTag. When
// namespaceOverride is non-empty it replaces the computed preview namespace (used
// by stack previews to co-locate every member in one namespace). Returns whether
// the preview already existed (200-vs-201 semantics) and the persisted env. On a
// publish failure it restores the prior env (or deletes the fresh record) so a
// retry is clean. Shared core of the per-app and stack preview paths.
// prepAppPreview does the git-free part of a preview upsert: build the preview
// EnvironmentInstance and persist its env record, returning the instance (to
// publish), the saved env, and the prior env + existed flag (for rollback). No
// git. Shared by the single-app upsert and the batched stack-preview path so the
// spec/store prep can run concurrently across members before one batch publish.
func (ah *appHandler) prepAppPreview(ctx context.Context, a *domain.App, previewName, baseEnv, imageTag, baseDomain, namespacePattern, namespaceOverride string) (inst *domain.EnvironmentInstance, env, prior *domain.AppEnvironment, existed bool, err error) {
	if !a.Spec.PreviewsEnabled {
		return nil, nil, nil, false, fmt.Errorf("app %q: %w", a.Name, errPreviewsDisabled)
	}
	previewResult, err := domainapp.CreatePreview(domainapp.PreviewRequest{
		App:              a,
		PreviewName:      previewName,
		BaseDomain:       baseDomain,
		NamespacePattern: namespacePattern,
	})
	if err != nil {
		return nil, nil, nil, false, err
	}
	inst = previewResult.Instance
	if namespaceOverride != "" {
		inst.Namespace = namespaceOverride // co-locate stack-preview members
	}
	imageTag = strings.TrimSpace(imageTag)

	// Upsert: capture the prior env so a failed publish can roll back cleanly.
	var getErr error
	prior, getErr = ah.appStore.GetAppEnvironment(ctx, a.ProjectName, a.Name, previewName)
	existed = getErr == nil

	urls := []string{}
	if inst.URL != "" {
		urls = []string{inst.URL}
	}
	env = &domain.AppEnvironment{
		AppName:     inst.AppName,
		ProjectName: inst.ProjectName,
		EnvName:     inst.EnvName,
		EnvType:     inst.EnvType,
		BaseEnv:     baseEnv, // the stable env this preview clones (grouping + delete)
		Namespace:   inst.Namespace,
		URLs:        urls,
		Status:      inst.Status,
	}
	if imageTag != "" {
		env.Release = &domain.AppReleaseRef{Tag: imageTag}
	} else if existed {
		env.Release = prior.Release // preserve a previously-set tag on re-publish
	}
	if err := ah.appStore.SaveAppEnvironment(ctx, a.ProjectName, env); err != nil {
		return nil, nil, prior, existed, fmt.Errorf("failed to save preview: %w", err)
	}
	return inst, env, prior, existed, nil
}

// rollbackPreviewPrep undoes prepAppPreview's store write after a failed publish:
// restore the prior env on an update, or delete the freshly-created record.
func (ah *appHandler) rollbackPreviewPrep(ctx context.Context, a *domain.App, previewName string, prior *domain.AppEnvironment, existed bool) {
	if existed && prior != nil {
		_ = ah.appStore.SaveAppEnvironment(ctx, a.ProjectName, prior)
	} else {
		_ = ah.appStore.DeleteAppEnvironment(ctx, a.ProjectName, a.Name, previewName)
	}
}

// publishPreviewsBatch publishes many previews in ONE git commit when the
// publisher supports batching (BatchPreviewPublisher), else falls back to
// per-member PublishAppPreview. Batched form of PublishAppPreview — the stack
// preview 504 fix, mirroring republishAppsFocus.
func (ah *appHandler) publishPreviewsBatch(ctx context.Context, targets []PreviewPublishTarget) error {
	if ah.gitOpsPublisher == nil || len(targets) == 0 {
		return nil
	}
	if b, ok := ah.gitOpsPublisher.(BatchPreviewPublisher); ok {
		if err := b.PublishPreviews(ctx, targets); !errors.Is(err, errUnbatched) {
			return err // batched (success or a real publish error)
		}
	}
	// Fallback: publisher without the batch capability (e.g. test stubs).
	for _, t := range targets {
		if err := ah.gitOpsPublisher.PublishAppPreview(ctx, t.App, t.Preview, t.BaseEnv, t.ImageTag); err != nil {
			return err
		}
	}
	return nil
}

func (ah *appHandler) upsertAppPreview(ctx context.Context, a *domain.App, previewName, baseEnv, imageTag, baseDomain, namespacePattern, namespaceOverride string) (bool, *domain.AppEnvironment, error) {
	inst, env, prior, existed, err := ah.prepAppPreview(ctx, a, previewName, baseEnv, imageTag, baseDomain, namespacePattern, namespaceOverride)
	if err != nil {
		return existed, nil, err
	}
	if ah.gitOpsPublisher != nil {
		if err := ah.gitOpsPublisher.PublishAppPreview(ctx, a, inst, baseEnv, imageTag); err != nil {
			ah.rollbackPreviewPrep(ctx, a, previewName, prior, existed)
			return existed, nil, fmt.Errorf("failed to publish preview: %w", err)
		}
	}
	return existed, env, nil
}

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

	// Run the full preview upsert (EnvironmentInstance + Helm values + ArgoCD
	// Application + publish). The base env's domain drives the preview routing
	// host so the stored URL matches what the chart renders. 201 on first create,
	// 200 on update (CI re-POSTing a new image tag on each PR push). The upsert
	// clones/commits/pushes the gitops repo, so it's deferred when the caller opts
	// into async (Prefer: respond-async / ?async=1) to avoid a gateway 504.
	op := func(ctx context.Context) (int, any, error) {
		existed, env, err := ah.upsertAppPreview(ctx, a, sanitized, baseEnv, req.ImageTag,
			ah.baseDomainForEnv(ctx, baseEnv), ah.previewNamespacePattern(ctx, projectName), "")
		if err != nil {
			code := http.StatusInternalServerError
			if errors.Is(err, errPreviewsDisabled) {
				code = http.StatusUnprocessableEntity
			}
			return code, nil, err
		}
		status := http.StatusCreated
		if existed {
			status = http.StatusOK
		}
		return status, appPreviewToDTO(env), nil
	}
	dispatchOp(w, r, ah.async, "preview-app", projectName, op)
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

	// Prune the preview's GitOps files first so ArgoCD removes the generated
	// Application + namespace. Done before the store delete so a gitops failure
	// keeps the record (the preview stays listed and retryable) rather than
	// orphaning a running Application with no store entry. The gitops prune
	// clones/commits/pushes, so it's deferred when the caller opts into async.
	op := func(ctx context.Context) (int, any, error) {
		if d, ok := ah.gitOpsPublisher.(AppPreviewDeleter); ok {
			if err := d.DeleteAppPreview(ctx, projectName, previewName, appName, env.BaseEnv); err != nil {
				return http.StatusInternalServerError, nil, fmt.Errorf("failed to remove preview from gitops")
			}
		}
		if err := ah.appStore.DeleteAppEnvironment(ctx, projectName, appName, previewName); err != nil {
			return http.StatusInternalServerError, nil, fmt.Errorf("failed to delete preview")
		}
		// 204 No Content: nil result → dispatchOp writes just the status, no body.
		return http.StatusNoContent, nil, nil
	}
	dispatchOp(w, r, ah.async, "preview-app-delete", projectName, op)
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

// upgradeAppTemplateRequest is the body for POST .../upgrade-template. Exactly
// one of Version / Components must be set.
type upgradeAppTemplateRequest struct {
	// Version upgrades the app's PRIMARY template: every component rendered by
	// AppSpec.Template.Name moves to this version, and so does the app-level
	// mirror. Components on a different template are left alone and reported
	// back in the response's "skipped". Must be one of the versions returned by
	// GET /api/v1/templates/{name}/versions.
	Version string `json:"version,omitempty"`
	// Components upgrades named components individually, keyed component name →
	// target version. This is the general form: a composed app mixes templates
	// (api→web-service, worker→worker, migrate→job), so there is no single
	// app-level version that means anything for it. Each version is validated
	// against ITS OWN component's template.
	Components map[string]string `json:"components,omitempty"`
	// Environment scopes the upgrade to ONE stable environment: the version
	// pins are written as that env's overrides (EnvironmentOverride.
	// TemplateVersions) instead of the app-wide pins, so e.g. staging runs the
	// new chart while production stays put — upgrade production the same way
	// when it's ready. Once every stable env converges on one version the
	// overrides fold into the app-wide pin. Omitted = upgrade every environment
	// at once (the pre-env-scoping behavior; any env-scoped pins for the moved
	// components are cleared).
	Environment string `json:"environment,omitempty"`
}

// upgradedComponentDTO reports one component's version move in the response.
type upgradedComponentDTO struct {
	Name        string `json:"name"`
	Template    string `json:"template"`
	FromVersion string `json:"fromVersion,omitempty"`
	ToVersion   string `json:"toVersion"`
}

// handleUpgradeAppTemplate moves an app's template version pin(s) and
// re-publishes via the existing gitops flow. The publisher's version-aware
// ChartFetcher then resolves to the per-version archive ConfigMap, so the chart
// bytes Argo deploys actually change.
//
// The pin lives per component (ComponentSpec.Template), because that is what the
// composed render path reads — a ≥2-component app renders one chart source per
// component and never looks at AppSpec.Template. But AppSpec.Template is not
// decoration either: it is the pin the SINGLE-source path reads. So this writes
// both, keeping the mirror in step via AppSpec.SyncPrimaryTemplate.
//
// Every version is validated before anything is mutated, then a single SaveApp +
// PublishApp makes the whole upgrade atomic; a publish failure restores the
// entire component list and the mirror.
//
// This does NOT migrate values when the new version's chart differs from the old
// one. Overlays (ComponentSpec.Values / AppSpec.RawValues) are an additive layer
// and are never rewritten — a renamed or removed chart key leaves the override
// silently inert. A preflight diff is a follow-up (docs/templates.md).
func (ah *appHandler) handleUpgradeAppTemplate(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")

	var req upgradeAppTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	wantVersion := strings.TrimSpace(req.Version)
	switch {
	case wantVersion == "" && len(req.Components) == 0:
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "version or components is required"})
		return
	case wantVersion != "" && len(req.Components) > 0:
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "set either version (upgrade the app's primary template) or components (per-component versions), not both",
		})
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

	// Env-scoped upgrade: the target must be an existing stable environment.
	envScope := strings.TrimSpace(req.Environment)
	if envScope != "" {
		envRec, envErr := ah.appStore.GetAppEnvironment(r.Context(), projectName, appName, envScope)
		if envErr != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: fmt.Sprintf("environment %q not found for app %q", envScope, appName),
			})
			return
		}
		if envRec.EnvType == domain.AppEnvPreview {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: "cannot upgrade a preview environment — previews follow their base env",
			})
			return
		}
	}

	// A BYO/passthrough app stores no components at all (creator.go returns nil
	// for a template with no declared components that opts out of the canonical
	// schema). There AppSpec.Template is not a mirror — it is the only pin there
	// is, and the single-source path renders straight from it. Do NOT synthesize a
	// component to carry it: persisting one would trip the publisher's
	// len(Components)==1 canonical-key remap, changing how the app renders as a
	// side effect of an upgrade.
	if len(app.Spec.Components) == 0 {
		ah.upgradeTemplatelessApp(w, r, app, wantVersion, req.Components, envScope)
		return
	}
	app.Spec.BackfillComponentTemplates()

	// Resolve the target version per component BEFORE mutating anything.
	targets := map[string]string{} // component name → target version
	var skipped []string
	if wantVersion != "" {
		// A bare version means "upgrade the app's primary template". Components
		// rendered by a different chart are untouched and reported back, so a
		// composed app's other templates can't be moved by accident.
		for _, c := range app.Spec.Components {
			if c.Template == nil {
				continue
			}
			if c.Template.Name == app.Spec.Template.Name {
				targets[c.Name] = wantVersion
			} else {
				skipped = append(skipped, c.Name)
			}
		}
		if len(targets) == 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: fmt.Sprintf("no component of %q uses template %q — name the components explicitly instead",
					appName, app.Spec.Template.Name),
			})
			return
		}
	} else {
		byName := map[string]domain.ComponentSpec{}
		for _, c := range app.Spec.Components {
			byName[c.Name] = c
		}
		for name, v := range req.Components {
			c, ok := byName[name]
			if !ok {
				writeJSON(w, http.StatusBadRequest, errorResponse{
					Error: fmt.Sprintf("app %q has no component %q", appName, name),
				})
				return
			}
			if c.Template == nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{
					Error: fmt.Sprintf("component %q carries no template to upgrade", name),
				})
				return
			}
			if strings.TrimSpace(v) == "" {
				writeJSON(w, http.StatusBadRequest, errorResponse{
					Error: fmt.Sprintf("component %q: version is required", name),
				})
				return
			}
			targets[name] = strings.TrimSpace(v)
		}
	}

	// Validate each target against ITS OWN component's template — a composed app
	// mixes charts, so one app-level version list would check the wrong archives.
	// Skipped when no kubeClient is wired (test harnesses, fake mode).
	for _, c := range app.Spec.Components {
		target, ok := targets[c.Name]
		if !ok || c.Template == nil {
			continue
		}
		if !ah.templateHasVersion(w, r, c.Template.Name, target, c.Name) {
			return
		}
	}

	// Env-scoped: write the pins as the chosen env's overrides and stop —
	// the app-wide pins (and every other env) stay untouched.
	if envScope != "" {
		ah.upgradeAppTemplateForEnv(w, r, app, envScope, targets, skipped)
		return
	}

	// Snapshot for rollback, then apply. Components are value types holding a
	// Template POINTER, so a slice copy would share those pointers with the
	// mutated spec — deep-copy each ref.
	prevComponents := make([]domain.ComponentSpec, len(app.Spec.Components))
	copy(prevComponents, app.Spec.Components)
	for i := range prevComponents {
		if prevComponents[i].Template != nil {
			t := *prevComponents[i].Template
			prevComponents[i].Template = &t
		}
	}
	prevTemplate := app.Spec.Template
	prevEnvDefaults := snapshotEnvTemplateVersions(app)

	var moved []upgradedComponentDTO
	for i := range app.Spec.Components {
		c := &app.Spec.Components[i]
		target, ok := targets[c.Name]
		if !ok || c.Template == nil || c.Template.Version == target {
			continue
		}
		moved = append(moved, upgradedComponentDTO{
			Name:        c.Name,
			Template:    c.Template.Name,
			FromVersion: c.Template.Version,
			ToVersion:   target,
		})
		t := *c.Template
		t.Version = target
		c.Template = &t
	}
	app.Spec.SyncPrimaryTemplate()
	// An app-wide upgrade supersedes any env-scoped pins for the moved
	// components — leaving them would silently hold those envs on old versions.
	for envName, ov := range app.Spec.EnvironmentDefaults {
		for name := range targets {
			delete(ov.TemplateVersions, name)
		}
		if len(ov.TemplateVersions) == 0 {
			ov.TemplateVersions = nil
		}
		app.Spec.EnvironmentDefaults[envName] = ov
	}

	// Nothing actually moved — re-pinning to the current version is fine, but
	// don't pretend we did work (or churn a gitops commit for it).
	if len(moved) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"message": "app already pinned to the requested version(s)",
			"project": projectName,
			"app":     appName,
			"skipped": skipped,
		})
		return
	}

	if !ah.saveAndRepublishUpgrade(w, r, app, func() {
		app.Spec.Components = prevComponents
		app.Spec.Template = prevTemplate
		restoreEnvTemplateVersions(app, prevEnvDefaults)
	}) {
		return
	}

	slog.Info("app upgraded to template version",
		"project", projectName, "app", appName,
		"components", len(moved), "skipped", len(skipped),
		"from", prevTemplate.Version, "to", app.Spec.Template.Version,
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "app upgraded — ArgoCD will sync the new chart bytes shortly",
		"project": projectName,
		"app":     appName,
		// fromVersion/toVersion describe the PRIMARY template, unchanged from
		// before per-component upgrades existed, so older clients keep working.
		"fromVersion": prevTemplate.Version,
		"toVersion":   app.Spec.Template.Version,
		"components":  moved,
		"skipped":     skipped,
	})
}

// snapshotEnvTemplateVersions / restoreEnvTemplateVersions snapshot ONLY the
// per-env TemplateVersions maps (the piece upgrade paths mutate) for rollback.
func snapshotEnvTemplateVersions(app *domain.App) map[string]map[string]string {
	out := make(map[string]map[string]string, len(app.Spec.EnvironmentDefaults))
	for envName, ov := range app.Spec.EnvironmentDefaults {
		if ov.TemplateVersions == nil {
			out[envName] = nil
			continue
		}
		tv := make(map[string]string, len(ov.TemplateVersions))
		for k, v := range ov.TemplateVersions {
			tv[k] = v
		}
		out[envName] = tv
	}
	return out
}

func restoreEnvTemplateVersions(app *domain.App, snap map[string]map[string]string) {
	for envName, tv := range snap {
		ov := app.Spec.EnvironmentDefaults[envName]
		ov.TemplateVersions = tv
		app.Spec.EnvironmentDefaults[envName] = ov
	}
}

// stableEnvNamesForApp lists the app's stable (non-preview) environment names,
// falling back to the org's environments when none are recorded yet — the same
// universe saveAndRepublishUpgrade publishes to.
func (ah *appHandler) stableEnvNamesForApp(ctx context.Context, app *domain.App) []string {
	var out []string
	if envs, err := ah.appStore.ListAppEnvironments(ctx, app.ProjectName, app.Name); err == nil {
		for _, e := range envs {
			if e.EnvType != domain.AppEnvPreview {
				out = append(out, e.EnvName)
			}
		}
	}
	if len(out) == 0 {
		for _, e := range ah.stableEnvsFromOrg(ctx, app) {
			out = append(out, e.EnvName)
		}
	}
	return out
}

// collapseConvergedTemplateVersions folds env-scoped version pins back into the
// app-wide pin once EVERY stable env explicitly pins the same version for a
// component (or, via the reserved "" key, the templateless app-level pin). The
// spec then reads as if the upgrade had been app-wide all along, and previews /
// newly-materialized envs inherit the converged version naturally.
func (ah *appHandler) collapseConvergedTemplateVersions(ctx context.Context, app *domain.App) {
	ed := app.Spec.EnvironmentDefaults
	if len(ed) == 0 {
		return
	}
	envNames := ah.stableEnvNamesForApp(ctx, app)
	if len(envNames) == 0 {
		return
	}
	keys := map[string]bool{}
	for _, name := range envNames {
		for k := range ed[name].TemplateVersions {
			keys[k] = true
		}
	}
	for key := range keys {
		ver := ""
		converged := true
		for _, name := range envNames {
			v := ed[name].TemplateVersions[key]
			if v == "" || (ver != "" && v != ver) {
				converged = false
				break
			}
			ver = v
		}
		if !converged || ver == "" {
			continue
		}
		if key == "" {
			app.Spec.Template.Version = ver
		} else {
			for i := range app.Spec.Components {
				c := &app.Spec.Components[i]
				if c.Name == key && c.Template != nil {
					t := *c.Template
					t.Version = ver
					c.Template = &t
				}
			}
		}
		for _, name := range envNames {
			ov := ed[name]
			delete(ov.TemplateVersions, key)
			if len(ov.TemplateVersions) == 0 {
				ov.TemplateVersions = nil
			}
			ed[name] = ov
		}
	}
	app.Spec.SyncPrimaryTemplate()
}

// upgradeAppTemplateForEnv writes validated version targets as ONE env's
// overrides (EnvironmentOverride.TemplateVersions) — the env-scoped upgrade. A
// target equal to the app-wide pin clears that env's override instead (the env
// simply follows the pin again), and full convergence across stable envs folds
// into the app-wide pin via collapseConvergedTemplateVersions.
func (ah *appHandler) upgradeAppTemplateForEnv(w http.ResponseWriter, r *http.Request, app *domain.App, envName string, targets map[string]string, skipped []string) {
	projectName, appName := app.ProjectName, app.Name

	appPin := map[string]string{}
	tmplName := map[string]string{}
	for _, c := range app.Spec.Components {
		if c.Template != nil {
			appPin[c.Name] = c.Template.Version
			tmplName[c.Name] = c.Template.Name
		}
	}
	// Reserved "" key = the app-level pin of a component-less (BYO) app.
	appPin[""] = app.Spec.Template.Version
	tmplName[""] = app.Spec.Template.Name

	prevComponents := make([]domain.ComponentSpec, len(app.Spec.Components))
	copy(prevComponents, app.Spec.Components)
	for i := range prevComponents {
		if prevComponents[i].Template != nil {
			t := *prevComponents[i].Template
			prevComponents[i].Template = &t
		}
	}
	prevTemplate := app.Spec.Template
	prevEnvDefaults := snapshotEnvTemplateVersions(app)

	ov := app.Spec.EnvironmentDefaults[envName]
	next := map[string]string{}
	for k, v := range ov.TemplateVersions {
		next[k] = v
	}
	var moved []upgradedComponentDTO
	for name, target := range targets {
		cur := next[name]
		if cur == "" {
			cur = appPin[name]
		}
		if cur == target {
			continue
		}
		moved = append(moved, upgradedComponentDTO{
			Name:        name,
			Template:    tmplName[name],
			FromVersion: cur,
			ToVersion:   target,
		})
		if target == appPin[name] {
			delete(next, name)
		} else {
			next[name] = target
		}
	}
	if len(moved) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"message":     fmt.Sprintf("%s is already on the requested version(s)", envName),
			"project":     projectName,
			"app":         appName,
			"environment": envName,
			"skipped":     skipped,
		})
		return
	}
	if len(next) == 0 {
		next = nil
	}
	ov.TemplateVersions = next
	if app.Spec.EnvironmentDefaults == nil {
		app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
	}
	app.Spec.EnvironmentDefaults[envName] = ov

	ah.collapseConvergedTemplateVersions(r.Context(), app)

	if !ah.saveAndRepublishUpgrade(w, r, app, func() {
		app.Spec.Components = prevComponents
		app.Spec.Template = prevTemplate
		restoreEnvTemplateVersions(app, prevEnvDefaults)
	}) {
		return
	}

	slog.Info("app env upgraded to template version",
		"project", projectName, "app", appName, "env", envName, "components", len(moved))
	writeJSON(w, http.StatusOK, map[string]any{
		"message":     fmt.Sprintf("%s upgraded — ArgoCD will sync its new chart bytes shortly; other environments are unchanged", envName),
		"project":     projectName,
		"app":         appName,
		"environment": envName,
		"components":  moved,
		"skipped":     skipped,
	})
}

// saveAndRepublishUpgrade persists a mutated app and re-publishes it via the same
// path as /sync, restoring the pre-upgrade state through restore() if the publish
// fails so the store never drifts from what's actually in the gitops repo. It
// writes the error response itself; reports whether the caller may continue.
func (ah *appHandler) saveAndRepublishUpgrade(w http.ResponseWriter, r *http.Request, app *domain.App, restore func()) bool {
	projectName, appName := app.ProjectName, app.Name

	if err := ah.appStore.SaveApp(r.Context(), projectName, app); err != nil {
		slog.Error("upgrade-template: save app failed", "project", projectName, "app", appName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist version pin"})
		return false
	}

	// PublishApp's syncChart honours each component's Template.Version (and
	// AppSpec.Template.Version on the single-source path), so the chart bytes in
	// the gitops repo actually change.
	allEnvs, err := ah.appStore.ListAppEnvironments(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list app environments"})
		return false
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
		restore()
		_ = ah.appStore.SaveApp(r.Context(), projectName, app)
		slog.Error("upgrade-template: publish failed; rolled back version pins",
			"project", projectName, "app", appName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{
			Error: "publish failed; version pins rolled back: " + err.Error(),
		})
		return false
	}
	return true
}

// upgradeTemplatelessApp handles the app shape that stores no components at all
// (BYO/passthrough). There is no component level to write: AppSpec.Template is
// the pin the single-source render path reads, so it is upgraded directly (or,
// env-scoped, via the env's reserved-"" TemplateVersions override). The
// per-component request form has nothing to address here and is rejected.
func (ah *appHandler) upgradeTemplatelessApp(w http.ResponseWriter, r *http.Request, app *domain.App, wantVersion string, components map[string]string, envScope string) {
	if len(components) > 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: fmt.Sprintf("app %q has no components; upgrade it with {\"version\": ...}", app.Name),
		})
		return
	}
	if app.Spec.Template.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: fmt.Sprintf("app %q has no template to upgrade", app.Name),
		})
		return
	}
	if !ah.templateHasVersion(w, r, app.Spec.Template.Name, wantVersion, "") {
		return
	}

	if envScope != "" {
		// The reserved "" key pins the app-level template for this env only.
		ah.upgradeAppTemplateForEnv(w, r, app, envScope, map[string]string{"": wantVersion}, nil)
		return
	}

	prevVersion := app.Spec.Template.Version
	prevEnvDefaults := snapshotEnvTemplateVersions(app)
	if prevVersion == wantVersion {
		writeJSON(w, http.StatusOK, map[string]any{
			"message": "app already pinned to the requested version(s)",
			"project": app.ProjectName,
			"app":     app.Name,
		})
		return
	}
	app.Spec.Template.Version = wantVersion
	// An app-wide upgrade supersedes any env-scoped pin of the app-level template.
	for envName, ov := range app.Spec.EnvironmentDefaults {
		delete(ov.TemplateVersions, "")
		if len(ov.TemplateVersions) == 0 {
			ov.TemplateVersions = nil
		}
		app.Spec.EnvironmentDefaults[envName] = ov
	}

	if !ah.saveAndRepublishUpgrade(w, r, app, func() {
		app.Spec.Template.Version = prevVersion
		restoreEnvTemplateVersions(app, prevEnvDefaults)
	}) {
		return
	}

	slog.Info("app upgraded to template version",
		"project", app.ProjectName, "app", app.Name,
		"from", prevVersion, "to", wantVersion,
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"message":     "app upgraded — ArgoCD will sync the new chart bytes shortly",
		"project":     app.ProjectName,
		"app":         app.Name,
		"fromVersion": prevVersion,
		"toVersion":   wantVersion,
	})
}

// templateHasVersion checks that a version exists as an archive ConfigMap for the
// named template, writing the error response and reporting false when it doesn't.
// component, when non-empty, prefixes the message so a composed app's failure
// names the offending component. Skipped (returns true) when no kubeClient is
// wired — test harnesses and fake mode leave version validity to the caller.
func (ah *appHandler) templateHasVersion(w http.ResponseWriter, r *http.Request, templateName, version, component string) bool {
	if ah.kubeClient == nil {
		return true
	}
	versions, err := kube.ListTemplateVersions(r.Context(), ah.kubeClient, templateName)
	if err != nil {
		slog.Error("upgrade-template: list versions failed", "template", templateName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list template versions"})
		return false
	}
	for _, v := range versions {
		if v.Version == version {
			return true
		}
	}
	prefix := ""
	if component != "" {
		prefix = fmt.Sprintf("component %q: ", component)
	}
	writeJSON(w, http.StatusBadRequest, errorResponse{
		Error: fmt.Sprintf("%stemplate %q has no archived version %q — call GET /api/v1/templates/%s/versions to see what's available",
			prefix, templateName, version, templateName),
	})
	return false
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

// appStableEnvs returns the app's non-preview environments — the persisted
// records when present, otherwise the org-derived fallback (same source
// republishAllApps uses). Order is carried on each record.
func (ah *appHandler) appStableEnvs(ctx context.Context, app *domain.App) []*domain.AppEnvironment {
	envs, _ := ah.appStore.ListAppEnvironments(ctx, app.ProjectName, app.Name)
	stable := make([]*domain.AppEnvironment, 0, len(envs))
	for _, e := range envs {
		if e.EnvType != domain.AppEnvPreview {
			stable = append(stable, e)
		}
	}
	if len(stable) == 0 {
		for _, e := range ah.stableEnvsFromOrg(ctx, app) {
			if e.EnvType != domain.AppEnvPreview {
				stable = append(stable, e)
			}
		}
	}
	return stable
}

// baseStableEnvName returns the app's base environment name — the lowest-Order
// non-preview env (Name tiebreak), matching the base the env-summary DTOs mark.
// Returns "" when the app has no stable env.
func (ah *appHandler) baseStableEnvName(ctx context.Context, app *domain.App) string {
	stable := ah.appStableEnvs(ctx, app)
	if len(stable) == 0 {
		return ""
	}
	sort.Slice(stable, func(i, j int) bool {
		if stable[i].Order != stable[j].Order {
			return stable[i].Order < stable[j].Order
		}
		return stable[i].EnvName < stable[j].EnvName
	})
	return stable[0].EnvName
}

// baseDomainForEnv returns the ingress DNS zone configured for the named org
// environment, or "localhost" when none is set (or no org provider). This
// mirrors the publisher's preview base-domain resolution (orgEnv.BaseDomain →
// localhost), so a preview's stored routing host matches what the chart renders.
func (ah *appHandler) baseDomainForEnv(ctx context.Context, envName string) string {
	if ah.orgProvider == nil {
		return "localhost"
	}
	org, err := ah.orgProvider.GetOrg(ctx)
	if err != nil || org == nil {
		return "localhost"
	}
	for _, e := range org.Environments {
		if e.Name == envName && e.BaseDomain != "" {
			return e.BaseDomain
		}
	}
	return "localhost"
}

// previewNamespacePattern returns the project's configured preview namespace
// pattern, or "" (→ domain.DefaultPreviewNamespacePattern) when the project has
// none or the store is unavailable.
func (ah *appHandler) previewNamespacePattern(ctx context.Context, projectName string) string {
	if ah.projectStore == nil {
		return ""
	}
	proj, err := ah.projectStore.Get(ctx, projectName)
	if err != nil || proj == nil {
		return ""
	}
	return proj.Spec.PreviewNamespacePattern
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
// handleUndeployAppEnv serves POST /api/v1/projects/{project}/apps/{app}/environments/{env}/undeploy.
// It is the explicit "remove from cluster" action: it sets the env's Deploy flag
// to false (so it won't be re-published) and removes the env's GitOps files so
// ArgoCD prunes its workload. Deliberate and destructive — for stateful apps this
// deletes the env's data. Other envs and apps are untouched.
func (ah *appHandler) handleUndeployAppEnv(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	app, err := ah.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "app \"" + appName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	// The base env (lowest Order) is the pipeline's warehouse source and the
	// previews' clone target — removing it would break both. Only envs above the
	// base can be decommissioned.
	if base := ah.baseStableEnvName(r.Context(), app); base != "" && envName == base {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{
			Error: "cannot remove the base environment \"" + envName + "\"; it is the pipeline source",
		})
		return
	}

	// Mark the env opted-out so a later re-publish doesn't recreate it. For a
	// pipeline app this also drops it from the Kargo chain (publishKargoCRs skips
	// Deploy=false envs), re-linking the neighbours below.
	if app.Spec.EnvironmentDefaults == nil {
		app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
	}
	ov := app.Spec.EnvironmentDefaults[envName]
	no := false
	ov.Deploy = &no
	app.Spec.EnvironmentDefaults[envName] = ov
	if err := ah.appStore.SaveApp(r.Context(), projectName, app); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save app"})
		return
	}

	// Remove the env's GitOps files (app + platform resources + Kargo stage) so
	// ArgoCD prunes the workload and the stage leaves the pipeline.
	if ah.gitOpsPublisher != nil {
		if err := ah.gitOpsPublisher.RemoveAppEnv(r.Context(), projectName, appName, envName); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to remove environment: " + err.Error()})
			return
		}
		// Pipeline apps: republish so the Kargo chain + promotion policies rebuild
		// without the decommissioned env (the previous env becomes terminal).
		// Direct apps have no pipeline — skip. Best-effort: the removal already
		// pruned the workload; a chain-rebuild failure self-heals on next publish.
		if !app.Spec.IsDirect() {
			if err := ah.gitOpsPublisher.PublishApp(r.Context(), app, ah.appStableEnvs(r.Context(), app)); err != nil {
				slog.Warn("undeploy: kargo chain rebuild failed — will self-heal on next publish",
					"project", projectName, "app", appName, "env", envName, "err", err)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "environment " + envName + " removed from cluster",
		"project": projectName,
		"app":     appName,
		"env":     envName,
	})
}

// pinAppEnvRequest is the body for pinning a stable env to a preview's image.
type pinAppEnvRequest struct {
	// FromPreview is the preview env name (e.g. "pr-712") whose built image is
	// pinned onto the target env. Its current image tag is resolved server-side.
	FromPreview string `json:"fromPreview"`
}

// pinLabel renders a human label for a pin, preferring the source preview name.
func pinLabel(from, tag string) string {
	if from != "" {
		return from + " (" + tag + ")"
	}
	return tag
}

// liveReadTimeout bounds best-effort live cluster reads (pre-pin status capture,
// Kargo freight lookup) so one slow/unreachable cluster can't hang a request —
// especially a stack fan-out that repeats the read per member.
const liveReadTimeout = 5 * time.Second

// kargoFreightReader is the optional capability (implemented by the Kargo
// promoter) to resolve an env's real image tag from Kargo, used to restore a
// stable env when unpinning a preview.
type kargoFreightReader interface {
	CurrentFreightImageTag(ctx context.Context, projectName, appName, env, repoSubstr string) (string, error)
	LatestFreightImageTag(ctx context.Context, projectName, appName, repoSubstr string) (string, error)
}

// KargoFreightImage / KargoFreightRecord mirror the kube-layer freight types at
// the server boundary (server must not import kube).
type KargoFreightImage struct {
	RepoURL string
	Tag     string
}

type KargoFreightRecord struct {
	Name         string
	Images       []KargoFreightImage
	DiscoveredAt string
	Current      bool
}

// kargoFreightHistorian is the optional capability (implemented by the Kargo
// promoter) to list a stage's previously-deployed freight and re-promote one of
// them — the rollback primitives.
type kargoFreightHistorian interface {
	StageFreightHistory(ctx context.Context, projectName, appName, envName string, limit int) ([]KargoFreightRecord, error)
	PromoteFreight(ctx context.Context, projectName, appName, envName, freightName string) (KargoPromotionResult, error)
}

// rollbackPinnedFrom is the EnvironmentOverride.PinnedFrom marker for a rollback
// hold: it pauses Kargo auto-promotion for the stage (any non-empty PinnedFrom
// does) without naming a source preview. "Resume CD" is the existing unpin.
const rollbackPinnedFrom = "rollback"

// appImageRepoSubstr returns the app's image repository (when set on the app),
// used to pick the right image within a multi-image freight. Empty for
// template-image apps, where the per-app freight is matched by warehouse origin.
func appImageRepoSubstr(app *domain.App) string {
	if repo, ok := app.Spec.Values["image_repository"].(string); ok {
		return strings.TrimSpace(repo)
	}
	return ""
}

// Sentinels let the per-app HTTP handler map pinAppEnv/unpinAppEnv failures back
// to status codes, and let the stack batch path classify a failure as
// "skip this member" (not applicable) vs a real error.
var (
	errPinAppNotFound     = errors.New("app not found")
	errPinNotPipeline     = errors.New("pinning applies to pipeline-delivery apps only")
	errPinTargetNotFound  = errors.New("target environment not found")
	errPinTargetIsPreview = errors.New("cannot pin a preview environment")
	errPinPreviewNotFound = errors.New("source preview not found")
	errPinNoTag           = errors.New("source preview has no image tag")
)

// statusForPinErr maps a pinAppEnv/unpinAppEnv error to an HTTP status.
func statusForPinErr(err error) int {
	switch {
	case errors.Is(err, errPinAppNotFound), errors.Is(err, errPinTargetNotFound), errors.Is(err, errPinPreviewNotFound):
		return http.StatusNotFound
	case errors.Is(err, errPinNotPipeline), errors.Is(err, errPinTargetIsPreview), errors.Is(err, errPinNoTag):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// pinIsSkippable reports whether a pin failure means the op does not apply to
// this member (a skip row in a stack fan-out) rather than a real error: a
// direct-delivery member, a member not deployed to the target env, or a member
// without the named preview / with no built image.
func pinIsSkippable(err error) bool {
	return errors.Is(err, errPinNotPipeline) ||
		errors.Is(err, errPinTargetNotFound) ||
		errors.Is(err, errPinPreviewNotFound) ||
		errors.Is(err, errPinNoTag)
}

// appFocusPublish is one member of a batched full-app publish: the app plus an
// optional focus env whose values must be force-written (the pinned/unpinned
// stable env, which pipeline apps don't rewrite via the first-deploy set).
type appFocusPublish struct {
	app      *domain.App
	focusEnv *domain.AppEnvironment
}

// stableEnvsForPublish returns an app's stable (non-preview) env records, or the
// org-derived defaults when none are persisted — mirroring republishApp.
func (ah *appHandler) stableEnvsForPublish(ctx context.Context, app *domain.App) []*domain.AppEnvironment {
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
	return stable
}

// republishAppsFocus publishes many apps' full trees (infra + values + Kargo
// CRs) plus each app's focus env in ONE git commit when the publisher supports
// batching (BatchAppPublisher), else falls back to per-app republishApp +
// PublishAppEnv. This is the batched equivalent of republishApp +
// PublishAppEnv(focus) — the 504 fix for stack pin/unpin.
func (ah *appHandler) republishAppsFocus(ctx context.Context, items []appFocusPublish) error {
	if ah.gitOpsPublisher == nil || len(items) == 0 {
		return nil
	}
	targets := make([]AppPublishTarget, 0, len(items))
	for _, it := range items {
		stable := ah.stableEnvsForPublish(ctx, it.app)
		ah.resolveEnvNamespaces(ctx, it.app, stable)
		ah.ensureAppNamespaces(ctx, it.app, stable)
		var focus []*domain.AppEnvironment
		if it.focusEnv != nil {
			ah.ensureAppNamespace(ctx, it.app, it.focusEnv)
			focus = []*domain.AppEnvironment{it.focusEnv}
		}
		targets = append(targets, AppPublishTarget{App: it.app, Envs: stable, FocusEnvs: focus})
	}
	if b, ok := ah.gitOpsPublisher.(BatchAppPublisher); ok {
		if err := b.PublishApps(ctx, targets); !errors.Is(err, errUnbatched) {
			return err // batched (success or a real publish error)
		}
	}
	// Fallback: publisher without the batch capability (e.g. test stubs).
	for _, t := range targets {
		if err := ah.gitOpsPublisher.PublishApp(ctx, t.App, t.Envs); err != nil {
			return err
		}
		for _, fe := range t.FocusEnvs {
			if err := ah.gitOpsPublisher.PublishAppEnv(ctx, t.App, fe); err != nil {
				return err
			}
		}
	}
	return nil
}

// pinAppEnvSpec validates and persists a pin (capturing the pre-pin image for
// restore) WITHOUT publishing (no git). Returns the app, target env record, and
// pinned tag so the caller can batch the publish. Shared by the single-app op
// and the stack fan-out.
func (ah *appHandler) pinAppEnvSpec(ctx context.Context, projectName, appName, envName, fromPreview string) (*domain.App, *domain.AppEnvironment, string, error) {
	app, err := ah.appStore.GetApp(ctx, projectName, appName)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: app %q in project %q", errPinAppNotFound, appName, projectName)
	}
	if app.Spec.IsDirect() {
		return nil, nil, "", errPinNotPipeline
	}
	// The target must be a stable env; the source must be a preview with an image.
	targetEnv, err := ah.appStore.GetAppEnvironment(ctx, projectName, appName, envName)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%w: %q for app %q", errPinTargetNotFound, envName, appName)
	}
	if targetEnv.EnvType == domain.AppEnvPreview {
		return nil, nil, "", errPinTargetIsPreview
	}
	preview, err := ah.appStore.GetAppEnvironment(ctx, projectName, appName, fromPreview)
	if err != nil || preview.EnvType != domain.AppEnvPreview {
		return nil, nil, "", fmt.Errorf("%w: %q for app %q", errPinPreviewNotFound, fromPreview, appName)
	}
	if preview.Release == nil || strings.TrimSpace(preview.Release.Tag) == "" {
		return nil, nil, "", fmt.Errorf("%w: %q", errPinNoTag, fromPreview)
	}

	// Capture the env's current deployed image tag before pinning, so unpinning
	// can restore it (best-effort: empty when the env isn't deployed yet).
	prePin := ""
	if app.Spec.EnvironmentDefaults[envName].PinnedFrom == "" { // don't overwrite an existing capture on re-pin
		// Read the current image from Kargo's stage freight rather than a live
		// workload-cluster status enrichment: it's a single Freight lookup (the
		// same source unpin uses to restore), not a full ArgoCD/runtime round
		// trip — so a large pin fan-out doesn't pay a heavy per-member live read.
		// Bounded so a slow tooling cluster can't hang the request; best-effort
		// (unpin has its own restore fallback when this is empty).
		if fr, ok := ah.kargoPromoter.(kargoFreightReader); ok {
			frCtx, cancel := context.WithTimeout(ctx, liveReadTimeout)
			if tag, ferr := fr.CurrentFreightImageTag(frCtx, projectName, appName, envName, appImageRepoSubstr(app)); ferr == nil {
				prePin = tag
			}
			cancel()
		}
	} else {
		prePin = app.Spec.EnvironmentDefaults[envName].PrePinImageTag
	}

	if app.Spec.EnvironmentDefaults == nil {
		app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
	}
	ov := app.Spec.EnvironmentDefaults[envName]
	ov.PinnedImageTag = strings.TrimSpace(preview.Release.Tag)
	ov.PinnedFrom = fromPreview
	if prePin != "" && prePin != ov.PinnedImageTag {
		ov.PrePinImageTag = prePin
	}
	app.Spec.EnvironmentDefaults[envName] = ov
	if err := ah.appStore.SaveApp(ctx, projectName, app); err != nil {
		return nil, nil, "", fmt.Errorf("failed to save app: %w", err)
	}
	return app, targetEnv, ov.PinnedImageTag, nil
}

// pinAppEnv pins a PR preview's built image to a stable env WITHOUT merging: the
// env's values.yaml holds the preview's image tag and Kargo auto-promotion to
// that stage is paused. Mutates the spec then publishes in one batched op.
func (ah *appHandler) pinAppEnv(ctx context.Context, projectName, appName, envName, fromPreview string) (string, error) {
	app, targetEnv, tag, err := ah.pinAppEnvSpec(ctx, projectName, appName, envName, fromPreview)
	if err != nil {
		return "", err
	}
	if err := ah.republishAppsFocus(ctx, []appFocusPublish{{app: app, focusEnv: targetEnv}}); err != nil {
		return "", fmt.Errorf("failed to publish pin: %w", err)
	}
	return tag, nil
}

// unpinAppEnvSpec clears a pin, computing the restore image, and persists it
// WITHOUT publishing (no git). Returns the app, target env record (nil if the
// env has no record), restore tag, and wasPinned (false = no-op success).
func (ah *appHandler) unpinAppEnvSpec(ctx context.Context, projectName, appName, envName string) (*domain.App, *domain.AppEnvironment, string, bool, error) {
	app, err := ah.appStore.GetApp(ctx, projectName, appName)
	if err != nil {
		return nil, nil, "", false, fmt.Errorf("%w: app %q in project %q", errPinAppNotFound, appName, projectName)
	}
	ov := app.Spec.EnvironmentDefaults[envName]
	if ov.PinnedFrom == "" {
		return app, nil, "", false, nil
	}

	// Restore the pre-pin image (if captured) by force-writing it ONCE: clearing
	// PinnedFrom re-enables Kargo auto-promotion while PinnedImageTag=prePin makes
	// the publisher write the restored tag over the committed pinned one.
	restore := ov.PrePinImageTag
	// No capture (e.g. pinned before capture existed, or pinned when the env was
	// already on the preview image): ask Kargo for the env's real image — its
	// current stage freight, else the latest the Warehouse discovered (what Kargo
	// would deploy next). Match by the app's image repo so we don't pick another
	// app's freight from the shared project namespace. Falls through to a plain
	// clear if Kargo can't tell us — then Kargo reasserts on the next promote.
	if restore == "" {
		repo := appImageRepoSubstr(app)
		if fr, ok := ah.kargoPromoter.(kargoFreightReader); ok {
			// Bound the Kargo freight reads so a slow tooling cluster can't hang a
			// stack unpin fan-out (best-effort restore; falls through to a plain clear).
			frCtx, cancel := context.WithTimeout(ctx, liveReadTimeout)
			if tag, ferr := fr.CurrentFreightImageTag(frCtx, projectName, appName, envName, repo); ferr == nil && tag != "" && tag != ov.PinnedImageTag {
				restore = tag
			}
			if restore == "" {
				if tag, ferr := fr.LatestFreightImageTag(frCtx, projectName, appName, repo); ferr == nil && tag != "" && tag != ov.PinnedImageTag {
					restore = tag
				}
			}
			cancel()
		}
	}
	ov.PinnedFrom = ""
	ov.PrePinImageTag = ""
	ov.PinnedImageTag = restore // "" when nothing captured
	app.Spec.EnvironmentDefaults[envName] = ov
	if err := ah.appStore.SaveApp(ctx, projectName, app); err != nil {
		return nil, nil, "", true, fmt.Errorf("failed to save app: %w", err)
	}
	targetEnv, _ := ah.appStore.GetAppEnvironment(ctx, projectName, appName, envName)
	return app, targetEnv, restore, true, nil
}

// unpinClearRestoreFlag clears the one-shot restore force-write flag after the
// unpin publish, handing tag ownership back to Kargo (preserve) / the seed.
func (ah *appHandler) unpinClearRestoreFlag(ctx context.Context, app *domain.App, envName, restore string) {
	if restore == "" {
		return
	}
	ov := app.Spec.EnvironmentDefaults[envName]
	ov.PinnedImageTag = ""
	app.Spec.EnvironmentDefaults[envName] = ov
	if err := ah.appStore.SaveApp(ctx, app.ProjectName, app); err != nil {
		slog.Warn("unpin: failed to clear restore flag", "project", app.ProjectName, "app", app.Name, "env", envName, "err", err)
	}
}

// unpinAppEnv clears a pin and republishes so the env returns to normal CD.
// Mutates the spec, publishes in one batched op, then clears the one-shot restore
// flag. Returns (restoredTag, wasPinned): wasPinned=false is a no-op success.
func (ah *appHandler) unpinAppEnv(ctx context.Context, projectName, appName, envName string) (string, bool, error) {
	app, targetEnv, restore, wasPinned, err := ah.unpinAppEnvSpec(ctx, projectName, appName, envName)
	if err != nil || !wasPinned {
		return restore, wasPinned, err
	}
	if err := ah.republishAppsFocus(ctx, []appFocusPublish{{app: app, focusEnv: targetEnv}}); err != nil {
		return "", true, fmt.Errorf("failed to publish unpin: %w", err)
	}
	ah.unpinClearRestoreFlag(ctx, app, envName, restore)
	return restore, true, nil
}

// handlePinAppEnv serves POST .../apps/{app}/environments/{env}/pin.
func (ah *appHandler) handlePinAppEnv(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	var req pinAppEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	req.FromPreview = strings.TrimSpace(req.FromPreview)
	if req.FromPreview == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "fromPreview is required"})
		return
	}
	// The pin itself (spec mutation + git clone/commit/push) is the slow part;
	// defer it when the caller opts in (Prefer: respond-async / ?async=1) so the
	// gateway doesn't 504. Validation above already ran synchronously.
	op := func(ctx context.Context) (int, any, error) {
		tag, err := ah.pinAppEnv(ctx, projectName, appName, envName, req.FromPreview)
		if err != nil {
			return statusForPinErr(err), nil, err
		}
		return http.StatusOK, map[string]string{
			"message":  "environment " + envName + " pinned to " + pinLabel(req.FromPreview, tag),
			"project":  projectName,
			"app":      appName,
			"env":      envName,
			"imageTag": tag,
			"from":     req.FromPreview,
		}, nil
	}
	dispatchOp(w, r, ah.async, "pin-app", projectName, op)
}

// handleUnpinAppEnv serves DELETE .../apps/{app}/environments/{env}/pin.
func (ah *appHandler) handleUnpinAppEnv(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	op := func(ctx context.Context) (int, any, error) {
		restore, wasPinned, err := ah.unpinAppEnv(ctx, projectName, appName, envName)
		if err != nil {
			return statusForPinErr(err), nil, err
		}
		if !wasPinned {
			return http.StatusOK, map[string]string{"message": "environment " + envName + " is not pinned", "project": projectName, "app": appName, "env": envName}, nil
		}
		msg := "environment " + envName + " unpinned; normal delivery resumed"
		if restore != "" {
			msg = "environment " + envName + " unpinned; restored " + restore
		}
		return http.StatusOK, map[string]string{"message": msg, "project": projectName, "app": appName, "env": envName}, nil
	}
	dispatchOp(w, r, ah.async, "unpin-app", projectName, op)
}

// Sentinels for rollback, mapped to HTTP status by statusForRollbackErr.
var (
	errRollbackUnavailable    = errors.New("rollback unavailable: Kargo integration is not enabled")
	errRollbackFreightMissing = errors.New("that build is not in this environment's deploy history (it may have been cleaned up)")
	errRollbackCurrent        = errors.New("that build is already the one deployed")
)

func statusForRollbackErr(err error) int {
	switch {
	case errors.Is(err, errRollbackUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, errPinAppNotFound), errors.Is(err, errPinTargetNotFound):
		return http.StatusNotFound
	case errors.Is(err, errPinNotPipeline), errors.Is(err, errPinTargetIsPreview),
		errors.Is(err, errRollbackFreightMissing), errors.Is(err, errRollbackCurrent):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// rollbackAppEnvSpec validates a rollback and places the HOLD on the spec
// WITHOUT publishing: PinnedFrom="rollback" pauses Kargo auto-promotion for the
// stage (the pinned policy) so newer freight doesn't immediately re-promote over
// the rolled-back one, and — when the target freight runs a single distinct tag —
// PinnedImageTag holds that tag across republishes. A multi-tag freight leaves
// PinnedImageTag empty: the promotion writes each component's own tag and the
// CD-managed preserve path keeps them on republish (a single pinned tag would
// clobber them). Returns the app, env record, and the freight record.
func (ah *appHandler) rollbackAppEnvSpec(ctx context.Context, projectName, appName, envName, freightName string) (*domain.App, *domain.AppEnvironment, *KargoFreightRecord, error) {
	historian, ok := ah.kargoPromoter.(kargoFreightHistorian)
	if !ok {
		return nil, nil, nil, errRollbackUnavailable
	}
	app, err := ah.appStore.GetApp(ctx, projectName, appName)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: app %q in project %q", errPinAppNotFound, appName, projectName)
	}
	if app.Spec.IsDirect() {
		return nil, nil, nil, errPinNotPipeline
	}
	targetEnv, err := ah.appStore.GetAppEnvironment(ctx, projectName, appName, envName)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %q for app %q", errPinTargetNotFound, envName, appName)
	}
	if targetEnv.EnvType == domain.AppEnvPreview {
		return nil, nil, nil, errPinTargetIsPreview
	}

	records, err := historian.StageFreightHistory(ctx, projectName, appName, envName, 0)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read deploy history: %w", err)
	}
	var rec *KargoFreightRecord
	for i := range records {
		if records[i].Name == freightName {
			rec = &records[i]
			break
		}
	}
	if rec == nil {
		return nil, nil, nil, errRollbackFreightMissing
	}
	if rec.Current {
		return nil, nil, nil, errRollbackCurrent
	}

	// Capture the pre-rollback tag once (mirrors pinAppEnvSpec) so "Resume CD"
	// can restore it; a rollback over an existing hold keeps the original capture.
	prePin := app.Spec.EnvironmentDefaults[envName].PrePinImageTag
	if app.Spec.EnvironmentDefaults[envName].PinnedFrom == "" {
		if fr, ok := ah.kargoPromoter.(kargoFreightReader); ok {
			frCtx, cancel := context.WithTimeout(ctx, liveReadTimeout)
			if tag, ferr := fr.CurrentFreightImageTag(frCtx, projectName, appName, envName, appImageRepoSubstr(app)); ferr == nil {
				prePin = tag
			}
			cancel()
		}
	}

	if app.Spec.EnvironmentDefaults == nil {
		app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
	}
	ov := app.Spec.EnvironmentDefaults[envName]
	ov.PinnedFrom = rollbackPinnedFrom
	ov.PinnedImageTag = singleDistinctTag(rec.Images)
	if prePin != "" && prePin != ov.PinnedImageTag {
		ov.PrePinImageTag = prePin
	}
	app.Spec.EnvironmentDefaults[envName] = ov
	if err := ah.appStore.SaveApp(ctx, projectName, app); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to save app: %w", err)
	}
	return app, targetEnv, rec, nil
}

// singleDistinctTag returns the tag shared by every image of a freight, or ""
// when the freight carries several distinct tags (or none).
func singleDistinctTag(images []KargoFreightImage) string {
	tag := ""
	for _, im := range images {
		if im.Tag == "" {
			continue
		}
		if tag == "" {
			tag = im.Tag
		} else if im.Tag != tag {
			return ""
		}
	}
	return tag
}

// rollbackAppEnv places the rollback hold, republishes (auto-promotion off +
// tag hold committed), then re-promotes the historical freight through Kargo —
// the same promotion steps a normal promote runs, just with older freight. If
// the promotion can't be created, the hold is reverted best-effort so the env
// isn't left paused for nothing.
func (ah *appHandler) rollbackAppEnv(ctx context.Context, projectName, appName, envName, freightName string) (*KargoFreightRecord, KargoPromotionResult, error) {
	app, targetEnv, rec, err := ah.rollbackAppEnvSpec(ctx, projectName, appName, envName, freightName)
	if err != nil {
		return nil, KargoPromotionResult{}, err
	}
	if err := ah.republishAppsFocus(ctx, []appFocusPublish{{app: app, focusEnv: targetEnv}}); err != nil {
		return nil, KargoPromotionResult{}, fmt.Errorf("failed to publish rollback hold: %w", err)
	}
	historian := ah.kargoPromoter.(kargoFreightHistorian) // asserted in rollbackAppEnvSpec
	promo, err := historian.PromoteFreight(ctx, projectName, appName, envName, freightName)
	if err != nil {
		if _, _, _, _, uerr := ah.unpinAppEnvSpec(ctx, projectName, appName, envName); uerr == nil {
			_ = ah.republishAppsFocus(ctx, []appFocusPublish{{app: app, focusEnv: targetEnv}})
		}
		return nil, KargoPromotionResult{}, fmt.Errorf("failed to create rollback promotion: %w", err)
	}
	return rec, promo, nil
}

type rollbackAppEnvRequest struct {
	// Freight is the Kargo Freight name to roll back to — one of the entries
	// returned by GET .../rollback-candidates.
	Freight string `json:"freight"`
}

// handleRollbackAppEnv serves POST .../apps/{app}/environments/{env}/rollback.
func (ah *appHandler) handleRollbackAppEnv(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	var req rollbackAppEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	req.Freight = strings.TrimSpace(req.Freight)
	if req.Freight == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "freight is required"})
		return
	}
	op := func(ctx context.Context) (int, any, error) {
		rec, promo, err := ah.rollbackAppEnv(ctx, projectName, appName, envName, req.Freight)
		if err != nil {
			return statusForRollbackErr(err), nil, err
		}
		images := make([]RollbackCandidateImageDTO, 0, len(rec.Images))
		for _, im := range rec.Images {
			images = append(images, RollbackCandidateImageDTO{Repository: im.RepoURL, Tag: im.Tag})
		}
		return http.StatusOK, map[string]any{
			"message":   "rolling back " + envName + " — CD paused until you resume it",
			"project":   projectName,
			"app":       appName,
			"env":       envName,
			"freight":   rec.Name,
			"images":    images,
			"promotion": promo.Name,
		}, nil
	}
	dispatchOp(w, r, ah.async, "rollback-app", projectName, op)
}

// handleGetAppEnvRollbackCandidates serves
// GET .../apps/{app}/environments/{env}/rollback-candidates: the freight this
// env has run (newest first, first = current), for the rollback picker.
// Best-effort: no Kargo integration (or a read failure) yields available=false
// rather than an error — the UI then simply doesn't offer rollback.
func (ah *appHandler) handleGetAppEnvRollbackCandidates(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	empty := RollbackCandidatesResponse{Available: false, Candidates: []RollbackCandidateDTO{}}
	historian, ok := ah.kargoPromoter.(kargoFreightHistorian)
	if !ok {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	app, err := ah.appStore.GetApp(r.Context(), projectName, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "app not found: " + appName})
		return
	}
	if app.Spec.IsDirect() {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	records, err := historian.StageFreightHistory(r.Context(), projectName, appName, envName, 20)
	if err != nil {
		slog.Debug("rollback candidates: freight history read failed",
			"project", projectName, "app", appName, "env", envName, "err", err)
		writeJSON(w, http.StatusOK, empty)
		return
	}
	dtos := make([]RollbackCandidateDTO, 0, len(records))
	for _, rec := range records {
		images := make([]RollbackCandidateImageDTO, 0, len(rec.Images))
		for _, im := range rec.Images {
			images = append(images, RollbackCandidateImageDTO{Repository: im.RepoURL, Tag: im.Tag})
		}
		dtos = append(dtos, RollbackCandidateDTO{
			Freight:      rec.Name,
			Images:       images,
			DiscoveredAt: rec.DiscoveredAt,
			Current:      rec.Current,
		})
	}
	writeJSON(w, http.StatusOK, RollbackCandidatesResponse{Available: true, Candidates: dtos})
}

// Sentinels for suspend/resume, mapped to HTTP status by statusForSuspendErr.
var (
	errSuspendAppNotFound = errors.New("app not found")
	errSuspendEnvNotFound = errors.New("environment not found")
	errSuspendPreview     = errors.New("cannot suspend a preview environment")
)

// statusForSuspendErr maps a suspendAppEnv error to an HTTP status.
func statusForSuspendErr(err error) int {
	switch {
	case errors.Is(err, errSuspendAppNotFound), errors.Is(err, errSuspendEnvNotFound):
		return http.StatusNotFound
	case errors.Is(err, errSuspendPreview):
		return http.StatusUnprocessableEntity
	default:
		return http.StatusInternalServerError
	}
}

// suspendIsSkippable reports whether a suspend failure means the op does not
// apply to this member (a skip row in a stack fan-out) rather than a real error:
// a member not deployed to the target env.
func suspendIsSkippable(err error) bool {
	return errors.Is(err, errSuspendEnvNotFound)
}

// suspendAppEnvSpec validates and persists the suspend flag on a stable env
// WITHOUT publishing (no git). It returns the app + target env record so the
// caller can batch the publish. Shared by the single-app op and the stack
// fan-out (which mutates every member's spec, then publishes them in one commit).
func (ah *appHandler) suspendAppEnvSpec(ctx context.Context, projectName, appName, envName string, suspend bool) (*domain.App, *domain.AppEnvironment, error) {
	app, err := ah.appStore.GetApp(ctx, projectName, appName)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: app %q in project %q", errSuspendAppNotFound, appName, projectName)
	}
	targetEnv, err := ah.appStore.GetAppEnvironment(ctx, projectName, appName, envName)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %q for app %q", errSuspendEnvNotFound, envName, appName)
	}
	if targetEnv.EnvType == domain.AppEnvPreview {
		return nil, nil, errSuspendPreview
	}
	if app.Spec.EnvironmentDefaults == nil {
		app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
	}
	ov := app.Spec.EnvironmentDefaults[envName]
	if suspend {
		t := true
		ov.Suspend = &t
	} else {
		ov.Suspend = nil // resume: drop back to the chart default (running)
	}
	app.Spec.EnvironmentDefaults[envName] = ov
	if err := ah.appStore.SaveApp(ctx, projectName, app); err != nil {
		return nil, nil, fmt.Errorf("failed to save app: %w", err)
	}
	return app, targetEnv, nil
}

// suspendAppEnv sets (suspend=true) or clears (suspend=false) the suspend flag on
// a stable env and publishes just that env's values (where the flag lives). When
// suspended the publisher writes the template's suspend values key so the chart
// scales the workload down; the env stays published (no data loss, unlike
// undeploy). Works for pipeline and direct apps.
func (ah *appHandler) suspendAppEnv(ctx context.Context, projectName, appName, envName string, suspend bool) error {
	app, targetEnv, err := ah.suspendAppEnvSpec(ctx, projectName, appName, envName, suspend)
	if err != nil {
		return err
	}
	return ah.republishAppsEnv(ctx, []AppEnvTarget{{App: app, Env: targetEnv}})
}

// Sentinels for the stack-level target-clusters fan-out: a member not deployed
// to the env is a skip (not applicable), the rest are real errors.
var (
	errTargetAppNotFound = errors.New("app not found")
	errTargetEnvNotFound = errors.New("environment not found")
	errTargetIsPreview   = errors.New("cannot target a preview environment")
)

// targetClustersIsSkippable reports whether a set-target-clusters failure means
// the op doesn't apply to this member (it isn't deployed to the env) rather than
// a real error — a skip row in a stack fan-out.
func targetClustersIsSkippable(err error) bool {
	return errors.Is(err, errTargetEnvNotFound)
}

// setAppEnvTargetClustersSpec sets (or clears, when clusters is empty) the
// per-env TargetClusters selection on one app WITHOUT publishing (no git),
// returning the app + target env record so the caller can batch the publish.
// Shared by the stack fan-out; per-app selection goes through PATCH app.
// Callers must validate clusters against the env's ClusterRefs first
// (validateTargetClusters). Changing targets rewrites the env's _targets/ app
// files AND the Kargo Stage's argocd-update app list, so publish the full app
// tree (republishAppsFocus), not just the env values.
func (ah *appHandler) setAppEnvTargetClustersSpec(ctx context.Context, projectName, appName, envName string, clusters []string) (*domain.App, *domain.AppEnvironment, error) {
	app, err := ah.appStore.GetApp(ctx, projectName, appName)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: app %q in project %q", errTargetAppNotFound, appName, projectName)
	}
	targetEnv, err := ah.appStore.GetAppEnvironment(ctx, projectName, appName, envName)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %q for app %q", errTargetEnvNotFound, envName, appName)
	}
	if targetEnv.EnvType == domain.AppEnvPreview {
		return nil, nil, errTargetIsPreview
	}
	if app.Spec.EnvironmentDefaults == nil {
		app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{}
	}
	ov := app.Spec.EnvironmentDefaults[envName]
	if len(clusters) == 0 {
		ov.TargetClusters = nil // clear → inherit the env DeployMode default
	} else {
		ov.TargetClusters = clusters
	}
	app.Spec.EnvironmentDefaults[envName] = ov
	if err := ah.appStore.SaveApp(ctx, projectName, app); err != nil {
		return nil, nil, fmt.Errorf("failed to save app: %w", err)
	}
	return app, targetEnv, nil
}

// republishAppsEnv publishes one env's values for many apps. It ensures each
// target env's namespace, then uses the batched BatchEnvPublisher (a single
// clone/commit/push) when the publisher supports it, else falls back to a
// per-app PublishAppEnv. This is what lets a stack suspend/resume fan out to N
// members in ONE git round-trip instead of N (the 504 fix).
func (ah *appHandler) republishAppsEnv(ctx context.Context, targets []AppEnvTarget) error {
	if ah.gitOpsPublisher == nil || len(targets) == 0 {
		return nil
	}
	for _, t := range targets {
		ah.resolveEnvNamespaces(ctx, t.App, []*domain.AppEnvironment{t.Env})
		ah.ensureAppNamespace(ctx, t.App, t.Env)
	}
	if b, ok := ah.gitOpsPublisher.(BatchEnvPublisher); ok {
		return b.PublishAppsEnv(ctx, targets)
	}
	for _, t := range targets {
		if err := ah.gitOpsPublisher.PublishAppEnv(ctx, t.App, t.Env); err != nil {
			return err
		}
	}
	return nil
}

// handleSuspendAppEnv serves POST .../apps/{app}/environments/{env}/suspend.
func (ah *appHandler) handleSuspendAppEnv(w http.ResponseWriter, r *http.Request) {
	ah.serveSuspendAppEnv(w, r, true)
}

// handleResumeAppEnv serves POST .../apps/{app}/environments/{env}/resume.
func (ah *appHandler) handleResumeAppEnv(w http.ResponseWriter, r *http.Request) {
	ah.serveSuspendAppEnv(w, r, false)
}

func (ah *appHandler) serveSuspendAppEnv(w http.ResponseWriter, r *http.Request, suspend bool) {
	projectName := r.PathValue("project")
	appName := r.PathValue("app")
	envName := r.PathValue("env")

	if err := ah.suspendAppEnv(r.Context(), projectName, appName, envName, suspend); err != nil {
		writeJSON(w, statusForSuspendErr(err), errorResponse{Error: err.Error()})
		return
	}
	verb := "resumed"
	if suspend {
		verb = "suspended"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"message": "environment " + envName + " " + verb,
		"project": projectName,
		"app":     appName,
		"env":     envName,
	})
}

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
	// errPromoteTargetNotReady means the target env's manifests are published
	// but ArgoCD has not generated its Application yet. Retryable — 409, not
	// 4xx-permanent or 5xx: the git side is done and a later retry succeeds
	// once the ApplicationSet reconciles.
	errPromoteTargetNotReady = errors.New("target environment not ready")
)

// statusForPromoteErr maps a promoteAppEnv error to an HTTP status.
func statusForPromoteErr(err error) int {
	switch {
	case errors.Is(err, errPromoteAppNotFound):
		return http.StatusNotFound
	case errors.Is(err, errPromoteBadRequest), errors.Is(err, domainapp.ErrNoRelease):
		return http.StatusBadRequest
	case errors.Is(err, errPromoteTargetNotReady):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// waitForTargetArgoApp polls the ArgoCD Application gate until the target
// env's generated Application exists, the timeout lapses, or ctx is done.
//
// Fail-open on gate ERRORS (a broken dynamic client must not freeze every
// promotion — log and proceed) but fail-closed on definitive absence. Nil gate
// (fake mode, no dynamic client) means no check, preserving prior behavior.
func (ah *appHandler) waitForTargetArgoApp(ctx context.Context, projectName, appName, envName string) error {
	if ah.argoAppGate == nil {
		return nil
	}
	timeout := ah.argoAppWaitTimeout
	if timeout <= 0 {
		timeout = defaultArgoAppWaitTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		exists, err := ah.argoAppGate.HasAppForEnv(ctx, projectName, appName, envName)
		if err != nil {
			slog.Warn("promote: ArgoCD application check failed — proceeding without the gate",
				"project", projectName, "app", appName, "env", envName, "err", err)
			return nil
		}
		if exists {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s manifests are published, but ArgoCD has not generated the %s Application yet (the ApplicationSet can take up to its poll interval, ~3 min, on a first promotion) — retry shortly; nothing was promoted",
				errPromoteTargetNotReady, envName, envName)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", errPromoteTargetNotReady, ctx.Err())
		case <-time.After(2 * time.Second):
		}
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
	if app.Spec.IsDirect() {
		return nil, fmt.Errorf("%w: direct-delivery apps deploy each environment from its values; promotion is disabled", errPromoteBadRequest)
	}

	targetEnv, err := ah.appStore.GetAppEnvironment(ctx, projectName, appName, targetEnvName)
	if err != nil {
		return nil, fmt.Errorf("%w: environment %q not found for app %q", errPromoteBadRequest, targetEnvName, appName)
	}
	if targetEnv.EnvType == domain.AppEnvPreview {
		return nil, fmt.Errorf("%w: cannot promote to a preview environment", errPromoteBadRequest)
	}
	// A pinned env is frozen to a specific image — promotion (auto or manual) is
	// paused until it's unpinned.
	if ov := app.Spec.EnvironmentDefaults[targetEnvName]; ov.PinnedFrom != "" {
		return nil, fmt.Errorf("%w: environment %q is pinned to %s; unpin it first", errPromoteBadRequest, targetEnvName, pinLabel(ov.PinnedFrom, ov.PinnedImageTag))
	}
	// A decommissioned env has left the pipeline (no Kargo stage) — re-enable it
	// before promoting, otherwise the promotion has no target stage.
	if ov := app.Spec.EnvironmentDefaults[targetEnvName]; ov.Deploy != nil && !*ov.Deploy {
		return nil, fmt.Errorf("%w: environment %q is decommissioned; re-enable it first", errPromoteBadRequest, targetEnvName)
	}

	// Resolve the source: the stable env with the highest Order strictly below
	// the target's Order (closest predecessor). Falls back to preview envs when
	// no stable predecessor exists.
	sourceEnv, err := ah.findPromotionSource(ctx, projectName, appName, targetEnv)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errPromoteBadRequest, err.Error())
	}

	// Write GitOps files for the target environment before triggering the
	// promotion, so ArgoCD can find app.yaml + values.yaml when Kargo (or the
	// store fallback) signals a sync. A publish failure ABORTS the promotion:
	// proceeding used to "succeed" while the target env had no manifests in
	// git — Kargo updated nothing deployable and the UI reported a green
	// promotion. Failing loudly here is the fix for that silent green.
	if ah.gitOpsPublisher != nil {
		ah.ensureAppNamespace(ctx, app, targetEnv)
		if pubErr := ah.gitOpsPublisher.PublishAppEnv(ctx, app, targetEnv); pubErr != nil {
			slog.Error("promote: failed to publish env files — aborting promotion",
				"project", projectName, "app", appName,
				"env", targetEnvName, "err", pubErr)
			return nil, fmt.Errorf("failed to publish %s manifests to gitops — promotion aborted (nothing was promoted): %w", targetEnvName, pubErr)
		}
	}

	// When Kargo is configured, create a Kargo Promotion CR. The Promotion CR
	// drives the actual release copy through the Kargo pipeline; suparship
	// then returns the Promotion details rather than the local release copy.
	if ah.kargoPromoter != nil {
		// Refuse to promote until the target env's ArgoCD Application exists.
		// The promotion's argocd-update step targets that Application by name;
		// against a nonexistent one, Kargo commits the git changes, deploys
		// nothing, and reports success. On the FIRST promotion to an env the
		// Application genuinely cannot exist yet — its app.yaml only entered
		// git in the publish above — so poll briefly for the ApplicationSet to
		// generate it, then return a retryable 409 rather than block the
		// request for the appset's full requeue interval.
		if err := ah.waitForTargetArgoApp(ctx, projectName, appName, targetEnvName); err != nil {
			return nil, err
		}
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

// findPromotionSource returns the source environment for a promotion to the given
// target: the stable env with the highest Order strictly below target.Order (the
// closest predecessor in the pipeline). Promotion advances freight between Kargo
// Stages, so the source must be a stable env — a preview has no Stage. Reaching
// the first stage (e.g. staging) is done by merging the PR (auto-promotes the
// merged build) or by pinning a preview, not by promotion.
func (ah *appHandler) findPromotionSource(ctx context.Context, projectName, appName string, target *domain.AppEnvironment) (*domain.AppEnvironment, error) {
	envs, err := ah.appStore.ListAppEnvironments(ctx, projectName, appName)
	if err != nil {
		return nil, fmt.Errorf("failed to list app environments")
	}

	// Stable candidates only (non-preview, Order < target.Order).
	var stableCandidates []*domain.AppEnvironment
	for _, env := range envs {
		if env.EnvName == target.EnvName || env.EnvType == domain.AppEnvPreview {
			continue
		}
		if env.Order < target.Order {
			stableCandidates = append(stableCandidates, env)
		}
	}

	if len(stableCandidates) == 0 {
		return nil, fmt.Errorf("%q has no upstream stage to promote from — it deploys merged builds automatically (merge the PR), or pin a preview to deploy without merging", target.EnvName)
	}

	// Pick the stable env with the highest Order (closest predecessor);
	// tie-break lexicographically for determinism.
	sort.Slice(stableCandidates, func(i, j int) bool {
		if stableCandidates[i].Order != stableCandidates[j].Order {
			return stableCandidates[i].Order > stableCandidates[j].Order
		}
		return stableCandidates[i].EnvName < stableCandidates[j].EnvName
	})
	return stableCandidates[0], nil
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
func (ah *appHandler) workloadClusterClient(ctx context.Context, projectName, appName, envName string) (kubernetes.Interface, error) {
	if ah.clusterPool == nil || ah.orgProvider == nil {
		return nil, nil
	}
	// Resolve the app's actual deploy clusters (env default ⊕ per-app
	// TargetClusters) so logs stream from the cluster the app really runs on —
	// not the env's default cluster, which for a per-app-targeted app has no pods.
	refs := ah.appDeployClusterRefs(ctx, projectName, appName, envName)
	if len(refs) == 0 {
		return nil, nil // env not bound to a cluster — single-cluster/local mode
	}
	// Logs stream from a single cluster; use the app's first target (which is the
	// env's active cluster for an untargeted app).
	ref := refs[0]
	client, err := ah.clusterPool.Get(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("workload cluster %q: %w", ref, err)
	}
	return client, nil
}

// namedClient pairs a resolved workload-cluster client with its cluster name.
type namedClient struct {
	name   string
	client kubernetes.Interface
	dyn    dynamic.Interface // for Gateway-API HTTPRoute discovery; may be nil
}

// appDeployClusterRefs resolves the cluster refs an app actually deploys to in an
// env: the env's deploy targets (the active cluster in "active" mode, all bound
// clusters in "all" mode) narrowed/overridden by the app's per-env TargetClusters
// selection. This is the single source of truth for "where does this app run in
// this env", so live status, logs, and ArgoCD diagnostics all read the SAME
// clusters. Critically, an app targeted to a non-active cluster (per-app cluster
// targeting) resolves to that cluster here — without this, status/logs query the
// env's default cluster, find nothing, and falsely report "not deployed". Empty
// when the env is unbound (single-cluster / local mode). appName may be "" to get
// the plain env defaults (no per-app override).
func (ah *appHandler) appDeployClusterRefs(ctx context.Context, projectName, appName, envName string) []string {
	if ah.orgProvider == nil {
		return nil
	}
	org, err := ah.orgOnce(ctx)
	if err != nil || org == nil {
		return nil
	}
	var deployTargets, clusterRefs []string
	for _, e := range org.Environments {
		if e.Name == envName {
			deployTargets = e.ResolveDeployTargets()
			clusterRefs = e.ClusterRefs
			break
		}
	}
	if ah.appStore != nil && appName != "" {
		if app, err := ah.appOnce(ctx, projectName, appName); err == nil && app != nil {
			return domain.ResolveAppClusterTargets(
				app.Spec.EnvironmentDefaults[envName].TargetClusters, deployTargets, clusterRefs)
		}
	}
	return deployTargets
}

// workloadClustersForEnv resolves the clusters an app's workloads run on in an
// env (env deploy targets ⊕ the app's per-env TargetClusters) to Kubernetes
// clients. routed=false means no remote routing applies (no pool/org wired, or
// the env is unbound) — the caller uses its locally-injected provider. When
// routed, it returns a client per reachable target plus the names of targets it
// couldn't reach (surfaced as diagnostics, never silently dropped).
func (ah *appHandler) workloadClustersForEnv(ctx context.Context, projectName, appName, envName string) (clients []namedClient, unreachable []string, routed bool) {
	if ah.clusterPool == nil || ah.orgProvider == nil {
		return nil, nil, false
	}
	targets := ah.appDeployClusterRefs(ctx, projectName, appName, envName)
	if len(targets) == 0 {
		return nil, nil, false // unbound — single-cluster/local mode
	}
	for _, ref := range targets {
		c, err := ah.clusterPool.Get(ctx, ref)
		if err != nil {
			unreachable = append(unreachable, ref)
			continue
		}
		// Dynamic client is best-effort — HTTPRoute endpoint discovery is optional.
		dyn, _ := ah.clusterPool.GetDynamic(ctx, ref)
		clients = append(clients, namedClient{name: ref, client: c, dyn: dyn})
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

	// A preview runs on its base env's cluster (it clones that env), and its own
	// name (e.g. "pr-712") is not a configured org environment — so resolve the
	// workload cluster from the base env, else routing falls back to the local
	// cluster and the preview always reads as 0/0 "not deployed".
	routeEnv := env.EnvName
	if env.EnvType == domain.AppEnvPreview && env.BaseEnv != "" {
		routeEnv = env.BaseEnv
	}
	clients, unreachable, routed := ah.workloadClustersForEnv(ctx, env.ProjectName, appName, routeEnv)

	// instances are each component's workload identity: the app.kubernetes.io/
	// instance label (Helm release name) its pods carry. Single-source → the sole
	// workload labelled {app}; composed → one per component labelled
	// {app}-{component}. Querying per instance is what makes a COMPOSED app report
	// real status: its component workloads are NOT labelled instance={app}, so the
	// old single instance={app} query read every composed app as "not deployed".
	instances := ah.appWorkloadInstances(ctx, env.ProjectName, appName)

	// No remote routing (single-cluster / fake mode): query the local provider.
	if !routed {
		if ah.runtimeProvider == nil {
			return
		}
		// Prefer label-based app-native discovery when the provider is a real
		// K8s provider; fakes/tests only implement the name-based interface.
		if kp, ok := ah.runtimeProvider.(*runtime.K8sProvider); ok {
			ah.applyComponentRuntimes(env, instances, runtimeByComponent(ctx, kp, env.Namespace, instances))
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
			Hint:    "Live replica count for this cluster is unavailable (ArgoCD may still show it Healthy). Check the cluster's kubeconfig/credentials under Clusters and that its API server is reachable from suparship.",
			Cluster: name,
		})
	}
	if len(clients) == 0 {
		return // all targets unreachable; diagnostics above explain
	}

	// Query every reachable cluster concurrently (a fan-out "all" env otherwise
	// serializes one full read per cluster); each returns per-component infos we
	// then merge across clusters in cluster order so the worst-of phase, summed
	// replicas, and per-cluster diagnostics are deterministic.
	perCluster := make([]map[string]*runtime.RuntimeInfo, len(clients))
	runBounded(len(clients), appEnrichConcurrency, func(i int) {
		nc := clients[i]
		perCluster[i] = runtimeByComponent(ctx, runtime.NewK8sProvider(nc.client, nc.dyn), env.Namespace, instances)
	})

	// compAgg: component name → RuntimeInfo aggregated across all reachable clusters.
	compAgg := map[string]*runtime.RuntimeInfo{}
	got := false
	for i, nc := range clients {
		byComp := perCluster[i]
		if len(byComp) == 0 {
			continue
		}
		got = true
		clusterAgg := &runtime.RuntimeInfo{Status: runtime.StatusHealthy}
		for comp, info := range byComp {
			cur := compAgg[comp]
			if cur == nil {
				cur = &runtime.RuntimeInfo{Status: runtime.StatusHealthy}
				compAgg[comp] = cur
			}
			mergeRuntime(cur, info)
			mergeRuntime(clusterAgg, info)
		}
		if len(clients) > 1 {
			env.Status.Diagnostics = append(env.Status.Diagnostics, domain.Diagnostic{
				Source:  "runtime",
				Level:   domain.DiagnosticInfo,
				Title:   fmt.Sprintf("Cluster %s: %s", nc.name, clusterAgg.Status),
				Detail:  fmt.Sprintf("%d/%d replicas available", clusterAgg.Available, clusterAgg.Replicas),
				Cluster: nc.name,
			})
		}
	}
	if !got {
		return
	}
	ah.applyComponentRuntimes(env, instances, compAgg)
}

// appWorkloadInstances resolves the app's per-component workload identities,
// falling back to a single instance={app} handle when the app record can't be
// read (so status still degrades gracefully to the pre-composed behaviour).
func (ah *appHandler) appWorkloadInstances(ctx context.Context, project, appName string) []domain.WorkloadInstance {
	if ah.appStore != nil {
		if app, err := ah.appStore.GetApp(ctx, project, appName); err == nil && app != nil {
			if wis := app.WorkloadInstances(); len(wis) > 0 {
				return wis
			}
		}
	}
	return []domain.WorkloadInstance{{Instance: appName}}
}

// appStatefulComponents returns the names of a COMPOSED app's stateful components
// (each of which renders as its own {argoAppName}-{component} ArgoCD Application).
// Nil for single-source apps or when the app record can't be read.
func (ah *appHandler) appStatefulComponents(ctx context.Context, project, appName string) []string {
	if ah.appStore == nil {
		return nil
	}
	app, err := ah.appStore.GetApp(ctx, project, appName)
	if err != nil || app == nil || !app.Spec.IsComposed() {
		return nil
	}
	var names []string
	for _, c := range app.Spec.StatefulComponents() {
		names = append(names, c.Name)
	}
	return names
}

// runtimeByComponent queries each component's workload instance on one cluster's
// provider, returning component-name → live RuntimeInfo for the components a read
// succeeded for. The fallback name is the instance itself: a composed component's
// workload fullname IS {app}-{component}, so the name-based fallback still resolves.
//
// One-shot components (job/cron) are skipped entirely: they have no steady-state
// running pods, so a running-replica read would report "not deployed" — dragging
// the composed app's worst-of aggregate down and showing the component itself as
// undeployed. Omitting them keeps them out of the aggregate and the per-component
// list, so the UI renders their row at the app's phase with "—" replicas. A
// failed migration still surfaces via the app's ArgoCD sync diagnostics.
func runtimeByComponent(ctx context.Context, kp *runtime.K8sProvider, namespace string, instances []domain.WorkloadInstance) map[string]*runtime.RuntimeInfo {
	out := make(map[string]*runtime.RuntimeInfo, len(instances))
	for _, wi := range instances {
		if wi.OneShot {
			continue
		}
		if info, err := kp.GetAppRuntime(ctx, namespace, wi.Instance, wi.Instance); err == nil {
			out[wi.Component] = info
		}
	}
	return out
}

// mergeRuntime folds src into dst: worst-of phase, summed replicas, union of
// ingress URLs, latest deploy timestamp, first non-empty image. dst must be non-nil.
func mergeRuntime(dst, src *runtime.RuntimeInfo) {
	if src == nil {
		return
	}
	if runtimeStatusRank[src.Status] >= runtimeStatusRank[dst.Status] {
		dst.Status = src.Status
	}
	dst.Replicas += src.Replicas
	dst.Available += src.Available
	if src.LastDeployed > dst.LastDeployed {
		dst.LastDeployed = src.LastDeployed
	}
	if dst.Image == "" {
		dst.Image = src.Image
	}
	dst.IngressURLs = append(dst.IngressURLs, src.IngressURLs...)
}

// applyComponentRuntimes aggregates per-component infos into the env-level status
// (worst-of phase, summed replicas) and, for a composed app (>1 component),
// records the per-component breakdown on env.Status.Components. No-op when no
// component read succeeded, so a fully-not-deployed app keeps its stored status.
func (ah *appHandler) applyComponentRuntimes(env *domain.AppEnvironment, instances []domain.WorkloadInstance, byComp map[string]*runtime.RuntimeInfo) {
	agg := &runtime.RuntimeInfo{Status: runtime.StatusHealthy}
	got := false
	var comps []domain.ComponentRuntimeStatus
	for _, wi := range instances { // deterministic order
		info := byComp[wi.Component]
		if info == nil {
			continue
		}
		got = true
		mergeRuntime(agg, info)
		if len(instances) > 1 {
			comps = append(comps, domain.ComponentRuntimeStatus{
				Component: wi.Component,
				Phase:     info.Status,
				Replicas:  info.Replicas,
				Available: info.Available,
			})
		}
	}
	if !got {
		return
	}
	ah.applyRuntimeInfo(env, agg)
	env.Status.Components = comps
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
	if ah.orgProvider != nil {
		if org, err := ah.orgOnce(ctx); err == nil && org != nil {
			pattern = org.ResourceNaming.EffectiveArgoAppName()
		}
	}
	// Read the Applications on the clusters the app actually deploys to (env
	// default ⊕ per-app TargetClusters) — the same resolution the workload-status
	// path uses, so status and diagnostics never disagree on which clusters.
	clusters := ah.appDeployClusterRefs(ctx, projectName, appName, envName)
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

// argoNameCollision returns an existing app in the project whose ArgoCD Application
// name identity collides with candidate, or "" if none. When the org's ArgoAppName
// pattern folds the project prefix ({projectApp}, the default), two apps like
// "foo-bar" and "bar" in project "foo" both resolve to "foo-bar" and would produce
// duplicate ArgoCD Application names in the shared argocd namespace — so one must be
// rejected at create/rename. When the pattern doesn't dedup, the project prefix keeps
// names unique and this is a no-op. Best-effort: a list failure never blocks the op.
func (ah *appHandler) argoNameCollision(ctx context.Context, projectName, candidate, exclude string) string {
	pattern := secrets.DefaultArgoAppName
	if ah.orgProvider != nil {
		if org, err := ah.orgOnce(ctx); err == nil && org != nil {
			pattern = org.ResourceNaming.EffectiveArgoAppName()
		}
	}
	if !strings.Contains(pattern, "{projectApp}") {
		return ""
	}
	want := secrets.DedupProjectPrefix(projectName, candidate)
	apps, err := ah.appStore.ListApps(ctx, projectName)
	if err != nil {
		return ""
	}
	for _, a := range apps {
		if a.Name == exclude || a.Name == candidate {
			continue
		}
		if secrets.DedupProjectPrefix(projectName, a.Name) == want {
			return a.Name
		}
	}
	return ""
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
	// A composed app's stateful components each render as their OWN ArgoCD
	// Application named {argoAppName}-{component} (BuildComponentApplication), so a
	// failed/degraded database sync would otherwise produce no diagnostic. Read
	// each alongside the main app + platform companion.
	statefulComps := ah.appStatefulComponents(ctx, env.ProjectName, appName)
	// Only tag diagnostics with a cluster when the env actually fans out, to keep
	// single-cluster output unchanged.
	multi := len(apps) > 1
	// Prefer the per-request snapshot (one LIST for the whole page) when available;
	// otherwise fall back to per-app live Gets, which also surface read errors.
	lookup, useSnapshot := ah.diagLookup(ctx)
	for _, ca := range apps {
		cluster := ""
		if multi {
			cluster = ca.cluster
		}
		targets := []struct{ app, source string }{
			{ca.name, "argocd"},
			{ca.name + "-platform", "external-secrets"},
		}
		for _, comp := range statefulComps {
			targets = append(targets, struct{ app, source string }{ca.name + "-" + comp, "argocd"})
		}
		for _, t := range targets {
			var diags []domain.Diagnostic
			if useSnapshot {
				diags = lookup(t.app, t.source)
			} else {
				var err error
				diags, err = ah.diagnosticsReader.GetAppDiagnostics(ctx, t.app, t.source)
				if err != nil {
					// Don't silently drop a read failure (RBAC, throttling, API down) —
					// that would falsely present a broken env as having no problems.
					// Surface it as a warning so the operator knows status is unknown.
					env.Status.Diagnostics = append(env.Status.Diagnostics, domain.Diagnostic{
						Source:  t.source,
						Level:   domain.DiagnosticWarning,
						Title:   "Delivery status unavailable",
						Detail:  err.Error(),
						Hint:    "Could not read the ArgoCD Application status; the env's real health is unknown. Check suparship's access to the ArgoCD namespace and that ArgoCD is reachable.",
						Cluster: cluster,
					})
					continue
				}
			}
			for i := range diags {
				diags[i].Cluster = cluster
			}
			env.Status.Diagnostics = append(env.Status.Diagnostics, diags...)
		}
	}
}

// --- DTO mapping helpers ---

// buildEnvSummaryDTOs converts an app's environments to per-env summary DTOs in
// promotion order (stable envs by Order, previews last) and resolves each env's
// Deploy/IsBase flags. Shared by the list (summary) and detail views so both
// agree on ordering and which envs actually deploy. The first stable env (sorted
// first) is the base; for direct apps Deploy follows the opt-in rules, pipeline
// apps always report Deploy=true.
func buildEnvSummaryDTOs(app *domain.App, envs []*domain.AppEnvironment) []AppEnvironmentSummaryDTO {
	envDTOs := make([]AppEnvironmentSummaryDTO, 0, len(envs))
	for _, env := range envs {
		envDTOs = append(envDTOs, appEnvToDTO(env))
	}
	sort.SliceStable(envDTOs, func(i, j int) bool {
		ip, jp := envDTOs[i].EnvType == string(domain.AppEnvPreview), envDTOs[j].EnvType == string(domain.AppEnvPreview)
		if ip != jp {
			return !ip // non-preview envs first
		}
		if envDTOs[i].Order != envDTOs[j].Order {
			return envDTOs[i].Order < envDTOs[j].Order
		}
		return envDTOs[i].EnvName < envDTOs[j].EnvName
	})

	baseSeen := false
	for i := range envDTOs {
		if envDTOs[i].EnvType == string(domain.AppEnvPreview) {
			envDTOs[i].Deploy = true
			continue
		}
		isBase := !baseSeen
		baseSeen = true
		envDTOs[i].IsBase = isBase
		if app.Spec.IsDirect() {
			envDTOs[i].Deploy = app.Spec.DeploysToEnv(envDTOs[i].EnvName, isBase)
		} else {
			// Pipeline apps deploy every env by default; a decommissioned env
			// (Deploy explicitly false) reports false so the UI can show it left
			// the pipeline and offer a re-deploy.
			deploy := true
			if ov, ok := app.Spec.EnvironmentDefaults[envDTOs[i].EnvName]; ok && ov.Deploy != nil {
				deploy = *ov.Deploy
			}
			envDTOs[i].Deploy = deploy
		}
		if ov := app.Spec.EnvironmentDefaults[envDTOs[i].EnvName]; ov.PinnedFrom != "" {
			envDTOs[i].PinnedTag = ov.PinnedImageTag
			envDTOs[i].PinnedFrom = ov.PinnedFrom
		}
		if ov := app.Spec.EnvironmentDefaults[envDTOs[i].EnvName]; ov.Suspend != nil && *ov.Suspend {
			envDTOs[i].Suspended = true
		}
	}
	return envDTOs
}

// summaryPhase aggregates per-env status into a single phase for list views,
// considering only stable environments the app actually deploys to (Deploy=true).
// Mirrors the UI's overallPhase: healthy when all deployed envs are healthy,
// else degraded/progressing if any is, else not_deployed. Returns not_deployed
// when the app deploys to no stable env.
func summaryPhase(envs []AppEnvironmentSummaryDTO) string {
	phases := make([]string, 0, len(envs))
	for _, e := range envs {
		if e.EnvType == string(domain.AppEnvPreview) || !e.Deploy {
			continue
		}
		phases = append(phases, e.Status.Phase)
	}
	if len(phases) == 0 {
		return domain.StatusNotDeployed
	}
	allHealthy := true
	for _, p := range phases {
		if p != domain.StatusHealthy {
			allHealthy = false
			break
		}
	}
	if allHealthy {
		return domain.StatusHealthy
	}
	for _, p := range phases {
		if p == domain.StatusDegraded {
			return domain.StatusDegraded
		}
	}
	for _, p := range phases {
		if p == domain.StatusProgressing {
			return domain.StatusProgressing
		}
	}
	// Some envs healthy, the rest not yet deployed — treat as healthy (partial
	// rollout) rather than not_deployed so deployed envs are visible.
	for _, p := range phases {
		if p == domain.StatusHealthy {
			return domain.StatusHealthy
		}
	}
	return domain.StatusNotDeployed
}

func appToSummaryDTO(app *domain.App, envs []*domain.AppEnvironment) AppSummaryDTO {
	envDTOs := buildEnvSummaryDTOs(app, envs)

	dto := AppSummaryDTO{
		Name:        app.Name,
		Project:     app.ProjectName,
		DisplayName: app.Spec.DisplayName,
		Description: app.Spec.Description,
		Template: AppTemplateRefDTO{
			Name:    app.Spec.Template.Name,
			Version: app.Spec.Template.Version,
		},
		URLs:         []string{},
		Environments: envDTOs,
		Components:   componentDTOs(app.EffectiveComponents(), app.Spec.EnvironmentDefaults),
		Status:       AppStatusSummaryDTO{Phase: summaryPhase(envDTOs)},
	}

	// URLs: union across deployed stable environments (previews excluded).
	for _, e := range envDTOs {
		if e.EnvType == string(domain.AppEnvPreview) || !e.Deploy {
			continue
		}
		dto.URLs = append(dto.URLs, e.URLs...)
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
	return domain.CDConfig{Managed: dto.Managed, AutoPromote: dto.AutoPromote}
}

// appImageBindingsFromDTO converts wire image selections into domain selections.
// A nil/empty slice yields nil (no CD-managed images).
func appImageBindingsFromDTO(dtos []AppImageBindingDTO) []domain.AppImageBinding {
	if len(dtos) == 0 {
		return nil
	}
	out := make([]domain.AppImageBinding, 0, len(dtos))
	for _, d := range dtos {
		out = append(out, domain.AppImageBinding{
			Name:              d.Name,
			TagKey:            d.TagKey,
			TagPattern:        d.TagPattern,
			SelectionStrategy: d.SelectionStrategy,
		})
	}
	return out
}

// appImageBindingsToDTO is the inverse of appImageBindingsFromDTO.
func appImageBindingsToDTO(bindings []domain.AppImageBinding) []AppImageBindingDTO {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]AppImageBindingDTO, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, AppImageBindingDTO{
			Name:              b.Name,
			TagKey:            b.TagKey,
			TagPattern:        b.TagPattern,
			SelectionStrategy: b.SelectionStrategy,
		})
	}
	return out
}

// validateImageBindings rejects a CD image selection that lacks the identity
// fields (Name + TagKey) needed to discover its repository and write its tag at
// publish.
func validateImageBindings(bindings []domain.AppImageBinding) error {
	for _, b := range bindings {
		if strings.TrimSpace(b.Name) == "" || strings.TrimSpace(b.TagKey) == "" {
			return fmt.Errorf("image selection requires a name and tagKey")
		}
	}
	return nil
}

// validateCDImageSource rejects enabling CD-managed tag ownership (cd.managed) on
// an app that has no image for Kargo to watch — publish would produce no
// Warehouse subscription, so the CD pipeline would silently never promote.
// Catching it at the API gives the operator immediate, actionable feedback
// rather than a quietly broken Warehouse.
//
// The check mirrors every source the PUBLISHER actually watches, in order:
// the app-level image selection, the legacy app image_repository value, any
// component's stored selection (composed apps keep images per component —
// checking only the app level rejected every composed app, the bug this
// rewrite fixes), and finally — when the selection was never explicitly
// configured — the template-declared images that publish auto-binds, for the
// app's template and each component's own (a sync-safe Images override
// replaces a template's declared set, so it counts too).
//
// imagesConfigured is the value the app will have AFTER this request: once an
// operator has explicitly saved a selection, an empty one means "watch
// nothing" and auto-bind no longer applies, so template images stop counting.
func (ah *appHandler) validateCDImageSource(ctx context.Context, app *domain.App, imagesConfigured bool) error {
	if len(app.Spec.Images) > 0 {
		return nil
	}
	if repo, ok := app.Spec.Values["image_repository"].(string); ok && strings.TrimSpace(repo) != "" {
		return nil
	}
	for i := range app.Spec.Components {
		if len(app.Spec.Components[i].Images) > 0 {
			return nil
		}
	}
	if !imagesConfigured {
		names := make(map[string]bool)
		if app.Spec.Template.Name != "" {
			names[app.Spec.Template.Name] = true
		}
		for _, c := range app.Spec.Components {
			if c.Template != nil && c.Template.Name != "" {
				names[c.Template.Name] = true
			}
		}
		for name := range names {
			if ah.kubeClient != nil {
				if ov, err := kube.LoadTemplateOverride(ctx, ah.kubeClient, name); err == nil && ov != nil && len(ov.Images) > 0 {
					return nil
				}
			}
			if tmpl, ok := ah.lookupTemplate(ctx, name); ok && len(tmpl.Spec.Images) > 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("continuous delivery (cd.managed) needs an image for Kargo to watch: select at least one image under Images (on the app or a component), or set image_repository on the app")
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

	// Environments in promotion order with resolved Deploy/IsBase flags (shared
	// with the list view so both agree on ordering and deploy state).
	envDTOs := buildEnvSummaryDTOs(app, envs)

	return AppDetailDTO{
		Name:        app.Name,
		Project:     app.ProjectName,
		DisplayName: app.Spec.DisplayName,
		Description: app.Spec.Description,
		Template: AppTemplateRefDTO{
			Name:    app.Spec.Template.Name,
			Version: app.Spec.Template.Version,
		},
		Values:              values,
		SecretRefs:          secretRefs,
		Components:          componentDTOs(app.EffectiveComponents(), app.Spec.EnvironmentDefaults),
		Environments:        envDTOs,
		ClusterOverrides:    clusterOverridesDTO(app.Spec.EnvironmentDefaults),
		TargetClusters:      targetClustersDTO(app.Spec.EnvironmentDefaults),
		RawValues:           app.Spec.RawValues,
		EnvRawValues:        envRawValuesDTO(app.Spec.EnvironmentDefaults),
		ComponentConfigs:    componentConfigsDTO(app.Spec.Components),
		EnvComponents:       envComponentsDTO(app.Spec.EnvironmentDefaults),
		EnvTemplateVersions: envTemplateVersionsDTO(app.Spec.EnvironmentDefaults),
		CD:                  CDConfigDTO{Managed: app.Spec.CD.Managed, AutoPromote: app.Spec.CD.AutoPromote, ImagesConfigured: app.Spec.CD.ImagesConfigured},
		Images:              appImageBindingsToDTO(app.Spec.Images),
		DeliveryMode:        string(app.Spec.DeliveryMode),
		PreviewsEnabled:     app.Spec.PreviewsEnabled,
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
func envTemplateVersionsDTO(defaults map[string]domain.EnvironmentOverride) map[string]map[string]string {
	var out map[string]map[string]string
	for envName, ov := range defaults {
		if len(ov.TemplateVersions) == 0 {
			continue
		}
		if out == nil {
			out = map[string]map[string]string{}
		}
		out[envName] = ov.TemplateVersions
	}
	return out
}

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

// foldTargetClusters folds a per-env cluster-targeting selection (env →
// cluster names) into the app's EnvironmentDefaults, mirroring how
// ClusterOverrides is folded. An env with an empty list clears the override
// (inherit the env default); creates the per-env override record when nil.
func foldTargetClusters(defaults map[string]domain.EnvironmentOverride, targetClusters map[string][]string) map[string]domain.EnvironmentOverride {
	ed := defaults
	if ed == nil {
		ed = map[string]domain.EnvironmentOverride{}
	}
	for envName, clusters := range targetClusters {
		ov := ed[envName]
		if len(clusters) == 0 {
			ov.TargetClusters = nil
		} else {
			ov.TargetClusters = clusters
		}
		ed[envName] = ov
	}
	return ed
}

// validateTargetClusters rejects any per-env cluster-targeting selection that
// names a cluster not in that env's registered ClusterRefs. The
// AllClustersSentinel ("*") and an empty/omitted list are always allowed. If
// the org config can't be loaded or an env's ClusterRefs can't be resolved,
// strict validation for that env is skipped — the generation layer drops
// unknown refs anyway, so a lookup miss must not hard-fail the request.
func (ah *appHandler) validateTargetClusters(ctx context.Context, targetClusters map[string][]string) error {
	if len(targetClusters) == 0 || ah.orgProvider == nil {
		return nil
	}
	org, err := ah.orgProvider.GetOrg(ctx)
	if err != nil || org == nil {
		return nil
	}
	refsByEnv := make(map[string]map[string]struct{}, len(org.Environments))
	for _, e := range org.Environments {
		set := make(map[string]struct{}, len(e.ClusterRefs))
		for _, c := range e.ClusterRefs {
			set[c] = struct{}{}
		}
		refsByEnv[e.Name] = set
	}
	for envName, sel := range targetClusters {
		refs, ok := refsByEnv[envName]
		if !ok || len(refs) == 0 {
			continue // unknown env / unresolved refs → skip strict validation
		}
		for _, c := range sel {
			if c == domain.AllClustersSentinel {
				continue
			}
			if _, inEnv := refs[c]; !inEnv {
				return fmt.Errorf("environment %q: cluster %q is not one of the environment's registered clusters", envName, c)
			}
		}
	}
	return nil
}

// targetClustersDTO extracts the per-env cluster-targeting selection from the
// app's EnvironmentDefaults into the env → cluster-names map the API exposes.
// Mirrors clusterOverridesDTO. Returns nil when no env sets one.
func targetClustersDTO(defaults map[string]domain.EnvironmentOverride) map[string][]string {
	var out map[string][]string
	for envName, ov := range defaults {
		if len(ov.TargetClusters) == 0 {
			continue
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[envName] = ov.TargetClusters
	}
	return out
}

func appEnvToDTO(env *domain.AppEnvironment) AppEnvironmentSummaryDTO {
	urls := env.URLs
	if urls == nil {
		urls = []string{}
	}

	dto := AppEnvironmentSummaryDTO{
		EnvName:   env.EnvName,
		EnvType:   string(env.EnvType),
		Order:     env.Order,
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
			BaseEnv:     env.BaseEnv,
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
	for _, c := range s.Components {
		dto.Components = append(dto.Components, ComponentRuntimeStatusDTO{
			Component: c.Component,
			Phase:     c.Phase,
			Replicas:  c.Replicas,
			Available: c.Available,
		})
	}
	return dto
}

// resolveComponentSpecs turns wire component DTOs into domain ComponentSpecs,
// resolving+pinning each component's template (unknown → error) and returning the
// first resolved template as the app "primary". Composed invariants are validated.
// Used by the edit-composed update path; the create handler has its own inline
// copy that maps errors to per-field HTTP statuses.
//
// prev holds the app's CURRENT components keyed by name (nil at create). It is
// what keeps an edit from becoming an accidental upgrade: PATCH replaces the
// whole component list, and clients that don't echo a version back would
// otherwise have every component silently re-pinned to registry latest. The
// resolution order per component is:
//
//  1. an explicit template.version on the wire — the caller means it;
//  2. else the STORED pin, when the component already exists under the same
//     template name — an edit to anything else must not move the chart;
//  3. else the template's current version — a brand-new component, or a
//     deliberate retemplate onto a different chart, correctly lands on latest.
func (ah *appHandler) resolveComponentSpecs(ctx context.Context, dtos []ComponentCreateDTO, prev map[string]domain.ComponentSpec) ([]domain.ComponentSpec, *tpl.Template, error) {
	var specs []domain.ComponentSpec
	var primary *tpl.Template
	for i, c := range dtos {
		ct, err := domain.ParseComponentType(c.Type)
		if err != nil {
			return nil, nil, fmt.Errorf("components[%d]: %w", i, err)
		}
		mode, err := domain.ParseExposeMode(c.ExposeMode)
		if err != nil {
			return nil, nil, fmt.Errorf("components[%d]: %w", i, err)
		}
		cs := domain.ComponentSpec{
			Name:           c.Name,
			Type:           ct,
			Enabled:        c.Enabled,
			ExposeMode:     mode,
			Values:         c.Values,
			InheritAppVars: c.InheritAppVars,
			Images:         componentImagesFromDTO(c.Images),
			Stateful:       c.Stateful,
			PreviewEnabled: c.PreviewEnabled,
		}
		for _, e := range c.EnvVars {
			cs.EnvVars = append(cs.EnvVars, domain.ComponentEnvVar{
				Name:       e.Name,
				Value:      e.Value,
				FromConfig: e.FromConfig,
				FromSecret: e.FromSecret,
			})
		}
		// Carry forward the fields this DTO cannot express from the prior
		// same-name spec. req.Components replaces the list wholesale, so
		// without this a manage save silently WIPED per-component config
		// (env defaults, envFrom extras, resources, scaling) set via the
		// componentConfigs API or template defaults.
		if p, ok := prev[c.Name]; ok {
			cs.Config = p.Config
			cs.EnvFromSecrets = p.EnvFromSecrets
			cs.EnvFromConfigMaps = p.EnvFromConfigMaps
			cs.Resources = p.Resources
			cs.Scaling = p.Scaling
			cs.Replicas = p.Replicas
			cs.SizePreset = p.SizePreset
		}
		if c.Template != nil && c.Template.Name != "" {
			ctmpl, ok := ah.lookupTemplate(ctx, c.Template.Name)
			if !ok {
				return nil, nil, fmt.Errorf("components[%d]: template %q not found", i, c.Template.Name)
			}
			version := c.Template.Version
			if version == "" {
				if p, ok := prev[c.Name]; ok && p.Template != nil && p.Template.Name == ctmpl.Metadata.Name {
					version = p.Template.Version
				} else {
					version = ctmpl.Metadata.Version
				}
			}
			cs.Template = &domain.AppTemplateRef{Name: ctmpl.Metadata.Name, Version: version}
			if primary == nil {
				primary = ctmpl
			}
		}
		specs = append(specs, cs)
	}
	if len(specs) > 0 {
		if err := domain.ValidateComponents(specs); err != nil {
			return nil, nil, err
		}
	}
	return specs, primary, nil
}

func componentDTOs(components []domain.ComponentSpec, envDefaults map[string]domain.EnvironmentOverride) []ComponentSummaryDTO {
	// Invert EnvironmentDefaults[env].ComponentValues[name] into name → env →
	// overlay so each component DTO carries its own per-env overrides.
	envValsByComp := map[string]map[string]map[string]any{}
	for envName, ov := range envDefaults {
		for compName, vals := range ov.ComponentValues {
			if len(vals) == 0 {
				continue
			}
			if envValsByComp[compName] == nil {
				envValsByComp[compName] = map[string]map[string]any{}
			}
			envValsByComp[compName][envName] = vals
		}
	}

	dtos := make([]ComponentSummaryDTO, 0, len(components))
	for _, c := range components {
		dto := ComponentSummaryDTO{
			Name:             c.Name,
			Type:             string(c.Type),
			Enabled:          c.Enabled,
			ExposeMode:       string(c.ExposeMode),
			Values:           c.Values,
			EnvValues:        envValsByComp[c.Name],
			InheritAppVars:   c.InheritAppVars,
			Stateful:         c.Stateful,
			EnabledInPreview: c.EnabledInPreview(),
			PreviewEnabled:   c.PreviewEnabled,
		}
		if c.Template != nil {
			dto.Template = c.Template.Name
			dto.TemplateVersion = c.Template.Version
		}
		for _, e := range c.EnvVars {
			dto.EnvVars = append(dto.EnvVars, ComponentEnvVarDTO{
				Name:       e.Name,
				Value:      e.Value,
				FromConfig: e.FromConfig,
				FromSecret: e.FromSecret,
			})
		}
		for _, img := range c.Images {
			dto.Images = append(dto.Images, ComponentImageDTO{
				Name:              img.Name,
				Repository:        img.Repository,
				TagKey:            img.TagKey,
				TagPattern:        img.TagPattern,
				SelectionStrategy: img.SelectionStrategy,
			})
		}
		dtos = append(dtos, dto)
	}
	return dtos
}

// componentImagesFromDTO converts wire image bindings to domain, trimming empties.
func componentImagesFromDTO(dtos []ComponentImageDTO) []domain.ComponentImage {
	if len(dtos) == 0 {
		return nil
	}
	out := make([]domain.ComponentImage, 0, len(dtos))
	for _, d := range dtos {
		// TagKey is the selection identity; skip empty rows. Repository is optional
		// (discovered from values), so it no longer gates inclusion.
		if d.TagKey == "" {
			continue
		}
		out = append(out, domain.ComponentImage{
			Name:              d.Name,
			Repository:        d.Repository,
			TagKey:            d.TagKey,
			TagPattern:        d.TagPattern,
			SelectionStrategy: d.SelectionStrategy,
		})
	}
	return out
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
		BaseEnv:   env.BaseEnv,
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
