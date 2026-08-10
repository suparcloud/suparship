export interface AuthUser {
  username: string;
  role: string;
}

export interface MetaInfo {
  app: string;
  version: string;
  commit: string;
  buildDate: string;
}

export interface Project {
  name: string;
  displayName?: string;
  description?: string;
}

export interface ProjectDetail {
  name: string;
  displayName?: string;
  description?: string;
  environments: EnvironmentInfo[];
  services: string[];
}

export interface Service {
  name: string;
  project: string;
}

export interface PreviewEnvironment {
  name: string;
  project: string;
  service: string;
  namespace: string;
  status: string;
  url?: string;
  createdAt: string;
  /** Source branch or PR reference, e.g. "main" or "pr-42". Optional until API supports it. */
  branch?: string;
}

export interface PreviewsResponse {
  previews: PreviewEnvironment[];
}

export interface CreatePreviewRequest {
  name: string;
  project: string;
  service: string;
  /** Optional source branch or PR reference to associate with the preview. */
  branch?: string;
}

/** Body for the app-scoped preview create endpoint. */
export interface CreateAppPreviewRequest {
  name: string;
  /**
   * Stable env the preview clones (cluster + per-env config/secrets). Omit to
   * default to the app's first stable env by order (conventionally "staging").
   */
  baseEnv?: string;
  /**
   * Image tag to deploy for this preview (every image the app maps). Omit to
   * inherit the base env's image. Re-creating with a new tag re-publishes the
   * preview (upsert) — CI typically passes the tag it built for the PR.
   */
  imageTag?: string;
}

/** Response from the app-scoped preview create/list endpoints. */
export interface AppPreviewSummary {
  name: string;
  appName: string;
  project: string;
  namespace: string;
  /** Stable env this preview clones (e.g. "staging"). Empty for older previews. */
  baseEnv?: string;
  urls: string[];
  status: AppStatusSummary;
}

/** PreviewGroup is a PR-level preview: all app-previews sharing a preview name
 *  within a project, grouped into one item. */
export interface PreviewGroup {
  name: string; // PR/preview name, e.g. "pr-712"
  project: string;
  baseEnv?: string;
  /** Aggregate phase across the group's app-previews. */
  health: string;
  apps: AppPreviewSummary[];
}

export interface PreviewGroupsResponse {
  previews: PreviewGroup[];
}

export interface OrgInfo {
  name: string;
  displayName: string;
  createdAt?: string;
}

export interface TeamInfo {
  name: string;
  displayName: string;
  members: string[];
}

export interface TeamsResponse {
  teams: TeamInfo[];
}

export interface ProjectsResponse {
  projects: Project[];
}

export interface RoleBinding {
  project: string;
  // Exactly one of team or group is set.
  team?: string;
  group?: string;
  role: string;
}

export interface ProjectRBACResponse {
  project: string;
  roleBindings: RoleBinding[];
}

export interface RoleBindingsResponse {
  roleBindings: RoleBinding[];
}

// OIDCConfig mirrors the server's OIDC config DTO. The client secret value is
// never returned — clientSecretSet reports whether one is stored.
export interface OIDCConfig {
  enabled: boolean;
  issuerURL: string;
  clientID: string;
  redirectURL: string;
  scopes: string[];
  usernameClaim: string;
  groupsClaim: string;
  clientSecretSet: boolean;
}

export interface AuthConfigResponse {
  oidc: OIDCConfig;
}

// UpdateOIDCRequest is the PUT body for /org/auth. clientSecret is write-only;
// omit it to keep the stored value.
export interface UpdateOIDCRequest {
  enabled: boolean;
  issuerURL: string;
  clientID: string;
  clientSecret?: string;
  redirectURL: string;
  scopes?: string[];
  usernameClaim?: string;
  groupsClaim?: string;
}

export interface TemplateSummary {
  name: string;
  version: string;
  title: string;
  description?: string;
  category: string;
  engine: string;
  /** Retired by an org admin: still listed for management, but the create
   *  flow must not offer it (the server also refuses with a 422). */
  disabled?: boolean;
}

export interface TemplatesResponse {
  templates: TemplateSummary[];
}

