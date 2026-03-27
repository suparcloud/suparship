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

export interface TemplateSummary {
  name: string;
  version: string;
  title: string;
  description?: string;
  category: string;
  engine: string;
}

export interface TemplatesResponse {
  templates: TemplateSummary[];
}

export interface TemplateInput {
  name: string;
  title: string;
  type: "string" | "number" | "boolean" | "enum";
  description?: string;
  required: boolean;
  default?: string | number | boolean;
  options: string[];
  min?: number;
  max?: number;
  pattern?: string;
}

export interface TemplateSecretInput {
  name: string;
  title: string;
  description?: string;
  secretRef: string;
}

export interface TemplatePreset {
  name: string;
  title: string;
  description?: string;
  values: Record<string, unknown>;
}

export interface TemplateDetail {
  name: string;
  version: string;
  title: string;
  description?: string;
  category: string;
  engine: string;
  inputs: TemplateInput[];
  advancedInputs: TemplateInput[];
  secretInputs: TemplateSecretInput[];
  presets: TemplatePreset[];
}

export interface CreateServiceRequest {
  name: string;
  template: string;
  values: Record<string, unknown>;
  secretRefs: SecretRefInput[];
}

export interface SecretRefInput {
  name: string;
  secretRef: string;
}

export interface CreateServiceResponse {
  service: {
    name: string;
    template: { name: string; version?: string };
    values: Record<string, unknown>;
    secretRefs: SecretRefInput[];
  };
  helmValues: Record<string, unknown>;
}
