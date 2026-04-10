import { api } from "./api";

export interface ProjectEnvironment {
  name: string;
  displayName?: string;
  project: string;
  namespace: string;
  order: number;
  clusterRef?: string;
  baseDomain?: string;
  namespacePattern?: string;
}

interface EnvironmentsResponse {
  environments: ProjectEnvironment[];
}

export interface UpsertEnvironmentRequest {
  name?: string;
  displayName?: string;
  order?: number;
  clusterRef?: string;
  baseDomain?: string;
  namespacePattern?: string;
}

export function listProjectEnvironments(
  project: string,
): Promise<ProjectEnvironment[]> {
  return api
    .get<EnvironmentsResponse>(
      `/projects/${encodeURIComponent(project)}/environments`,
    )
    .then((res) => res.environments ?? []);
}

export function createProjectEnvironment(
  project: string,
  req: UpsertEnvironmentRequest & { name: string },
): Promise<ProjectEnvironment> {
  return api.post<ProjectEnvironment>(
    `/projects/${encodeURIComponent(project)}/environments`,
    req,
  );
}

export function updateProjectEnvironment(
  project: string,
  env: string,
  req: UpsertEnvironmentRequest,
): Promise<ProjectEnvironment> {
  return api.put<ProjectEnvironment>(
    `/projects/${encodeURIComponent(project)}/environments/${encodeURIComponent(env)}`,
    req,
  );
}

export function deleteProjectEnvironment(
  project: string,
  env: string,
): Promise<void> {
  return api.del(
    `/projects/${encodeURIComponent(project)}/environments/${encodeURIComponent(env)}`,
  );
}