// Returned by POST /templates/import/preview — drives the review/edit UI.
export interface TemplateImportSummary {
  chartName: string;
  chartVersion: string;
  appVersion?: string;
  description?: string;
  hasSchema: boolean;
  inputCount: number;
  mappingCount: number;
  imageCount: number;
  archiveSize: number;
}

// TemplateImage maps one of a chart's services to its image source + the Helm
// values key holding its tag, driving external-CD (Kargo) wiring.
export interface TemplateImage {
  name: string;
  repository: string;
  tagKey: string;
  tagPattern?: string;
  selectionStrategy?: string;
  /** owning composed component (set only for a composed app's per-component
   *  discovered images); routes the selection to that component's values. */
  component?: string;
  /** the template declares a pull rule for this image (its tagPattern/
   *  selectionStrategy are the inherited rule); watched by default in the editor. */
  declared?: boolean;
}

// AppImageBinding marks one discovered chart image (identified by its tagKey) as
// managed by external CD (Kargo). No repository — it's read from the Helm values
// at publish, so it never goes stale.
export interface AppImageBinding {
  name: string;
  tagKey: string;
  tagPattern?: string;
  selectionStrategy?: string;
}

export interface TemplateImportChartFile {
  path: string;
  size: number;
}

export interface TemplateImportPreview {
  templateYAML: string;
  summary: TemplateImportSummary;
  chartFiles: TemplateImportChartFile[];
}

export interface TemplateImportResult {
  name: string;
  version: string;
}

// External template repos and registry — surfaced by GET /templates/registry.
// The shape mirrors internal/tpl/registry.go (TemplateRegistry).
export interface ExternalTemplateRepo {
  name: string;
  // type selects the fetcher. Empty defaults to "git" on the backend
  // for back-compat with sources persisted before the field was added.
  type?: TemplateSourceType | "";
  repoURL: string;
  // ref/path are git-only fields. Ignored for non-git source types.
  ref: string;
  path: string;
  // chart/version are required for non-git source types (oci/chartmuseum).
  // Ignored for git (which discovers all templates under path).
  chart?: string;
  version?: string;
  // Provider drives the credentials data-key shape: token for github/
  // gitlab/gitea, username+password for bitbucket/generic. Empty defaults
  // to generic. Only meaningful for git-type sources.
  provider?: TemplateRepoProvider | "";
  // existingSecret is a K8s Secret name in suparship-system. UI-managed
  // credentials write to a deterministic name (suparship-tpl-credentials-
  // <source>); operators may also point at a hand-managed Secret.
  existingSecret?: string;
}

// TemplateSourceType matches internal/tpl/registry.go SourceType*
// constants. "git", "gitcharts", and "oci" are wired in the backend today;
// "chartmuseum" / "gittgz" are reserved for future fetchers and
// surface as ErrUnsupportedSourceType when an operator picks them.
export type TemplateSourceType =
  | "git"
  | "gitcharts"
  | "oci"
  | "chartmuseum"
  | "gittgz";

export type TemplateRepoProvider =
  | "github"
  | "gitlab"
  | "gitea"
  | "bitbucket"
  | "generic";

export interface TemplateSource {
  name: string;
  origin: "builtin" | "external";
  version?: string;
  externalRepo?: string;
  externalRef?: string;
  externalPath?: string;
  syncedAt?: string;
}

export interface TemplateRegistry {
  builtIn: string[];
  external?: ExternalTemplateRepo[];
  sources: TemplateSource[];
}

export interface TemplateRegistryResponse {
  configured: boolean;
  registry: TemplateRegistry;
}

// Per-source sync outcome returned from POST /templates/registry/sync and
// .../sources/{name}/sync. Always 200; UI inspects each entry for partial
// failure surfacing.
export interface TemplateSyncResult {
  sourceName: string;
  templates: string[];
  syncedAt: string;
  error?: string;
}

export interface TemplateSyncResponse {
  results: TemplateSyncResult[];
}

// Per-version archive entry for a template, returned by
// GET /api/v1/templates/{name}/versions. Sorted descending by SemVer
// on the wire when the version strings parse cleanly.
export interface TemplateVersionInfo {
  version: string;
  createdAt?: string;
}

