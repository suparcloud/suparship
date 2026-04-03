import { api } from "./api";
import type {
  AppDetailResponse,
  AppEnvironmentResponse,
  AppEnvironmentsResponse,
  AppListResponse,
  AppLogsResponse,
  CreateAppRequest,
  CreateAppResponse,
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
