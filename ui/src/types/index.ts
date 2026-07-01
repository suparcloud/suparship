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
  // Platform-Engineer-authored Helm values overlays (all-envs + per-env),
  // layered above chart defaults and below developer overrides.
  defaultValues?: Record<string, unknown>;
  envValues?: Record<string, Record<string, unknown>>;
  // Values mode: undefined/true = canonical suparship-common base; false =
  // passthrough/BYO.
  injectCanonicalValues?: boolean;
  // Per-service image mapping for external-CD (Kargo) wiring.
  images?: TemplateImage[];
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
  // whether {platform.*}/{vars.*} tokens were resolved (always false in v1).
  interpolated: boolean;
  // overlays that contributed, low→high.
  layers: string[];
  // images discovered in the effective values (every image block with a
  // repository), each with its dotted tag key. The CD UI lists these so the user
  // can select which Kargo manages. Omitted/empty when none are found.
  discoveredImages?: TemplateImage[];
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
  type: "web" | "worker" | "cron";
  enabledInPreview: boolean;
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

export interface CreateAppRequest {
  name: string;
  displayName?: string;
  description?: string;
  template: string;
  values: Record<string, unknown>;
  secretRefs: SecretRefInput[];
  /** "app" (default) — dedicated namespace per app+env; "project" — share the project namespace */
  namespaceScope?: "app" | "project";
  /** Optional namespace pattern override. Only applies when namespaceScope is "app". */
  namespacePattern?: string;
  /** Optional freeform Helm values overlay, deep-merged onto generated values at
   *  publish. String leaves may reference {platform.*}/{vars.*} tokens. No secrets. */
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
