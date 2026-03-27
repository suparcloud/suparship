import { api } from "./api";
import type { CreateServiceRequest, CreateServiceResponse } from "../types";

export function createService(
  project: string,
  req: CreateServiceRequest,
): Promise<CreateServiceResponse> {
  return api.post<CreateServiceResponse>(
    `/projects/${encodeURIComponent(project)}/services`,
    req,
  );
}
