import { api } from "./api";
import type {
  AppImageBinding,
  CDConfig,
  ComponentConfig,
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

export function listApps(project: string): Promise<AppListResponse> {
  return api.get<AppListResponse>(
    `/projects/${encodeURIComponent(project)}/apps`,
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
  // rawValues replaces the app-level freeform Helm values overlay.
  rawValues?: Record<string, unknown>;
  // envRawValues replaces per-environment overlays keyed by env name.
  envRawValues?: Record<string, Record<string, unknown>>;
  // componentConfigs replaces app-level per-component config keyed by name.
  componentConfigs?: Record<string, ComponentConfig>;
  // envComponents replaces per-(env, component) overrides keyed env → component.
  envComponents?: Record<string, Record<string, ComponentConfig>>;
  // cd replaces the app's continuous-delivery settings (external-CD tag
  // ownership). Omit to leave unchanged.
  cd?: CDConfig;
  // images replaces the app's per-slot image repository bindings (keyed by
  // template slot name). Send [] to clear; omit to leave unchanged.
  images?: AppImageBinding[];
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

export function updateApp(
  project: string,
  app: string,
  req: UpdateAppRequest,
): Promise<CreateAppResponse> {
  return api.patch<CreateAppResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}`,
    req,
  );
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
): Promise<EffectiveValuesResponse> {
  return api.post<EffectiveValuesResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/values/preview`,
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

export interface UpgradeAppTemplateResponse {
  message: string;
  project: string;
  app: string;
  fromVersion?: string;
  toVersion?: string;
}

/**
 * Pins an app to a specific template version and re-publishes. The
 * version must be one of those returned by GET /templates/{name}/versions.
 * On publish failure the backend rolls the pin back so a retry sees the
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
