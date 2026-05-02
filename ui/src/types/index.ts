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
  team: string;
  role: string;
}

export interface ProjectRBACResponse {
  project: string;
  roleBindings: RoleBinding[];
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
  archiveSize: number;
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
  repoURL: string;
  ref: string;
  path: string;
  // Provider drives the credentials data-key shape: token for github/
  // gitlab/gitea, username+password for bitbucket/generic. Empty defaults
  // to generic.
  provider?: TemplateRepoProvider | "";
  // existingSecret is a K8s Secret name in suparship-system. UI-managed
  // credentials write to a deterministic name (suparship-tpl-credentials-
  // <source>); operators may also point at a hand-managed Secret.
  existingSecret?: string;
}

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
}

// --- Onboarding types ---

export interface OnboardingStatus {
  clusterConnected: boolean;
  authConfigured: boolean;
  orgExists: boolean;
  hasProjects: boolean;
  hasEnvironments: boolean;
  hasServices: boolean;
  complete: boolean;
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

export interface AppStatusSummary {
  phase: string;
  replicas: number;
  available: number;
  lastDeployed?: string;
}

export interface PreviewMeta {
  previewName: string;
  createdAt?: string;
}

export interface AppEnvironmentSummary {
  envName: string;
  envType: "staging" | "prod" | "preview";
  namespace: string;
  urls: string[];
  release?: AppReleaseRef;
  status: AppStatusSummary;
  preview?: PreviewMeta;
}

export interface AppSummary {
  name: string;
  project: string;
  displayName?: string;
  description?: string;
  template: AppTemplateRef;
  status: AppStatusSummary;
  urls: string[];
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
  project: string;
  app: string;
  environment: string;
  /** Reverse-chronological order (most recent first). Empty when not deployed. */
  history: DeploymentHistoryEntry[];
}