export interface TemplateVersionsResponse {
  versions: TemplateVersionInfo[];
}

// Credentials sealing flow for a single source.
// Provider may be omitted to fall back to the source's stored value;
// when set, it overrides (useful for correcting a misclassified entry).
export interface TemplateCredentialsRequest {
  provider?: TemplateRepoProvider | "";
  token?: string;
  username?: string;
  password?: string;
}

export interface TemplateTestConnectionResult {
  success: boolean;
  message: string;
  durationMs: number;
}

export interface TemplateCredentialsResponse {
  source: ExternalTemplateRepo;
  sealedSecretName: string;
  testResult?: TemplateTestConnectionResult;
}

export interface TemplateInput {
  name: string;
  title: string;
  type: "string" | "number" | "boolean" | "enum";
  description?: string;
  required: boolean;
  default?: string | number | boolean;
  options: string[];
  min?: number;
  max?: number;
  pattern?: string;
}

// ValueField is one entry of a template's developer-facing values projection: the
// dotted Helm values path the developer owns, plus presentation metadata. Today it
// drives the commented YAML the editor is seeded with; the same declaration is what
// a future form renderer will read, which is why it carries type/options/bounds.
export interface ValueField {
  path: string;
  title?: string;
  type?: "string" | "number" | "boolean" | "enum";
  description?: string;
  required?: boolean;
  default?: unknown;
  options?: string[];
  min?: number;
  max?: number;
  pattern?: string;
}

export interface TemplateSecretInput {
  name: string;
  title: string;
  description?: string;
  secretRef: string;
}

export interface TemplatePreset {
  name: string;
  title: string;
  description?: string;
  values: Record<string, unknown>;
}

export interface TemplateComponentInfo {
  name: string;
  type: string;
}

export interface TemplateDetail {
  name: string;
  version: string;
  title: string;
  description?: string;
  category: string;
  engine: string;
  inputs: TemplateInput[];
  advancedInputs: TemplateInput[];
  secretInputs: TemplateSecretInput[];
  presets: TemplatePreset[];
  components?: TemplateComponentInfo[];
  /** Retired: refuses new apps; existing apps keep working. Toggled via
   *  PATCH {disabled} by org admins. */
  disabled?: boolean;
  // Platform-Engineer-authored Helm values overlays (all-envs + per-env),
  // layered above chart defaults and below developer overrides.
  defaultValues?: Record<string, unknown>;
  envValues?: Record<string, Record<string, unknown>>;
  // Values mode: undefined/true = canonical suparship-common base; false =
  // passthrough/BYO.
  injectCanonicalValues?: boolean;
  // Per-service image mapping for external-CD (Kargo) wiring.
  images?: TemplateImage[];
  // The developer-facing values projection (org override applied). Empty/absent =
  // no projection; editors seed from the full concise platform base.
  developerValues?: ValueField[];
  // Default app delivery mode: "pipeline" (Kargo + promotion) or "direct"
  // (deploy each env from values, no Kargo). "" means pipeline.
  deliveryMode?: string;
  // editable = metadata can be edited in place (imported/BYO + cluster-stored).
  editable?: boolean;
  source?: TemplateProvenance;
}

// TemplateProvenance describes where a template came from, for edit gating.
export interface TemplateProvenance {
  origin: "builtin" | "imported" | "synced";
  externalRepo?: string;
  syncedAt?: string;
}

// TemplateMetadataPatch is the partial body for PATCH /templates/{name}.
export interface TemplateMetadataPatch {
  title?: string;
  category?: string;
  description?: string;
  injectCanonicalValues?: boolean;
  // images, when present, replaces the per-service image mapping (editable
  // templates only). Send [] to clear.
  images?: TemplateImage[];
  // deliveryMode sets the template's default app delivery mode ("pipeline" or
  // "direct"). Editable (imported) templates only; "" reverts to pipeline.
  deliveryMode?: string;
  // disabled retires (true) / restores (false) the template. Works for every
  // provenance, including built-ins — this is how a shipped template is
  // "removed" without deleting files. Existing apps keep working.
  disabled?: boolean;
}

