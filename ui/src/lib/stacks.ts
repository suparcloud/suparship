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
  envConfigByEnv?: Record<string, EnvConfig>;
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
  envConfigByEnv?: Record<string, EnvConfig>;
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

// deleteStack removes the stack record. By default member apps are detached and
// kept; with deleteApps=true every member app is deleted and the stack's shared
// namespaces are reclaimed (full teardown).
export function deleteStack(
  project: string,
  stack: string,
  deleteApps = false,
): Promise<void> {
  const q = deleteApps ? "?deleteApps=true" : "";
  return api.del(`${base(project)}/${encodeURIComponent(stack)}${q}`);
}

// --- Batch lifecycle (Phase 3) ---

// stackOpResult is one app's outcome in a batch operation.
export interface StackOpResult {
  app: string;
  ok: boolean;
  message?: string;
  error?: string;
}

export interface StackBatchResponse {
  project: string;
  stack: string;
  action: string;
  results: StackOpResult[];
}

const stackBase = (project: string, stack: string) =>
  `${base(project)}/${encodeURIComponent(stack)}`;

// syncStack republishes every member app of the stack.
export function syncStack(
  project: string,
  stack: string,
): Promise<StackBatchResponse> {
  return api.post<StackBatchResponse>(`${stackBase(project, stack)}/sync`, {});
}

// promoteStack promotes every member app to the target environment.
export function promoteStack(
  project: string,
  stack: string,
  targetEnvironment: string,
): Promise<StackBatchResponse> {
  return api.post<StackBatchResponse>(`${stackBase(project, stack)}/promote`, {
    targetEnvironment,
  });
}

// createStackPreview brings up a preview of the whole stack co-located in one
// {project}-{stack}-preview-{name} namespace.
export function createStackPreview(
  project: string,
  stack: string,
  name: string,
): Promise<StackBatchResponse> {
  return api.post<StackBatchResponse>(`${stackBase(project, stack)}/previews`, {
    name,
  });
}

// CloneStackRequest duplicates a stack. Override fields, when set, replace the
// value copied from the source; appNames maps source app name -> new app name
// (default {newName}-{oldApp}).
export interface CloneStackRequest {
  newName: string;
  appNames?: Record<string, string>;
  displayName?: string;
  description?: string;
  sharedNamespace?: boolean;
  namespacePattern?: string;
  rawValues?: Record<string, unknown>;
  envRawValues?: Record<string, Record<string, unknown>>;
  envConfig?: EnvConfig;
}

export interface CloneStackResponse {
  stack: Stack;
  results: StackOpResult[];
}

// cloneStack creates a copy of the stack (source stays intact) with each member
// recreated under a derived name. App-tier secret values are NOT migrated.
export function cloneStack(
  project: string,
  stack: string,
  req: CloneStackRequest,
): Promise<CloneStackResponse> {
  return api.post<CloneStackResponse>(`${stackBase(project, stack)}/clone`, req);
}

// deleteStackPreview tears down a stack preview across all members.
export function deleteStackPreview(
  project: string,
  stack: string,
  name: string,
): Promise<void> {
  return api.del(
    `${stackBase(project, stack)}/previews/${encodeURIComponent(name)}`,
  );
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
