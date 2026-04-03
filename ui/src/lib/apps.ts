import { api } from "./api";
import type {
  AppDetailResponse,
  AppEnvironmentResponse,
  AppEnvironmentsResponse,
  AppListResponse,
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