// TemplateOverride is the org-level platform values overlay a PE/SRE authors for
// a template (all-envs default + per-env), layered above the template's own
// values and below developer overrides at publish. Stored separately from the
// template so external sync can't clobber it.
export interface TemplateOverride {
  defaultValues?: Record<string, unknown>;
  envValues?: Record<string, Record<string, unknown>>;
  // Per-cluster overlays keyed by cluster ref (env-agnostic), for cloud-intrinsic
  // structured annotations. Applied to every env that deploys to the cluster.
  clusterValues?: Record<string, Record<string, unknown>>;
  // Default overlay applied to every preview of this template's apps, below the
  // app's own preview override.
  previewDefaultValues?: Record<string, unknown>;
}

// EffectiveValuesResponse is the read-only "what will deploy" preview backing
// the values editor: the merged values document (chart ⊕ platform/env ⊕
// overrides), NOT the fully rendered chart.
export interface EffectiveValuesResponse {
  values: Record<string, unknown>;
  // false when the chart bundle couldn't be read (built-in/disk/external-mode);
  // the preview then reflects only platform/env defaults + overrides.
  chartDefaultsAvailable: boolean;
  // whether ((platform.*))/((vars.*)) tokens were resolved (always false in v1).
  interpolated: boolean;
  // overlays that contributed, low→high.
  layers: string[];
  // images discovered in the effective values (every image block with a
  // repository), each with its dotted tag key. The CD UI lists these so the user
  // can select which Kargo manages. Omitted/empty when none are found.
  discoveredImages?: TemplateImage[];
  // The template's developer-facing values projection (org override applied).
  // Rides this response so the seeding call sites need no extra round trip.
  // Omitted/empty when the template declares none.
  developerValues?: ValueField[];
}

// --- Onboarding types ---

export interface SetupGate {
  key: string;
  title: string;
  status: "ok" | "incomplete" | "error";
  message?: string;
  action?: string;
}

export interface OnboardingStatus {
  clusterConnected: boolean;
  authConfigured: boolean;
  orgExists: boolean;
  hasProjects: boolean;
  hasEnvironments: boolean;
  hasServices: boolean;
  complete: boolean;
  gates?: SetupGate[];
  platformReady?: boolean;
}

// --- Inventory / Runtime types ---

export interface EnvironmentInfo {
  name: string;
  displayName?: string;
  project?: string;  // empty for org-level environments
  namespace?: string;
  order: number;
  clusterRefs?: string[];
  activeClusterRef?: string;
  baseDomain?: string;
}

export interface EnvironmentsResponse {
  environments: EnvironmentInfo[];
}

export interface RuntimeInfo {
  status: string;
  image?: string;
  replicas: number;
  available: number;
  ingressUrls: string[];
  namespace: string;
  lastDeployed?: string;
}

export interface ServiceRuntime {
  name: string;
  template: { name: string; version?: string };
  runtime: RuntimeInfo;
}

export interface ProjectServicesResponse {
  project: string;
  services: ServiceRuntime[];
}

export interface ServiceEnv {
  environment: string;
  namespace: string;
  runtime: RuntimeInfo;
}

export interface ServiceDetailInfo {
  name: string;
  project: string;
  template: { name: string; version?: string };
  values: Record<string, unknown>;
  secretRefs: SecretRefInput[];
  environments: ServiceEnv[];
}

// --- Service creation types ---

export interface CreateServiceRequest {
  name: string;
  template: string;
  values: Record<string, unknown>;
  secretRefs: SecretRefInput[];
}

export interface SecretRefInput {
  name: string;
  secretRef: string;
}

// --- App-oriented types ---
// These mirror the backend app DTOs (internal/server/apps.go) and are additive;
// the service-oriented types above are unchanged.

export interface AppTemplateRef {
  name: string;
  version?: string;
}

export interface AppSecretRef {
  name: string;
  secretRef: string;
}

