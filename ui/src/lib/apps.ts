import { api, ApiError } from "./api";
import type {
  AppImageBinding,
  CDConfig,
  ComponentConfig,
  ComponentCreate,
  ComponentImage,
  AppDeploymentHistoryResponse,
  AppDetailResponse,
  AppEnvironmentResponse,
  AppEnvironmentsResponse,
  AppListResponse,
  AppLogsResponse,
  CreateAppRequest,
  CreateAppResponse,
  EffectiveValuesResponse,
  KargoAppPipeline,
  KargoPromotionStatus,
  PromoteRequest,
  PromoteResponse,
} from "../types";

// listApps returns a project's apps with live per-env status. Pass opts.stack to
// scope live-status enrichment to that stack's members: non-member apps are still
// returned (names, for pickers) but without the per-env cluster reads, so a stack
// detail view doesn't pay to enrich apps it won't show status for.
export function listApps(
  project: string,
  opts?: { stack?: string },
): Promise<AppListResponse> {
  const qs = opts?.stack
    ? `?stack=${encodeURIComponent(opts.stack)}`
    : "";
  return api.get<AppListResponse>(
    `/projects/${encodeURIComponent(project)}/apps${qs}`,
  );
}

export function getApp(
  project: string,
  app: string,
): Promise<AppDetailResponse> {
  return api.get<AppDetailResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}`,
  );
}

export function getAppEnvironments(
  project: string,
  app: string,
): Promise<AppEnvironmentsResponse> {
  return api.get<AppEnvironmentsResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/environments`,
  );
}

