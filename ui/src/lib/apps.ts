import { api } from "./api";
import type {
  AppDeploymentHistoryResponse,
  AppDetailResponse,
  AppEnvironmentResponse,
  AppEnvironmentsResponse,
  AppListResponse,
  AppLogsResponse,
  CreateAppRequest,
  CreateAppResponse,
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
