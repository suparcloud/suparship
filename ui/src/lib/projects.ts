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
  /**
   * origin describes how this environment was resolved:
   *   "org"      — inherited from org defaults, no project customisation
   *   "override" — org environment with project-level field overrides applied
   *   "project"  — project-specific environment not defined in org defaults
   */
  origin?: "org" | "override" | "project";
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

// ── Project namespace naming ──────────────────────────────────────────────────

export interface ProjectNaming {
  namespacePattern?: string;
}

export function getProjectNaming(project: string): Promise<ProjectNaming> {
  return api.get<ProjectNaming>(
    `/projects/${encodeURIComponent(project)}/naming`,
  );
}

export function updateProjectNaming(
  project: string,
  naming: ProjectNaming,
): Promise<ProjectNaming> {
  return api.put<ProjectNaming>(
    `/projects/${encodeURIComponent(project)}/naming`,
    naming,
  );
}