export function getAppEnvironment(
  project: string,
  app: string,
  env: string,
): Promise<AppEnvironmentResponse> {
  return api.get<AppEnvironmentResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/environments/${encodeURIComponent(env)}`,
  );
}

export function createApp(
  project: string,
  req: CreateAppRequest,
): Promise<CreateAppResponse> {
  return api.post<CreateAppResponse>(
    `/projects/${encodeURIComponent(project)}/apps`,
    req,
  );
}

// UpdateAppRequest edits an app's metadata + template input values. Omitted
// fields are left unchanged. Template name is immutable (use upgradeAppTemplate
// for the version).
// ClusterValueOverride mirrors the backend per-(env, cluster) override.
export interface ClusterValueOverride {
  replicas?: number;
  sizePreset?: string;
  values?: Record<string, unknown>;
  config?: Record<string, string>;
}

export interface UpdateAppRequest {
  displayName?: string;
  description?: string;
  values?: Record<string, unknown>;
  // clusterOverrides replaces per-(env, cluster) overrides, keyed env → cluster.
  clusterOverrides?: Record<string, Record<string, ClusterValueOverride>>;
  // targetClusters selects which of each env's clusters the app deploys to,
  // keyed env name → cluster names. Value ["*"] = ALL of that env's clusters
  // (dynamic); an explicit list = that subset; omit an env (or empty array) =
  // inherit the env default (active cluster). Mirrors clusterOverrides' keying.
  targetClusters?: Record<string, string[]>;
  // rawValues replaces the app-level freeform Helm values overlay.
  rawValues?: Record<string, unknown>;
  // envRawValues replaces per-environment overlays keyed by env name.
  envRawValues?: Record<string, Record<string, unknown>>;
  // components REPLACES the app's component list (edit-composed: add / remove /
  // retemplate). Each carries its own template; ≥2 makes the app composed.
  components?: ComponentCreate[];
  // componentConfigs replaces app-level per-component config keyed by name.
  componentConfigs?: Record<string, ComponentConfig>;
  // componentValues sets the base (all-env) Helm values overlay for the named
  // composed components. Only the named components change; {} clears one.
  componentValues?: Record<string, Record<string, unknown>>;
  // envComponentValues sets per-(env, component) overlay overrides keyed
  // env → component → overlay. Only the named pairs change; {} clears one.
  envComponentValues?: Record<string, Record<string, Record<string, unknown>>>;
  // envComponents replaces per-(env, component) overrides keyed env → component.
  envComponents?: Record<string, Record<string, ComponentConfig>>;
  // cd replaces the app's continuous-delivery settings (external-CD tag
  // ownership). Omit to leave unchanged.
  cd?: CDConfig;
  // images replaces the app's per-slot image repository bindings (keyed by
  // template slot name). Send [] to clear; omit to leave unchanged.
  images?: AppImageBinding[];
  // componentImages replaces the Kargo image selection of the named composed
  // components (component name → its images), so the single app-level Images
  // panel can manage a composed app's per-component images. Omit to leave
  // unchanged; a component mapped to [] clears it.
  componentImages?: Record<string, ComponentImage[]>;
  // deliveryMode switches the app's delivery mode ("pipeline" or "direct").
  // Omit to leave unchanged.
  deliveryMode?: string;
  // deployEnvs (direct apps) opts environments in/out of deployment, keyed by env
  // name → deploy. Opting out leaves a running env in place (use undeployAppEnv to
  // remove it). Omit to leave unchanged.
  deployEnvs?: Record<string, boolean>;
  // previewsEnabled toggles whether this app supports preview environments.
  // Omit to leave unchanged.
  previewsEnabled?: boolean;
}

// --- Accept-and-poll async operations ---
// The server can defer a slow operation (Prefer: respond-async / ?async=1):
// the request validates synchronously, returns 202 + a task id, and the heavy
// save + gitops publish runs on a server goroutine. We poll the task to its
// terminal state and return exactly what the synchronous call would have.

interface AcceptedTask {
  taskId: string;
  state: string;
  statusUrl: string;
}

interface AsyncTaskStatus<T> {
  id: string;
  state: "pending" | "running" | "succeeded" | "failed";
  status?: number;
  result?: T;
  error?: string;
}

function isAcceptedTask(v: unknown): v is AcceptedTask {
  return (
    typeof v === "object" && v !== null && "taskId" in v && "statusUrl" in v
  );
}

// pollTask polls a deferred operation until it succeeds or fails. The server
// keeps working regardless — a poll timeout only means the CLIENT stopped
// watching, so the error says so instead of implying the save was lost.
async function pollTask<T>(project: string, taskId: string): Promise<T> {
  const deadline = Date.now() + 15 * 60_000;
  for (;;) {
    await new Promise((r) => setTimeout(r, 2000));
    const t = await api.get<AsyncTaskStatus<T>>(
      `/projects/${encodeURIComponent(project)}/tasks/${encodeURIComponent(taskId)}`,
    );
    if (t.state === "succeeded") return t.result as T;
    if (t.state === "failed") {
      throw new ApiError(t.status ?? 500, t.error || "operation failed");
    }
    if (Date.now() > deadline) {
      throw new ApiError(
        504,
        "the save is still publishing on the server — refresh in a bit to see the result",
      );
    }
  }
}

// updateApp saves app changes and re-publishes to GitOps. Slow by nature (a
// many-component manage save syncs charts and renders every env), so it opts
// into the server's accept-and-poll async mode — the request can't hit a
// gateway 504; we poll the task to completion instead. A server without the
// async runner (fake mode / older builds) simply responds synchronously and the
// shape check falls through. Callers see the same promise either way.
export async function updateApp(
  project: string,
  app: string,
  req: UpdateAppRequest,
): Promise<CreateAppResponse> {
  const res = await api.patch<CreateAppResponse | AcceptedTask>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}?async=1`,
    req,
  );
  if (isAcceptedTask(res)) {
    return pollTask<CreateAppResponse>(project, res.taskId);
  }
  return res;
}

// previewAppValues computes the read-only effective values for an existing
// app + env, layering the supplied (possibly unsaved) overlays so the editor can
// preview live as the user types. Computes only — never mutates.
export function previewAppValues(
  project: string,
  app: string,
  env: string,
  body: {
    rawValues?: Record<string, unknown>;
    envRawValues?: Record<string, Record<string, unknown>>;
  },
  // preview=true layers the template+org preview defaults and the app's preview
  // override (sent as envRawValues.preview) on top of the base env `env` — so the
  // preview scope shows what actually deploys, matching composed apps.
  preview = false,
  // skipChart drops the chart defaults AND the canonical struct base so the
  // response is the CONCISE platform base (template ⊕ org overrides) for the env —
  // used to seed the single-component editor without the chart's default keys.
  skipChart = false,
): Promise<EffectiveValuesResponse> {
  const params = new URLSearchParams();
  if (preview) params.set("preview", "true");
  if (skipChart) params.set("skipChart", "true");
  const q = params.toString();
  return api.post<EffectiveValuesResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/values/preview${q ? `?${q}` : ""}`,
    body,
  );
}

// undeployAppEnv removes a single environment's resources from the cluster (the
// explicit "remove from cluster" action for a direct-delivery env). Destructive:
// for stateful apps this deletes that env's data.
export function undeployAppEnv(
  project: string,
  app: string,
  env: string,
): Promise<{ message: string }> {
  return api.post<{ message: string }>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/environments/${encodeURIComponent(env)}/undeploy`,
  );
}

