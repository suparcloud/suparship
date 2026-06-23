import { api } from "./api";
import type {
  AppPreviewSummary,
  CreateAppPreviewRequest,
  CreatePreviewRequest,
  PreviewEnvironment,
  PreviewsResponse,
} from "../types";

export function fetchPreviews(): Promise<PreviewsResponse> {
  return api.get<PreviewsResponse>("/previews");
}

export function fetchServicePreviews(
  project: string,
  service: string,
): Promise<PreviewsResponse> {
  return api.get<PreviewsResponse>(
    `/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}/previews`,
  );
}

export function createPreview(
  req: CreatePreviewRequest,
): Promise<PreviewEnvironment> {
  return api.post<PreviewEnvironment>("/previews", req);
}

// createAppPreview provisions a preview for an app. The preview clones req.baseEnv
// (default: the app's first stable env) — reusing its cluster, config and vault.
export function createAppPreview(
  project: string,
  app: string,
  req: CreateAppPreviewRequest,
): Promise<AppPreviewSummary> {
  return api.post<AppPreviewSummary>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/previews`,
    req,
  );
}

export function deletePreview(name: string): Promise<void> {
  return api.del(`/previews/${encodeURIComponent(name)}`);
}