export interface ComponentSummary {
  name: string;
  type: "web" | "worker" | "cron" | "job";
  enabledInPreview: boolean;
  exposeMode?: string;
  /** The component's own template. */
  template?: string;
  /** The chart version this component is pinned to (what actually deploys). */
  templateVersion?: string;
  /** Newest archived version of THIS component's template. Empty = the template
   *  isn't version-managed, so no upgrade affordance should be shown. */
  latestVersion?: string;
  /** Whether latestVersion is newer than templateVersion. */
  upgradeAvailable?: boolean;
  /** The component's base Helm values overlay (all environments), editable. */
  values?: Record<string, unknown>;
  /** Per-environment overlay overrides keyed by env name (deep-merged over
   *  values for that env). Editable per (component, env). */
  envValues?: Record<string, Record<string, unknown>>;
  /** Env policy: inherit all app vars (default) or a curated subset. */
  inheritAppVars?: boolean;
  envVars?: ComponentEnvVar[];
  /** Kargo image bindings (repo + tag-key). */
  images?: ComponentImage[];
  /** Stateful (a database/cache): its own prune-disabled Application. */
  stateful?: boolean;
}

export interface AppReleaseRef {
  image?: string;
  tag?: string;
  commit?: string;
}

export interface Diagnostic {
  source: string;
  level: "error" | "warning";
  title: string;
  detail?: string;
  hint?: string;
  /** Destination cluster this came from, set when the env fans out to >1 cluster. */
  cluster?: string;
}

export interface AppStatusSummary {
  phase: string;
  replicas: number;
  available: number;
  lastDeployed?: string;
  diagnostics?: Diagnostic[];
  /** per-component live health of a composed app (empty for single-component) */
  components?: ComponentRuntimeStatus[];
}

export interface ComponentRuntimeStatus {
  component: string;
  phase: string;
  replicas: number;
  available: number;
}

export interface PreviewMeta {
  previewName: string;
  /** Stable env this preview clones (e.g. "staging"). Empty for older previews. */
  baseEnv?: string;
  createdAt?: string;
}

export interface AppEnvironmentSummary {
  envName: string;
  envType: "staging" | "prod" | "preview";
  /** Position in the promotion pipeline (lower = earlier). The first stable env
   *  is the default preview base env. Preview envs have order 0. */
  order: number;
  namespace: string;
  urls: string[];
  release?: AppReleaseRef;
  status: AppStatusSummary;
  preview?: PreviewMeta;
  /** For direct-delivery apps: whether suparship deploys to this env (effective
   *  opt-in/out). Always true for pipeline apps. */
  deploy: boolean;
  /** Marks the base (lowest-order stable) env, which deploys by default. */
  isBase: boolean;
  /** When set, this stable env is pinned to a specific image (e.g. a PR preview
   *  promoted without merging): CD is paused until unpinned. */
  pinnedTag?: string;
  pinnedFrom?: string;
  /** When true, this env's workload is scaled down via the suspend op (the env
   *  stays published; resume brings it back). */
  suspended?: boolean;
}

export interface AppSummary {
  name: string;
  project: string;
  displayName?: string;
  description?: string;
  template: AppTemplateRef;
  status: AppStatusSummary;
  urls: string[];
  /** Per-environment status in promotion order (stable envs by order, previews
   *  last). Lets list views show per-env status without a per-app request. */
  environments?: AppEnvironmentSummary[];
  components: ComponentSummary[];
}

export interface AppDetail {
  name: string;
  project: string;
  displayName?: string;
  description?: string;
  template: AppTemplateRef;
  values: Record<string, unknown>;
  secretRefs: AppSecretRef[];
  components: ComponentSummary[];
  environments: AppEnvironmentSummary[];
  // Per-(env, cluster) value overrides keyed env → cluster (fan-out envs only).
  clusterOverrides?: Record<string, Record<string, ClusterValueOverrideDTO>>;
  // Per-env target clusters, keyed env name → cluster names. Value ["*"] = ALL
  // of that env's clusters (dynamic); explicit list = that subset; an omitted
  // env (or empty array) = inherit the env default (active cluster).
  targetClusters?: Record<string, string[]>;
  // App-level freeform Helm values overlay.
  rawValues?: Record<string, unknown>;
  // Per-environment freeform overlays keyed by env name.
  envRawValues?: Record<string, Record<string, unknown>>;
  // App-level per-component config keyed by component name.
  componentConfigs?: Record<string, ComponentConfig>;
  // Per-(env, component) overrides keyed env → component.
  envComponents?: Record<string, Record<string, ComponentConfig>>;
  // Continuous-delivery settings (external-CD tag ownership). Always present.
  cd: CDConfig;
  // Per-slot image repository bindings (keyed by template slot name). Omitted
  // when the app has none (legacy single-image flow).
  images?: AppImageBinding[];
  // "pipeline" (Kargo + promotion) or "direct" (deploy each env from values, no
  // Kargo). "" is treated as pipeline.
  deliveryMode?: string;
  // Whether this app supports preview (ephemeral PR) environments. Default true.
  previewsEnabled: boolean;
  // Newest archived version of the app's PRIMARY template. Empty when that
  // template isn't version-managed (a built-in with no archives).
  templateLatestVersion?: string;
  // How many components have a newer template version available. The upgrade
  // affordance keys off this so it works for composed apps too.
  upgradesAvailable?: number;
  // Archived versions of every template this app's components use, keyed by
  // template name, newest first — the source for the upgrade picker.
  templateVersions?: Record<string, TemplateVersionInfo[]>;
}

