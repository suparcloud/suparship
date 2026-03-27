export interface AuthUser {
  username: string;
  role: string;
}

export interface MetaInfo {
  app: string;
  version: string;
  commit: string;
  buildDate: string;
}

export interface Project {
  name: string;
}

export interface Service {
  name: string;
  project: string;
}

export interface PreviewEnvironment {
  name: string;
  status: string;
  url?: string;
}

export interface OrgInfo {
  name: string;
  displayName: string;
  createdAt?: string;
}

export interface TeamInfo {
  name: string;
  displayName: string;
  members: string[];
}

export interface TeamsResponse {
  teams: TeamInfo[];
}

export interface ProjectsResponse {
  projects: Project[];
}

export interface RoleBinding {
  project: string;
  team: string;
  role: string;
}

export interface ProjectRBACResponse {
  project: string;
  roleBindings: RoleBinding[];
}
