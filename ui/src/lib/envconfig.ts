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
}

export interface ResolvedEnvVar {
  key: string;
  value: string;
  source: string;
  isSecret: boolean;
}

export interface ResolvedEnvConfigResponse {
  vars: ResolvedEnvVar[];
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

export function getClusterEnvConfig(cluster: string): Promise<EnvConfig> {
  return api.get<EnvConfig>(
    `/clusters/${encodeURIComponent(cluster)}/envconfig`,
  );
}

export function updateClusterEnvConfig(
  cluster: string,
  cfg: EnvConfig,
): Promise<unknown> {
  return api.put<unknown>(
    `/clusters/${encodeURIComponent(cluster)}/envconfig`,
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
