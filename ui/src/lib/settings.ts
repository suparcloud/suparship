import { api } from "./api";
import type {
  OrgInfo,
  TeamsResponse,
  ProjectsResponse,
  ProjectDetail,
  ProjectRBACResponse,
  RoleBinding,
} from "../types";

// ── OrgEnvironment ────────────────────────────────────────────────────────────

export interface OrgEnvironment {
  name: string;
  displayName?: string;
  order: number;
  clusterRef?: string;
  baseDomain?: string;
  namespacePattern?: string;
}

export interface OrgEnvironmentsResponse {
  environments: OrgEnvironment[];
}

export function listOrgEnvironments(): Promise<OrgEnvironmentsResponse> {
  return api.get<OrgEnvironmentsResponse>("/org/environments");
}

export function createOrgEnvironment(
  env: Omit<OrgEnvironment, "order"> & { order?: number },
): Promise<OrgEnvironment> {
  return api.post<OrgEnvironment>("/org/environments", env);
}

export function updateOrgEnvironment(
  name: string,
  env: Partial<Omit<OrgEnvironment, "name">>,
): Promise<OrgEnvironment> {
  return api.put<OrgEnvironment>(`/org/environments/${encodeURIComponent(name)}`, env);
}

export function deleteOrgEnvironment(name: string): Promise<void> {
  return api.del(`/org/environments/${encodeURIComponent(name)}`);
}

// ── Org / Teams / Projects ────────────────────────────────────────────────────

export function fetchOrg(): Promise<OrgInfo> {
  return api.get<OrgInfo>("/org");
}

export function fetchTeams(): Promise<TeamsResponse> {
  return api.get<TeamsResponse>("/teams");
}

export function fetchProjects(): Promise<ProjectsResponse> {
  return api.get<ProjectsResponse>("/projects");
}

export interface CreateProjectRequest {
  name: string;
  displayName?: string;
  description?: string;
}

export function createProject(req: CreateProjectRequest): Promise<import("../types").Project> {
  return api.post<import("../types").Project>("/projects", req);
}

export function deleteProject(name: string): Promise<void> {
  return api.del(`/projects/${encodeURIComponent(name)}`);
}

export function fetchProjectDetail(project: string): Promise<ProjectDetail> {
  return api.get<ProjectDetail>(`/projects/${encodeURIComponent(project)}`);
}

export function fetchProjectRBAC(
  project: string,
): Promise<ProjectRBACResponse> {
  return api.get<ProjectRBACResponse>(
    `/projects/${encodeURIComponent(project)}/rbac`,
  );
}

export async function fetchAllRoleBindings(): Promise<RoleBinding[]> {
  const { projects } = await fetchProjects();
  if (projects.length === 0) return [];

  const results = await Promise.allSettled(
    projects.map((p) => fetchProjectRBAC(p.name)),
  );

  const seen = new Set<string>();
  const bindings: RoleBinding[] = [];

  for (const result of results) {
    if (result.status !== "fulfilled") continue;
    for (const rb of result.value.roleBindings) {
      const key = `${rb.project}|${rb.team}|${rb.role}`;
      if (!seen.has(key)) {
        seen.add(key);
        bindings.push(rb);
      }
    }
  }

  bindings.sort((a, b) => {
    if (a.project !== b.project) {
      if (a.project === "*") return -1;
      if (b.project === "*") return 1;
      return a.project.localeCompare(b.project);
    }
    return a.team.localeCompare(b.team);
  });

  return bindings;
}
