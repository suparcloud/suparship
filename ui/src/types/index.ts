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

// --- Onboarding types ---

export interface OnboardingStatus {
  clusterConnected: boolean;
  authConfigured: boolean;
  orgExists: boolean;
  hasProjects: boolean;
  hasEnvironments: boolean;
  hasServices: boolean;
  complete: boolean;
}

// --- Inventory / Runtime types ---

export interface EnvironmentInfo {
  name: string;
  displayName?: string;
  project: string;
  namespace: string;
  order: number;
}

export interface EnvironmentsResponse {
  environments: EnvironmentInfo[];
}

export interface RuntimeInfo {
  status: string;
  image?: string;
  replicas: number;
  available: number;
  ingressUrls: string[];
  namespace: string;
  lastDeployed?: string;
}

export interface ServiceRuntime {
  name: string;
  template: { name: string; version?: string };
  runtime: RuntimeInfo;
}

export interface ProjectServicesResponse {
  project: string;
  services: ServiceRuntime[];
}

export interface ServiceEnv {
  environment: string;
  namespace: string;
  runtime: RuntimeInfo;
}

export interface ServiceDetailInfo {
  name: string;
  project: string;
  template: { name: string; version?: string };
  values: Record<string, unknown>;
  secretRefs: SecretRefInput[];
  environments: ServiceEnv[];
}

// --- Service creation types ---

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
