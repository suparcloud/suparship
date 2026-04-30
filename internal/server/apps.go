package server

// This file defines app-oriented API DTOs. Routes and handlers that serve
// these types are registered in rbac.go via appHandler. Legacy service-oriented
// DTOs in inventory.go, services.go, previews.go, and promote.go are retained
// for backwards compatibility; see docs/migration-app-model.md.

// --- Shared sub-DTOs ---

// AppTemplateRefDTO is the serialized form of the template an app was created
// from. Both name and version are exposed so callers can reproduce the exact
// template state without probing the templates endpoint.
type AppTemplateRefDTO struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// AppSecretRefDTO is the serialized representation of a secret reference bound
// to an app. No plaintext secret values are included; only the reference path.
type AppSecretRefDTO struct {
	Name      string `json:"name"`
	SecretRef string `json:"secretRef"`
}

// ComponentSummaryDTO describes a single runtime component within an app
// (e.g. "web", "worker", "cron"). It is a read-only view; mutations go through
// the app spec update endpoint.
type ComponentSummaryDTO struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	Enabled        bool   `json:"enabled"`
	ExposeMode     string `json:"exposeMode,omitempty"`
	PreviewEnabled bool   `json:"previewEnabled"`
}

// AppReleaseRefDTO identifies the deployed release version for one environment
// instance. All fields are optional; a nil release means nothing has been
// promoted to that environment yet.
type AppReleaseRefDTO struct {
	Image  string `json:"image,omitempty"`
	Tag    string `json:"tag,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// AppStatusSummaryDTO is a coarse runtime health summary for one environment
// instance, derived from cluster observations. Phase matches the domain
// Status* constants (e.g. "running", "degraded", "not-deployed").
type AppStatusSummaryDTO struct {
	Phase        string `json:"phase"`
	Replicas     int32  `json:"replicas"`
	Available    int32  `json:"available"`
	LastDeployed string `json:"lastDeployed,omitempty"`
}

// PreviewMetaDTO carries preview-specific metadata embedded in an environment
// summary. It is non-nil only when EnvType == "preview".
type PreviewMetaDTO struct {
	// PreviewName is the logical name of the preview (e.g. "pr-42").
	PreviewName string `json:"previewName"`
	// CreatedAt is the RFC 3339 creation timestamp of the preview.
	CreatedAt string `json:"createdAt,omitempty"`
}

// --- Environment summary ---

// AppEnvironmentSummaryDTO is the per-environment state of an app. It combines
// the desired release intent (what should be running) with the live runtime
// status observed from the cluster (what is actually running).
//
// EnvType is one of "staging", "prod", "preview". When EnvType is "preview",
// the Preview field is populated with additional metadata.
type AppEnvironmentSummaryDTO struct {
	EnvName   string               `json:"envName"`
	EnvType   string               `json:"envType"`
	Namespace string               `json:"namespace"`
	// URLs are the ingress hostnames assigned to this environment instance.
	// Always a non-nil slice; empty when no ingress has been provisioned.
	URLs      []string             `json:"urls"`
	Release   *AppReleaseRefDTO    `json:"release,omitempty"`
	Status    AppStatusSummaryDTO  `json:"status"`
	Preview   *PreviewMetaDTO      `json:"preview,omitempty"`
}

// --- App summary (list view) ---

// AppSummaryDTO is the condensed view of an app used in list responses. It
// surfaces the primary stable-environment status and URLs so the dashboard can
// render a useful row without a separate detail fetch.
//
// Status reflects the first stable environment (staging or prod); URLs are
// the union of stable-environment ingress URLs. Components are included so
// callers can distinguish single-component apps from multi-component apps
// without a detail fetch.
type AppSummaryDTO struct {
	Name        string               `json:"name"`
	Project     string               `json:"project"`
	DisplayName string               `json:"displayName,omitempty"`
	Description string               `json:"description,omitempty"`
	Template    AppTemplateRefDTO    `json:"template"`
	// Status is the runtime summary for the primary stable environment.
	Status      AppStatusSummaryDTO  `json:"status"`
	// URLs are the primary ingress hostnames (stable environments only).
	URLs        []string             `json:"urls"`
	Components  []ComponentSummaryDTO `json:"components"`
}

// --- App detail (single-app view) ---

// AppDetailDTO is the full view of an app, including all environment instances,
// component topology, template reference, curated values, and secret refs.
// Secret values are never included; only reference strings are returned.
type AppDetailDTO struct {
	Name        string                     `json:"name"`
	Project     string                     `json:"project"`
	DisplayName string                     `json:"displayName,omitempty"`
	Description string                     `json:"description,omitempty"`
	Template    AppTemplateRefDTO          `json:"template"`
	// Values holds the curated (non-secret) template input values.
	Values      map[string]any             `json:"values"`
	SecretRefs  []AppSecretRefDTO          `json:"secretRefs"`
	Components  []ComponentSummaryDTO      `json:"components"`
	// Environments includes stable (staging, prod) and preview instances.
	Environments []AppEnvironmentSummaryDTO `json:"environments"`
}

// --- Response wrappers ---

// AppListResponse is the JSON body for GET /api/v1/projects/{project}/apps.
type AppListResponse struct {
	Project string          `json:"project"`
	Apps    []AppSummaryDTO `json:"apps"`
}

// AppDetailResponse is the JSON body for GET /api/v1/projects/{project}/apps/{app}.
type AppDetailResponse struct {
	App AppDetailDTO `json:"app"`
}

// AppEnvironmentsResponse is the JSON body for
// GET /api/v1/projects/{project}/apps/{app}/environments.
type AppEnvironmentsResponse struct {
	Project      string                     `json:"project"`
	AppName      string                     `json:"appName"`
	Environments []AppEnvironmentSummaryDTO `json:"environments"`
}

// AppEnvironmentResponse is the JSON body for
// GET /api/v1/projects/{project}/apps/{app}/environments/{env}.
type AppEnvironmentResponse struct {
	Environment AppEnvironmentSummaryDTO `json:"environment"`
}

// --- App creation request / response ---

// ComponentCreateDTO allows callers to explicitly define a component when
// creating an app. When the Components list is omitted from the request body,
// the handler derives a default component from the template category.
// ComponentCreateDTO represents a component as specified in the create-app
// request body. When absent from the request, the handler derives a default
// component from the template category.
type ComponentCreateDTO struct {
	// Name must be a valid DNS label (lowercase alphanumeric and hyphens).
	Name string `json:"name"`
	// Type must be one of "web", "worker", or "cron".
	Type string `json:"type"`
	// Enabled controls whether this component is active. Defaults to true.
	Enabled bool `json:"enabled"`
	// ExposeMode selects which routing profile (disabled/internal/external)
	// the chart should use for this component. Empty is treated as
	// "disabled" — the component runs without any ingress.
	ExposeMode string `json:"exposeMode,omitempty"`
	// PreviewEnabled controls whether this component is deployed in preview
	// environments. Defaults to true for web components, false for others.
	PreviewEnabled bool `json:"previewEnabled"`
}

// createAppRequest is the JSON body for POST /api/v1/projects/{project}/apps.
// Template is required; all other fields are optional unless the referenced
// template marks specific inputs as required.
type createAppRequest struct {
	// Name is the unique identifier for the app within the project.
	// Must be a valid DNS label (lowercase alphanumeric and hyphens, 2–63 chars).
	Name string `json:"name"`
	// DisplayName is a human-friendly label shown in the UI.
	DisplayName string `json:"displayName,omitempty"`
	// Description provides optional context about the app's purpose.
	Description string `json:"description,omitempty"`
	// Template is the name of the golden-path template to create this app from.
	Template string `json:"template"`
	// Values holds curated template input values (no secrets).
	Values map[string]any `json:"values"`
	// SecretRefs maps secret input names to Kubernetes Secret references.
	// Values are resolved at runtime; no plaintext secrets are accepted.
	SecretRefs []AppSecretRefDTO `json:"secretRefs"`
	// ComponentToggles overrides the enabled state of optional (non-required)
	// components declared in the template. Keys are component names; values
	// are the desired enabled state. Required components ignore this field.
	// When absent, the template's defaultEnabled value is used for each
	// component. Has no effect when Components is also supplied.
	ComponentToggles map[string]bool `json:"componentToggles,omitempty"`
	// Components is optional and bypasses the template-derived component
	// initialisation entirely when provided. Prefer ComponentToggles for
	// templates that declare a spec.components section. When absent, the
	// handler initialises components from the template via ComponentToggles.
	Components []ComponentCreateDTO `json:"components,omitempty"`
	// NamespaceScope controls whether this app deploys into a dedicated
	// namespace ("app", default) or the shared project namespace ("project").
	NamespaceScope string `json:"namespaceScope,omitempty"`
	// NamespacePattern overrides org/project defaults. Only applies when
	// NamespaceScope is "app". Tokens: {org}, {project}, {app}, {env}.
	NamespacePattern string `json:"namespacePattern,omitempty"`
}

// createAppResponse is the JSON body returned on a successful app creation.
// The full app detail (including default environments) is included so the
// caller does not need a subsequent GET.
type createAppResponse struct {
	App AppDetailDTO `json:"app"`
}

// --- App-scoped preview DTOs ---

// AppPreviewSummaryDTO is the app-oriented view of a single preview environment.
// Unlike the service-scoped PreviewDTO, this type references the owning app by
// name rather than the internal service name, and embeds a full status summary.
type AppPreviewSummaryDTO struct {
	Name      string              `json:"name"`
	AppName   string              `json:"appName"`
	Project   string              `json:"project"`
	Namespace string              `json:"namespace"`
	Status    AppStatusSummaryDTO `json:"status"`
	// URLs are the ingress hostnames assigned to this preview instance.
	// Always a non-nil slice; empty when no ingress has been provisioned.
	URLs      []string          `json:"urls"`
	// Release is the deployed release for this preview, when available.
	Release   *AppReleaseRefDTO `json:"release,omitempty"`
	CreatedAt string            `json:"createdAt,omitempty"`
}

// AppPreviewsResponse is the JSON body for app-scoped preview list responses
// (GET /api/v1/projects/{project}/apps/{app}/previews).
type AppPreviewsResponse struct {
	Project  string                 `json:"project"`
	AppName  string                 `json:"appName"`
	Previews []AppPreviewSummaryDTO `json:"previews"`
}

// CreateAppPreviewRequest is the JSON body for
// POST /api/v1/projects/{project}/apps/{app}/previews.
//
// Name accepts a raw identifier such as a Git branch name ("feature/my-branch")
// or PR ref ("PR-42"). It is sanitized deterministically via
// domain.SanitizePreviewName before validation and storage.
type CreateAppPreviewRequest struct {
	Name string `json:"name"`
}

// --- App-scoped promotion DTOs ---

// AppPromoteRequest is the JSON body for
// POST /api/v1/projects/{project}/apps/{app}/promote.
// TargetEnvironment must be the logical name of an environment that exists in
// the project and is higher in the promotion order than the current one.
type AppPromoteRequest struct {
	TargetEnvironment string `json:"targetEnvironment"`
}

// KargoPromotionDTO describes a Kargo Promotion CR created by the promote endpoint.
// It is populated only when the server is configured with a KargoPromoter;
// otherwise Release is populated instead (in-store fallback).
type KargoPromotionDTO struct {
	// Name is the Kargo Promotion CR name (deterministically generated).
	Name string `json:"name"`
	// Stage is the Kargo Stage (= target environment) being promoted to.
	Stage string `json:"stage"`
	// Freight is the Kargo Freight name being promoted.
	Freight string `json:"freight"`
	// Phase is the initial observed phase ("Pending", "Running", etc.).
	Phase string `json:"phase,omitempty"`
}

// AppPromoteResponse is the JSON body returned on a successful app promotion.
// When Kargo is configured, KargoPromotion is populated and Release is nil.
// Without Kargo, Release is populated with the in-store release copy result.
type AppPromoteResponse struct {
	Project     string             `json:"project"`
	App         string             `json:"app"`
	Source      string             `json:"source"`
	Destination string             `json:"destination"`
	Namespace   string             `json:"namespace"`
	Message     string             `json:"message"`
	// Release is the release bundle copied in the store (no-Kargo fallback).
	Release        *AppReleaseRefDTO  `json:"release,omitempty"`
	// KargoPromotion is populated when a Kargo Promotion CR was created.
	KargoPromotion *KargoPromotionDTO `json:"kargoPromotion,omitempty"`
}

// KargoPromotionStatusResponse is the JSON body for
// GET /api/v1/projects/{project}/apps/{app}/promotions/{name}.
// It returns the current observed phase of a Kargo Promotion CR, enabling the
// UI to poll for live status updates without subscribing to server-sent events.
type KargoPromotionStatusResponse struct {
	// Name is the Kargo Promotion CR name.
	Name string `json:"name"`
	// Stage is the target Kargo Stage (= target environment).
	Stage string `json:"stage"`
	// Freight is the Freight name being promoted.
	Freight string `json:"freight"`
	// Phase is the current observed phase: "Pending", "Running", "Succeeded", "Failed".
	Phase string `json:"phase"`
}

// KargoStageStatusDTO is the per-stage view returned by the pipeline endpoint.
type KargoStageStatusDTO struct {
	// StageName is the full Kargo Stage name, e.g. "color-app-staging".
	StageName string `json:"stageName"`
	// EnvName is the suparship environment name (e.g. "staging").
	EnvName string `json:"envName"`
	// Phase is "Steady", "Promoting", or "NotReady".
	Phase string `json:"phase"`
	// Health is "Healthy", "Unhealthy", or "Unknown".
	Health string `json:"health"`
	// CurrentFreight is the abbreviated freight name currently running.
	CurrentFreight string `json:"currentFreight,omitempty"`
	// AvailableFreightCount is the number of new freights waiting to be
	// promoted into this stage. A non-zero value means a new image/commit
	// is available but has not yet been promoted.
	AvailableFreightCount int `json:"availableFreightCount"`
}

// KargoAppPipelineResponse is the JSON body for
// GET /api/v1/projects/{project}/apps/{app}/kargo/stages.
type KargoAppPipelineResponse struct {
	// Stages is ordered staging → prod (matches the promotion chain).
	Stages []KargoStageStatusDTO `json:"stages"`
}

// --- Deployment history DTOs ---

// AppDeploymentHistoryEntryDTO represents a single ArgoCD sync event in the
// deployment history of one app environment.
type AppDeploymentHistoryEntryDTO struct {
	// ID is the ArgoCD-assigned sequence number; higher = more recent.
	ID int64 `json:"id"`
	// Revision is the Git commit SHA that was synced.
	Revision string `json:"revision,omitempty"`
	// DeployedAt is the RFC 3339 timestamp when the sync completed.
	DeployedAt string `json:"deployedAt,omitempty"`
	// DeployStartedAt is the RFC 3339 timestamp when the sync began (may be empty).
	DeployStartedAt string `json:"deployStartedAt,omitempty"`
	// RepoURL is the source Git repository URL.
	RepoURL string `json:"repoURL,omitempty"`
	// Path is the path within the repository that was synced.
	Path string `json:"path,omitempty"`
	// TargetRevision is the Git ref (branch/tag/commit) tracked by the Application.
	TargetRevision string `json:"targetRevision,omitempty"`
}

// AppDeploymentHistoryResponse is the JSON body for
// GET /api/v1/projects/{project}/apps/{app}/environments/{env}/history.
type AppDeploymentHistoryResponse struct {
	Project     string                         `json:"project"`
	App         string                         `json:"app"`
	Environment string                         `json:"environment"`
	// History is in reverse-chronological order (most recent first).
	// Empty slice when no syncs have occurred yet.
	History     []AppDeploymentHistoryEntryDTO `json:"history"`
}
