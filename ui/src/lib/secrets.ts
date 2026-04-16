import { api } from "./api";

// ── Types ──────────────────────────────────────────────────────────────────────

export interface SecretKeyEntry {
  key: string;
}

export interface SecretKeysResponse {
  keys: SecretKeyEntry[];
  secretName: string;
}

export interface SecretBackendConfig {
  type: string;
}

export interface ResolvedSecretEntry {
  key: string;
  source: string;
}

export interface ResolvedSecretsResponse {
  secrets: ResolvedSecretEntry[];
}

// ── Org backend config ─────────────────────────────────────────────────────────

export function getSecretsBackend(): Promise<SecretBackendConfig> {
  return api.get<SecretBackendConfig>("/org/secrets-backend");
}

export function updateSecretsBackend(
  cfg: SecretBackendConfig,
): Promise<SecretBackendConfig> {
  return api.put<SecretBackendConfig>("/org/secrets-backend", cfg);
}

// ── Org-level secrets CRUD ─────────────────────────────────────────────────────

export function listOrgSecretKeys(): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>("/org/secrets");
}

export function upsertOrgSecrets(entries: Record<string, string>): Promise<void> {
  return api.post("/org/secrets", { entries });
}

export function deleteOrgSecretKey(key: string): Promise<void> {
  return api.del(`/org/secrets/${encodeURIComponent(key)}`);
}

// ── Env-type-level secrets CRUD ────────────────────────────────────────────────

export function listEnvTypeSecretKeys(
  envType: string,
): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(
    `/org/secrets/envtype/${encodeURIComponent(envType)}`,
  );
}

export function upsertEnvTypeSecrets(
  envType: string,
  entries: Record<string, string>,
): Promise<void> {
  return api.post(
    `/org/secrets/envtype/${encodeURIComponent(envType)}`,
    { entries },
  );
}

export function deleteEnvTypeSecretKey(
  envType: string,
  key: string,
): Promise<void> {
  return api.del(
    `/org/secrets/envtype/${encodeURIComponent(envType)}/${encodeURIComponent(key)}`,
  );
}

// ── Project-level secrets CRUD ─────────────────────────────────────────────────

export function listProjectSecretKeys(
  project: string,
): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(
    `/projects/${encodeURIComponent(project)}/secrets`,
  );
}

export function upsertProjectSecrets(
  project: string,
  entries: Record<string, string>,
): Promise<void> {
  return api.post(
    `/projects/${encodeURIComponent(project)}/secrets`,
    { entries },
  );
}

export function deleteProjectSecretKey(
  project: string,
  key: string,
): Promise<void> {
  return api.del(
    `/projects/${encodeURIComponent(project)}/secrets/${encodeURIComponent(key)}`,
  );
}

// ── App-level secrets CRUD ─────────────────────────────────────────────────────

export function listAppSecretKeys(
  project: string,
  app: string,
): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/secrets`,
  );
}

export function upsertAppSecrets(
  project: string,
  app: string,
  entries: Record<string, string>,
): Promise<void> {
  return api.post(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/secrets`,
    { entries },
  );
}

export function deleteAppSecretKey(
  project: string,
  app: string,
  key: string,
): Promise<void> {
  return api.del(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/secrets/${encodeURIComponent(key)}`,
  );
}

// ── App-env secrets CRUD ───────────────────────────────────────────────────────

export function listSecretKeys(
  project: string,
  app: string,
  env: string,
): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/secrets`,
  );
}

export function upsertSecrets(
  project: string,
  app: string,
  env: string,
  entries: Record<string, string>,
): Promise<void> {
  return api.post(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/secrets`,
    { entries },
  );
}

export function deleteSecretKey(
  project: string,
  app: string,
  env: string,
  key: string,
): Promise<void> {
  return api.del(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/secrets/${encodeURIComponent(key)}`,
  );
}

// ── Resolved secrets ───────────────────────────────────────────────────────────

export function getResolvedSecrets(
  project: string,
  app: string,
  env: string,
): Promise<ResolvedSecretsResponse> {
  return api.get<ResolvedSecretsResponse>(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/envs/${encodeURIComponent(env)}/secrets/resolved`,
  );
}