// CDConfig configures who owns the deployed image tag. When managed is true an
// external CD controller (Kargo) writes the tag and the platform preserves it
// across republishes instead of overwriting it with the create-time seed. The
// tag-keys Kargo manages are declared by the template's image slots; the
// repository each slot watches is bound per-app via AppImageBinding.
export interface CDConfig {
  managed?: boolean;
  /** Auto-promote this pipeline app to prod once staging is healthy. */
  autoPromote?: boolean;
  /**
   * The user has saved a CD image selection at least once (server-set, read-only
   * here). When true, an empty selection means "watch nothing" — disabling CD for
   * a template-declared image persists instead of reverting to the template default.
   */
  imagesConfigured?: boolean;
}

// ComponentResources holds raw k8s resource quantities (cpu/memory/…).
export interface ComponentResources {
  requests?: Record<string, string>;
  limits?: Record<string, string>;
}

// KEDATrigger mirrors a KEDA ScaledObject trigger.
export interface KEDATrigger {
  type: string;
  metricType?: string;
  metadata?: Record<string, string>;
}

export interface ComponentScaling {
  triggers?: KEDATrigger[];
  minReplicas?: number;
  maxReplicas?: number;
}

// ComponentConfig is the per-component knob set (resources / envFrom / scaling /
// env) editable at app level and per environment.
export interface ComponentConfig {
  resources?: ComponentResources;
  envFromSecrets?: string[];
  envFromConfigMaps?: string[];
  scaling?: ComponentScaling;
  env?: Record<string, string>;
}

// ClusterValueOverrideDTO mirrors the backend per-(env, cluster) override.
export interface ClusterValueOverrideDTO {
  replicas?: number;
  sizePreset?: string;
  values?: Record<string, unknown>;
  config?: Record<string, string>;
}

export interface AppListResponse {
  project: string;
  apps: AppSummary[];
}

export interface AppDetailResponse {
  app: AppDetail;
}

export interface AppEnvironmentsResponse {
  project: string;
  appName: string;
  environments: AppEnvironmentSummary[];
}

export interface AppEnvironmentResponse {
  environment: AppEnvironmentSummary;
}

// --- Promotion types ---

export interface PromoteRequest {
  targetEnvironment: string;
}

/**
 * Describes a Kargo Promotion CR created by the promote endpoint.
 * Populated only when the server is configured with a KargoPromoter.
 */
export interface KargoPromotion {
  /** Kargo Promotion CR name, e.g. "hello-prod-1712774400" */
  name: string;
  /** Target Kargo Stage name (= target environment name) */
  stage: string;
  /** Kargo Freight being promoted */
  freight: string;
  /** Initial observed phase: "Pending" | "Running" | "Succeeded" | "Failed" */
  phase?: string;
}

/**
 * Live status response for a Kargo Promotion CR.
 * Returned by GET /api/v1/projects/:project/apps/:app/promotions/:name.
 */
export interface KargoPromotionStatus {
  /** False when no Kargo status reader is wired; other fields are then empty. */
  available?: boolean;
  name: string;
  stage: string;
  freight: string;
  /** Current observed phase: "Pending" | "Running" | "Succeeded" | "Failed" */
  phase: string;
}