// pinAppEnv promotes a PR preview's image to a stable env WITHOUT merging and
// pins it: the env holds that image and CD (Kargo auto-promotion) is paused for
// it until unpinned, so newer images don't override it.
export function pinAppEnv(
  project: string,
  app: string,
  env: string,
  fromPreview: string,
): Promise<{ message: string; imageTag: string }> {
  return api.post<{ message: string; imageTag: string }>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/environments/${encodeURIComponent(env)}/pin`,
    { fromPreview },
  );
}

// --- Rollback (previous deployments) ---

export interface RollbackCandidateImage {
  repository?: string;
  tag?: string;
}

/** One previously-deployed Kargo freight of an env (rollback target). */
export interface RollbackCandidate {
  freight: string;
  images: RollbackCandidateImage[];
  /** When the Warehouse discovered the build (not the promotion time). */
  discoveredAt?: string;
  /** The freight the env runs now — not a rollback target. */
  current?: boolean;
}

export interface RollbackCandidatesResponse {
  /** False = rollback isn't offered (no Kargo, direct app, unreadable stage). */
  available: boolean;
  candidates: RollbackCandidate[];
}

// getRollbackCandidates lists the freight an env has run, newest first.
export function getRollbackCandidates(
  project: string,
  app: string,
  env: string,
): Promise<RollbackCandidatesResponse> {
  return api.get<RollbackCandidatesResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/environments/${encodeURIComponent(env)}/rollback-candidates`,
  );
}

// rollbackAppEnv re-promotes a previously-deployed freight to the env and
// places a rollback hold: CD (auto-promotion) is paused for the env until
// resumed via unpinAppEnv.
export function rollbackAppEnv(
  project: string,
  app: string,
  env: string,
  freight: string,
): Promise<{ message: string; promotion: string }> {
  return api.post<{ message: string; promotion: string }>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/environments/${encodeURIComponent(env)}/rollback`,
    { freight },
  );
}

// unpinAppEnv clears a pin so the env returns to normal CD.
export function unpinAppEnv(
  project: string,
  app: string,
  env: string,
): Promise<void> {
  return api.del(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/environments/${encodeURIComponent(env)}/pin`,
  );
}

// suspendAppEnv scales an env's workload down (the env stays published, no data
// loss); resumeAppEnv brings it back. The chart honors the platform's suspend
// values key (default `suspend`).
export function suspendAppEnv(
  project: string,
  app: string,
  env: string,
): Promise<{ message: string }> {
  return api.post<{ message: string }>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/environments/${encodeURIComponent(env)}/suspend`,
    {},
  );
}

export function resumeAppEnv(
  project: string,
  app: string,
  env: string,
): Promise<{ message: string }> {
  return api.post<{ message: string }>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/environments/${encodeURIComponent(env)}/resume`,
    {},
  );
}

export function promoteApp(
  project: string,
  app: string,
  req: PromoteRequest,
): Promise<PromoteResponse> {
  return api.post<PromoteResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/promote`,
    req,
  );
}

export interface SyncAppResponse {
  message: string;
  project: string;
  app: string;
}

/**
 * Re-triggers the gitops publish pipeline for an existing app.
 * Useful for apps created before the gitops publisher was configured,
 * or that failed to push during creation.
 */
export function syncApp(
  project: string,
  app: string,
): Promise<SyncAppResponse> {
  return api.post<SyncAppResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/sync`,
  );
}

export interface UpgradedComponent {
  name: string;
  template: string;
  fromVersion?: string;
  toVersion: string;
}

