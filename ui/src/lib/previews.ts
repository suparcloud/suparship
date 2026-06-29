import { api } from "./api";
import type {
  AppPreviewSummary,
  CreateAppPreviewRequest,
  CreatePreviewRequest,
  PreviewEnvironment,
  PreviewGroupsResponse,
  PreviewsResponse,
} from "../types";

// listPreviewGroups returns previews grouped by PR (preview name) across all
// projects — one item per PR with its per-app previews nested.
export function listPreviewGroups(
  project?: string,
): Promise<PreviewGroupsResponse> {
  const q = project ? `?project=${encodeURIComponent(project)}` : "";
  return api.get<PreviewGroupsResponse>(`/preview-groups${q}`);
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

// deleteAppPreview removes an app-scoped preview. Use this for previews created
// via createAppPreview — they live in the app store, not the legacy preview
// store that deletePreview targets.
export function deleteAppPreview(
  project: string,
  app: string,
  name: string,
): Promise<void> {
  return api.del(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/previews/${encodeURIComponent(name)}`,
  );
}

// deletePreview removes a preview via the legacy service-oriented endpoint.
// Deprecated: prefer deleteAppPreview for app-scoped previews.
export function deletePreview(name: string): Promise<void> {
  return api.del(`/previews/${encodeURIComponent(name)}`);
}