/** One stage's live status in the Kargo pipeline. */
export interface KargoStageStatus {
  stageName: string;
  envName: string;
  phase: string;
  health: string;
  currentFreight?: string;
  availableFreightCount: number;
}

/** Response from GET .../kargo/stages */
export interface KargoAppPipeline {
  /** False when no Kargo pipeline reader is wired; stages is then empty. */
  available?: boolean;
  stages: KargoStageStatus[];
}

export interface PromoteResponse {
  project: string;
  /** App name (new field). Falls back to `service` for legacy responses. */
  app?: string;
  /** @deprecated Use `app` instead. Retained for legacy service-promote responses. */
  service?: string;
  source: string;
  destination: string;
  namespace: string;
  message: string;
  /** How the promotion ran: "kargo" (pipeline) or "in-store" (direct copy, no Kargo). */
  mechanism?: "kargo" | "in-store";
  /** Populated when Kargo is configured — describes the created Promotion CR. */
  kargoPromotion?: KargoPromotion;
  /** Populated when Kargo is NOT configured — in-store release copy result. */
  release?: AppReleaseRef;
}

// --- Logs types ---

export interface LogsResponse {
  project: string;
  service: string;
  pod: string;
  container: string;
  logs: string;
}

export interface AppLogsResponse {
  project: string;
  app: string;
  environment: string;
  namespace: string;
  component?: string;
  pod: string;
  container: string;
  logs: string;
}

export interface CreateServiceResponse {
  service: {
    name: string;
    template: { name: string; version?: string };
    values: Record<string, unknown>;
    secretRefs: SecretRefInput[];
  };
  helmValues: Record<string, unknown>;
}

// --- App creation types ---

// ComponentCreate declares one component of a COMPOSED app in the create
// request. Each component carries its own template (so the app assembles from
// multiple templates into one multi-source Application) plus per-component typed
// config. Mirrors the backend ComponentCreateDTO.
// ComponentEnvVar is one curated env var: a literal value, or a selected/renamed
// key of the app config (fromConfig) / app secret (fromSecret).
export interface ComponentEnvVar {
  name: string;
  value?: string;
  fromConfig?: string;
  fromSecret?: string;
}

export interface ComponentCreate {
  name: string;
  /** web | worker | cron | job */
  type: string;
  enabled: boolean;
  /** disabled | internal | external. Omit for non-exposed components. */
  exposeMode?: string;
  /** The component's own template. Present on every component of a composed app. */
  template?: { name: string; version?: string };
  /** Per-component Helm values overlay, deep-merged onto this component's chart
   *  values at publish (value-based config — image/port/command/etc. in the shape
   *  THIS chart expects, so BYO charts work like canonical ones). */
  values?: Record<string, unknown>;
  /** Whether the component inherits ALL app vars (<app>-config + <app>-secrets).
   *  Omit/true = inherit; false = only the curated envVars (no app secrets). */
  inheritAppVars?: boolean;
  /** Curated env vars when not inheriting all app vars. */
  envVars?: ComponentEnvVar[];
  /** Kargo image bindings (repo + tag-key path in this component's overlay). */
  images?: ComponentImage[];
  /** Stateful (a database/cache): renders as its own prune-disabled Application. */
  stateful?: boolean;
  /** Override preview inclusion. Omit for the type default (web/worker on;
   *  stateful + job/cron off). */
  previewEnabled?: boolean;
}

// ComponentImage marks one of a composed component's discovered images as managed
// by Kargo, keyed by its tag-key path. The repository is discovered from the
// component's values (not stored) except as a legacy/BYO fallback.
export interface ComponentImage {
  /** discovered image slot name (display only; tagKey is the match key) */
  name?: string;
  tagKey: string;
  /** derived from discovery; present only for legacy/BYO fallback selections */
  repository?: string;
  tagPattern?: string;
  selectionStrategy?: string;
}

