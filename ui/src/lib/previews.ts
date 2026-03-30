import { api } from "./api";
import type {
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

export function deletePreview(name: string): Promise<void> {
  return api.del(`/previews/${encodeURIComponent(name)}`);
}