export interface UpgradeAppTemplateResponse {
  message: string;
  project: string;
  app: string;
  /** The PRIMARY template's move, for the single-component headline. */
  fromVersion?: string;
  toVersion?: string;
  /** Every component whose pin actually moved. */
  components?: UpgradedComponent[];
  /** Components left alone because they render from a different template. */
  skipped?: string[];
}

/**
 * Upgrades the app's PRIMARY template: every component rendered by that template
 * moves to `version`, and so does the app-level pin. Components on a different
 * template are untouched and returned in `skipped`. The version must be one of
 * those the app detail response lists under templateVersions.
 *
 * On publish failure the backend rolls every pin back so a retry sees the
 * pre-upgrade state.
 */
export function upgradeAppTemplate(
  project: string,
  app: string,
  version: string,
): Promise<UpgradeAppTemplateResponse> {
  return api.post<UpgradeAppTemplateResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/upgrade-template`,
    { version },
  );
}

/**
 * Upgrades named components individually, keyed component name → target version.
 * This is the general form: a composed app mixes templates, so there is no single
 * app-level version that means anything for it. Each version is validated against
 * its own component's template, and the whole batch is applied atomically — one
 * bad version rejects the request without touching any pin.
 */
export function upgradeAppComponents(
  project: string,
  app: string,
  components: Record<string, string>,
  // Scope the upgrade to ONE stable environment (its per-env version pins);
  // omit to upgrade every environment at once.
  environment?: string,
): Promise<UpgradeAppTemplateResponse> {
  return api.post<UpgradeAppTemplateResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/upgrade-template`,
    environment ? { components, environment } : { components },
  );
}

/**
 * Polls the live status of a Kargo Promotion CR.
 * Used to track phase transitions (Pending → Running → Succeeded/Failed)
 * after triggering a promotion.
 */
export function getKargoPromotionStatus(
  project: string,
  app: string,
  promotionName: string,
): Promise<KargoPromotionStatus> {
  return api.get<KargoPromotionStatus>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/promotions/${encodeURIComponent(promotionName)}`,
  );
}

/**
 * Returns the live Kargo Stage statuses for all stages belonging to an app.
 * Each stage shows phase, health, current freight, and available freight count.
 */
export function getKargoAppPipeline(
  project: string,
  app: string,
): Promise<KargoAppPipeline> {
  return api.get<KargoAppPipeline>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/kargo/stages`,
  );
}

/**
 * Fetches the ArgoCD sync/deployment history for one app environment.
 * Returns reverse-chronological entries (most recent first).
 * Returns a response with an empty history array when not yet deployed.
 * The server returns 501 when the deployment history reader is not configured
 * (e.g. no ArgoCD integration); callers should handle this gracefully.
 */
export function getAppDeploymentHistory(
  project: string,
  app: string,
  env: string,
): Promise<AppDeploymentHistoryResponse> {
  return api.get<AppDeploymentHistoryResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/environments/${encodeURIComponent(env)}/history`,
  );
}

export function fetchAppLogs(
  project: string,
  app: string,
  params: {
    environment: string;
    component?: string;
    pod?: string;
    container?: string;
    tailLines?: number;
  },
): Promise<AppLogsResponse> {
  const qs = new URLSearchParams({ environment: params.environment });
  if (params.component) qs.set("component", params.component);
  if (params.pod) qs.set("pod", params.pod);
  if (params.container) qs.set("container", params.container);
  if (params.tailLines !== undefined)
    qs.set("tailLines", String(params.tailLines));
  return api.get<AppLogsResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/logs?${qs.toString()}`,
  );
}

export function deleteApp(project: string, app: string): Promise<void> {
  return api.del(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}`,
  );
}

// renameApp changes an app's identity name. The server recreates the app under
// the new name (new ArgoCD Apps, Kargo CRs, namespaces) and tears down the old.
// A deployed app briefly redeploys; app-level secrets must be re-entered under
// the new name. Returns the renamed app's detail.
export function renameApp(
  project: string,
  app: string,
  newName: string,
): Promise<AppDetailResponse> {
  return api.post<AppDetailResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/rename`,
    { newName },
  );
}
