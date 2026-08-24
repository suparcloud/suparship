import { api } from "./api";
import type {
  OrgInfo,
  TeamInfo,
  TeamsResponse,
  ProjectsResponse,
  ProjectDetail,
  ProjectRBACResponse,
  RoleBinding,
  RoleBindingsResponse,
  AuthConfigResponse,
  UpdateOIDCRequest,
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
  // "active" (deploy to activeClusterRef only) or "all" (fan out to every
  // clusterRef). Empty = active.
  deployMode?: string;
  baseDomain?: string;
  namespacePattern?: string;
  // Set on an update response only when the change relocates workloads (active
  // cluster or namespace pattern moved) and was saved but NOT auto-applied.
  warning?: string;
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

// ── Team management (org_admin) ───────────────────────────────────────────────

export interface UpsertTeamRequest {
  name?: string;
  displayName?: string;
  members?: string[];
}

export function createTeam(req: UpsertTeamRequest): Promise<TeamInfo> {
  return api.post<TeamInfo>("/teams", req);
}

export function updateTeam(
  name: string,
  req: Omit<UpsertTeamRequest, "name">,
): Promise<TeamInfo> {
  return api.put<TeamInfo>(`/teams/${encodeURIComponent(name)}`, req);
}

export function deleteTeam(name: string): Promise<void> {
  return api.del(`/teams/${encodeURIComponent(name)}`);
}

// ── Role bindings (org_admin) ─────────────────────────────────────────────────

export function listRoleBindings(): Promise<RoleBindingsResponse> {
  return api.get<RoleBindingsResponse>("/role-bindings");
}

export function createRoleBinding(rb: RoleBinding): Promise<RoleBinding> {
  return api.post<RoleBinding>("/role-bindings", rb);
}

export function deleteRoleBinding(rb: RoleBinding): Promise<void> {
  const params = new URLSearchParams({ project: rb.project, role: rb.role });
  if (rb.team) params.set("team", rb.team);
  if (rb.group) params.set("group", rb.group);
  return api.del(`/role-bindings?${params.toString()}`);
}

// ── OIDC SSO config (read open; write org_admin) ──────────────────────────────

export function getAuthConfig(): Promise<AuthConfigResponse> {
  return api.get<AuthConfigResponse>("/org/auth");
}

export function updateAuthConfig(
  req: UpdateOIDCRequest,
): Promise<AuthConfigResponse> {
  return api.put<AuthConfigResponse>("/org/auth", req);
}

export function fetchProjects(): Promise<ProjectsResponse> {
  return api.get<ProjectsResponse>("/projects");
}

// ── Org Namespace Naming ──────────────────────────────────────────────────────

export interface OrgNaming {
  projectNamespace?: string;
  appNamespace?: string;
  /** ArgoCD Application name pattern. Tokens {project}/{app}/{env}/{cluster}.
   *  Empty → default "{project}-{app}-{cluster}". Must contain {app} and {cluster}. */
  argoAppName?: string;
}

export function getOrgNaming(): Promise<OrgNaming> {
  return api.get<OrgNaming>("/org/naming");
}

export function updateOrgNaming(naming: OrgNaming): Promise<OrgNaming> {
  return api.put<OrgNaming>("/org/naming", naming);
}

// ── Org Endpoints (URL scheme) ────────────────────────────────────────────────

export interface OrgEndpoints {
  /** true → generated app URLs use https:// (default); false → http:// for
   *  TLS-less local/dev ingress. */
  secureEndpoints: boolean;
}

export function getOrgEndpoints(): Promise<OrgEndpoints> {
  return api.get<OrgEndpoints>("/org/endpoints");
}

export function updateOrgEndpoints(e: OrgEndpoints): Promise<OrgEndpoints> {
  return api.put<OrgEndpoints>("/org/endpoints", e);
}

// ── Org Routing Profiles ──────────────────────────────────────────────────────
//
// A routing profile maps an ExposeMode (internal/external) to the ingress
// class + cert-manager ClusterIssuer the chart should use for components
// targeting that tier. Per-environment overrides live on the env record;
// these endpoints manage org-level defaults only.

export type ExposeMode = "internal" | "external";

export interface GatewayRef {
  name: string;
  namespace?: string;
  sectionName?: string;
}

export interface RoutingProfile {
  name: ExposeMode;
  ingressClassName: string;
  clusterIssuer?: string;
  baseDomain?: string;
  gateway?: GatewayRef;
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

// fetchAllRoleBindings returns every role binding org-wide, sorted with the
// wildcard project first. Backed by the org-level /role-bindings endpoint.
export async function fetchAllRoleBindings(): Promise<RoleBinding[]> {
  const { roleBindings } = await listRoleBindings();
  const subject = (rb: RoleBinding) => rb.team ?? rb.group ?? "";
  return [...roleBindings].sort((a, b) => {
    if (a.project !== b.project) {
      if (a.project === "*") return -1;
      if (b.project === "*") return 1;
      return a.project.localeCompare(b.project);
    }
    return subject(a).localeCompare(subject(b));
  });
}
