import { api } from "./api";

// ── Types ──────────────────────────────────────────────────────────────────────

export interface SecretRef {
  envKey: string;
  provider: string;
  secretName: string;
  secretKey: string;
}

export interface EnvConfig {
  vars?: Record<string, string>;
  secretRefs?: SecretRef[];
  // Rendered ConfigMap the vars land in (the value behind
  // platform.configMapName). Only present on app-scoped GET responses — every
  // layer merges into one <app>-config per environment. Read-only.
  configMapName?: string;
}

export interface ResolvedEnvVar {
  key: string;
  value: string;
  source: string;
  isSecret: boolean;
}

export interface ResolvedEnvConfigResponse {
  vars: ResolvedEnvVar[];
  configMapName?: string;
}

// ── Per-base-env preview band ─────────────────────────────────────────────────
// Env vars for previews whose BASE env is {env} (previews of staging vs
// previews of production can differ). Layered above the legacy all-previews
// band at preview publish.

export function getAppPreviewEnvConfig(
  project: string,
  app: string,
  env: string,
): Promise<EnvConfig> {
  return api.get<EnvConfig>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/preview-envconfig`,
  );
}

export function updateAppPreviewEnvConfig(
  project: string,
  app: string,
  env: string,
  cfg: EnvConfig,
): Promise<unknown> {
  return api.put(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/preview-envconfig`,
    cfg,
  );
}

// ── Org level ──────────────────────────────────────────────────────────────────

export function getOrgEnvConfig(): Promise<EnvConfig> {
  return api.get<EnvConfig>("/org/envconfig");
}

export function updateOrgEnvConfig(cfg: EnvConfig): Promise<unknown> {
  return api.put<unknown>("/org/envconfig", cfg);
}

// ── Environment-type level ─────────────────────────────────────────────────────

export function getEnvTypeEnvConfig(envType: string): Promise<EnvConfig> {
  return api.get<EnvConfig>(`/org/envconfig/${encodeURIComponent(envType)}`);
}

export function updateEnvTypeEnvConfig(
  envType: string,
  cfg: EnvConfig,
): Promise<unknown> {
  return api.put<unknown>(
    `/org/envconfig/${encodeURIComponent(envType)}`,
    cfg,
  );
}

// ── Cluster level (platform escape hatch — wins over every layer) ─────────────

// getClusterEnvConfig reads cluster-scope vars. Pass env to read the
// per-(cluster, env) override set instead of the cluster-global set.
export function getClusterEnvConfig(
  cluster: string,
  env?: string,
): Promise<EnvConfig> {
  const q = env ? `?env=${encodeURIComponent(env)}` : "";
  return api.get<EnvConfig>(
    `/clusters/${encodeURIComponent(cluster)}/envconfig${q}`,
  );
}

export function updateClusterEnvConfig(
  cluster: string,
  cfg: EnvConfig,
  env?: string,
): Promise<unknown> {
  const q = env ? `?env=${encodeURIComponent(env)}` : "";
  return api.put<unknown>(
    `/clusters/${encodeURIComponent(cluster)}/envconfig${q}`,
    cfg,
  );
}

// ── Project level ──────────────────────────────────────────────────────────────

export function getProjectEnvConfig(project: string): Promise<EnvConfig> {
  return api.get<EnvConfig>(
    `/projects/${encodeURIComponent(project)}/envconfig`,
  );
}

export function updateProjectEnvConfig(
  project: string,
  cfg: EnvConfig,
): Promise<unknown> {
  return api.put<unknown>(
    `/projects/${encodeURIComponent(project)}/envconfig`,
    cfg,
  );
}

// Project per-environment variables (overrides the project-global level for one
// environment), mirroring project-env secrets.
export function getProjectEnvEnvConfig(
  project: string,
  env: string,
): Promise<EnvConfig> {
  return api.get<EnvConfig>(
    `/projects/${encodeURIComponent(project)}/envconfig/env/${encodeURIComponent(env)}`,
  );
}

export function updateProjectEnvEnvConfig(
  project: string,
  env: string,
  cfg: EnvConfig,
): Promise<unknown> {
  return api.put<unknown>(
    `/projects/${encodeURIComponent(project)}/envconfig/env/${encodeURIComponent(env)}`,
    cfg,
  );
}

// ── App level ──────────────────────────────────────────────────────────────────

export function getAppEnvConfig(
  project: string,
  app: string,
): Promise<EnvConfig> {
  return api.get<EnvConfig>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envconfig`,
  );
}

export function updateAppEnvConfig(
  project: string,
  app: string,
  cfg: EnvConfig,
): Promise<unknown> {
  return api.put<unknown>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envconfig`,
    cfg,
  );
}

// ── App-environment level ──────────────────────────────────────────────────────

export function getAppEnvEnvConfig(
  project: string,
  app: string,
  env: string,
): Promise<EnvConfig> {
  return api.get<EnvConfig>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/envconfig`,
  );
}

export function updateAppEnvEnvConfig(
  project: string,
  app: string,
  env: string,
  cfg: EnvConfig,
): Promise<unknown> {
  return api.put<unknown>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/envconfig`,
    cfg,
  );
}

// ── Resolved view ──────────────────────────────────────────────────────────────

export function getResolvedEnvConfig(
  project: string,
  app: string,
  env: string,
): Promise<ResolvedEnvConfigResponse> {
  return api.get<ResolvedEnvConfigResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/envconfig/resolved`,
  );
}
