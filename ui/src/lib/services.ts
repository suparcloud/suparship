import { api } from "./api";
import type {
  CreateServiceRequest,
  CreateServiceResponse,
  EnvironmentsResponse,
  ProjectServicesResponse,
  PromoteRequest,
  PromoteResponse,
  ServiceDetailInfo,
} from "../types";

export function fetchEnvironments(): Promise<EnvironmentsResponse> {
  return api.get<EnvironmentsResponse>("/environments");
}

export function fetchProjectServices(
  project: string,
): Promise<ProjectServicesResponse> {
  return api.get<ProjectServicesResponse>(
    `/projects/${encodeURIComponent(project)}/services`,
  );
}

export function fetchServiceDetail(
  project: string,
  service: string,
): Promise<ServiceDetailInfo> {
  return api.get<ServiceDetailInfo>(
    `/projects/${encodeURIComponent(project)}/services/${encodeURIComponent(service)}`,
  );
}

export function createService(
  project: string,
  req: CreateServiceRequest,
): Promise<CreateServiceResponse> {
  return api.post<CreateServiceResponse>(
    `/projects/${encodeURIComponent(project)}/services`,
    req,
  );
}

export function promoteService(
  projectName: string,
  serviceName: string,
  req: PromoteRequest,
): Promise<PromoteResponse> {
  return api.post<PromoteResponse>(
    `/projects/${encodeURIComponent(projectName)}/services/${encodeURIComponent(serviceName)}/promote`,
    req,
  );
}
