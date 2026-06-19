import { api } from "./api";
import type { EnvConfig } from "./envconfig";

// A stack groups tightly-coupled apps inside a project with a shared override
// layer (env/values/secrets), an optional shared namespace, and batch actions.
export interface Stack {
  name: string;
  project: string;
  displayName?: string;
  description?: string;
  sharedNamespace?: boolean;
  namespacePattern?: string;
  rawValues?: Record<string, unknown>;
  envRawValues?: Record<string, Record<string, unknown>>;
  envConfig?: EnvConfig;
  apps: string[]; // member app names
}

export interface StacksResponse {
  project: string;
  stacks: Stack[];
}

export interface CreateStackRequest {
  name: string;
  displayName?: string;
  description?: string;
  sharedNamespace?: boolean;
  namespacePattern?: string;
}

export interface UpdateStackRequest {
  displayName?: string;
  description?: string;
  sharedNamespace?: boolean;
  namespacePattern?: string;
  rawValues?: Record<string, unknown>;
  envRawValues?: Record<string, Record<string, unknown>>;
  envConfig?: EnvConfig;
}

const base = (project: string) =>
  `/projects/${encodeURIComponent(project)}/stacks`;

export function listStacks(project: string): Promise<StacksResponse> {
  return api.get<StacksResponse>(base(project));
}

export function getStack(project: string, stack: string): Promise<Stack> {
  return api.get<Stack>(`${base(project)}/${encodeURIComponent(stack)}`);
}

export function createStack(
  project: string,
  req: CreateStackRequest,
): Promise<Stack> {
  return api.post<Stack>(base(project), req);
}

export function updateStack(
  project: string,
  stack: string,
  req: UpdateStackRequest,
): Promise<Stack> {
  return api.patch<Stack>(`${base(project)}/${encodeURIComponent(stack)}`, req);
}

export function deleteStack(project: string, stack: string): Promise<void> {
  return api.del(`${base(project)}/${encodeURIComponent(stack)}`);
}

// setAppStack adds an app to a stack (or removes it when stack is ""). The
// server republishes the app so the stack override layer takes effect.
export function setAppStack(
  project: string,
  app: string,
  stack: string,
): Promise<unknown> {
  return api.put(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/stack`,
    { stack },
  );
}