export interface CreateAppRequest {
  name: string;
  displayName?: string;
  description?: string;
  template: string;
  /** Components of a composed app, each with its own template + config. When set
   *  (every component carries a template), the app renders as one multi-source
   *  Application. Omit for a single-template app. */
  components?: ComponentCreate[];
  values: Record<string, unknown>;
  secretRefs: SecretRefInput[];
  /** "app" (default) — dedicated namespace per app+env; "project" — share the project namespace */
  namespaceScope?: "app" | "project";
  /** Optional namespace pattern override. Only applies when namespaceScope is "app". */
  namespacePattern?: string;
  /** Optional freeform Helm values overlay, deep-merged onto generated values at
   *  publish. String leaves may reference ((platform.*))/((vars.*)) tokens. No secrets. */
  rawValues?: Record<string, unknown>;
  /** App-level per-component config keyed by component name. */
  componentConfigs?: Record<string, ComponentConfig>;
  /** Per-(env, component) overrides keyed env → component. */
  envComponents?: Record<string, Record<string, ComponentConfig>>;
  /** Continuous-delivery settings (external-CD tag ownership). */
  cd?: CDConfig;
  /** Per-slot image repository bindings (keyed by template slot name). */
  images?: AppImageBinding[];
  /** "pipeline" (default) or "direct". Omit to inherit the template default. */
  deliveryMode?: string;
  /** Per-env target clusters, keyed env name → cluster names. Value ["*"] = ALL
   *  of that env's clusters (dynamic); explicit list = that subset; an omitted
   *  env (or empty array) = inherit the env default (active cluster). */
  targetClusters?: Record<string, string[]>;
  /** App-level (all-environments) non-secret env vars, committed to Git. */
  envConfig?: { vars?: Record<string, string> };
  /** Per-env non-secret env-var overrides, keyed env name (wins over envConfig). */
  envConfigByEnv?: Record<string, { vars?: Record<string, string> }>;
  /** Per-(env, component) Helm values overlays set at creation, keyed
   *  env → component → overlay. A new app deploys to its base env only, so the
   *  create wizard populates just that env; component values are per-env only
   *  (no all-envs base at creation). */
  envComponentValues?: Record<
    string,
    Record<string, Record<string, unknown>>
  >;
}

export interface CreateAppResponse {
  app: AppDetail;
}

// --- Deployment history types ---

/** One ArgoCD sync event in the deployment history of an app environment. */
export interface DeploymentHistoryEntry {
  /** ArgoCD-assigned sequence number; higher = more recent. */
  id: number;
  /** Git commit SHA that was synced. */
  revision?: string;
  /** RFC 3339 timestamp when the sync completed. */
  deployedAt?: string;
  /** RFC 3339 timestamp when the sync began (may be absent). */
  deployStartedAt?: string;
  /** Source Git repository URL. */
  repoURL?: string;
  /** Path within the repository that was synced. */
  path?: string;
  /** Git ref (branch/tag/commit) tracked by the ArgoCD Application. */
  targetRevision?: string;
}

/** Response from GET .../environments/:env/history */
export interface AppDeploymentHistoryResponse {
  /** False when no ArgoCD history reader is wired; history is then empty. */
  available?: boolean;
  project: string;
  app: string;
  environment: string;
  /** Reverse-chronological order (most recent first). Empty when not deployed. */
  history: DeploymentHistoryEntry[];
}

// --- Project API tokens ---

/** Roles a project API token may carry (org_admin is never a token role). */
export type ProjectTokenRole = "viewer" | "developer" | "project_admin";

/** Metadata for a project API token. Never carries the secret. */
export interface ApiToken {
  id: string;
  name: string;
  project: string;
  role: string;
  createdBy: string;
  createdAt: string;
  /** RFC 3339 expiry; absent means the token never expires. */
  expiresAt?: string;
}

/** Body for POST .../projects/:project/tokens. */
export interface CreateTokenRequest {
  name: string;
  /** Defaults to "developer" server-side when omitted. */
  role?: ProjectTokenRole;
  /** Omit or 0 to never expire. */
  expiresInDays?: number;
}

/** Create response: token metadata plus the one-time plaintext secret. */
export interface CreatedToken extends ApiToken {
  /** Shown exactly once — unrecoverable afterwards. */
  token: string;
}

export interface TokensResponse {
  tokens: ApiToken[];
}
