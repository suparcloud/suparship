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
  // Every Cluster registered with this env. Today only activeClusterRef is
  // deployed to; the rest are reserved for future multi-cluster fan-out.
  clusterRefs?: string[];
  // The single member of clusterRefs currently receiving deploys. Empty
  // falls back to clusterRefs[0] at the server.
  activeClusterRef?: string;
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

// ── Org Namespace Naming ──────────────────────────────────────────────────────

export interface OrgNaming {
  projectNamespace?: string;
  appNamespace?: string;
}

export function getOrgNaming(): Promise<OrgNaming> {
  return api.get<OrgNaming>("/org/naming");
}

export function updateOrgNaming(naming: OrgNaming): Promise<OrgNaming> {
  return api.put<OrgNaming>("/org/naming", naming);
}

// ── Org Routing Profiles ──────────────────────────────────────────────────────
//
// A routing profile maps an ExposeMode (internal/external) to the ingress
// class + cert-manager ClusterIssuer the chart should use for components
// targeting that tier. Per-environment overrides live on the env record;
// these endpoints manage org-level defaults only.

export type ExposeMode = "internal" | "external";

export interface RoutingProfile {
  name: ExposeMode;
  ingressClassName: string;
  clusterIssuer?: string;
  baseDomain?: string;
}

export interface RoutingProfilesResponse {
  routingProfiles: RoutingProfile[];
}

export function listOrgRoutingProfiles(): Promise<RoutingProfilesResponse> {
  return api.get<RoutingProfilesResponse>("/org/routing-profiles");
}

export function upsertOrgRoutingProfile(
  name: ExposeMode,
  profile: Omit<RoutingProfile, "name">,
): Promise<RoutingProfile> {
  return api.put<RoutingProfile>(
    `/org/routing-profiles/${encodeURIComponent(name)}`,
    profile,
  );
}

export function deleteOrgRoutingProfile(name: ExposeMode): Promise<void> {
  return api.del(`/org/routing-profiles/${encodeURIComponent(name)}`);
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
