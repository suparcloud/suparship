import { api } from "./api";

// ── Types ──────────────────────────────────────────────────────────────────────

export interface SecretKeyEntry {
  key: string;
}

export interface SecretKeysResponse {
  keys: SecretKeyEntry[];
  secretName: string;
}

export interface EnvBinding {
  env: string;
  vaultId: string;
  vaultName: string;
  provisioned: boolean;
  lastProvisioned?: string;
  lastError?: string;
  clusterSecretStoreName?: string;
  connectEndpoint?: string;
}

export interface ConnectStatus {
  endpoint: string;
  installed: boolean;
  healthy: boolean;
  lastProbe?: string;
}

export interface OnePasswordConfig {
  groupName: string;
  connect: ConnectStatus;
  bindings: EnvBinding[];
}

export interface SecretBackendConfig {
  type: string;
  onePassword?: OnePasswordConfig;
}

export interface SATokenResponse {
  valid: boolean;
  vaultCount?: number;
  error?: string;
}

export interface VaultInfo {
  id: string;
  title: string;
}

export interface BindingResponse {
  env: string;
  vaultId: string;
  vaultName: string;
  clusterSecretStoreName: string;
  provisioned: boolean;
  rotated: boolean;
  error?: string;
}

export interface ResolvedSecretEntry {
  key: string;
  source: string;
}

export interface ResolvedSecretsResponse {
  secrets: ResolvedSecretEntry[];
}

// ── Backend state (GET) ─────────────────────────────────────────────────────────

export function getSecretsBackend(): Promise<SecretBackendConfig> {
  return api.get<SecretBackendConfig>("/org/secret-backend");
}

export function updateSecretsBackend(
  cfg: Partial<SecretBackendConfig>,
): Promise<SecretBackendConfig> {
  return api.put<SecretBackendConfig>("/org/secret-backend", cfg);
}

// ── SA Token (POST) ──────────────────────────────────────────────────────────────

export function saveSAToken(token: string): Promise<SATokenResponse> {
  return api.post<SATokenResponse>("/org/secret-backend/sa-token", { token });
}

// ── Vault listing ───────────────────────────────────────────────────────────────

export function listVaults(): Promise<VaultInfo[]> {
  return api.get<VaultInfo[]>("/org/secret-backend/vaults");
}

// ── Bindings (Add / Remove) ─────────────────────────────────────────────────────

export function addBinding(
  env: string,
  vaultId: string,
  connectToken: string,
  vaultName?: string,
  connectEndpoint?: string,
): Promise<BindingResponse> {
  return api.post<BindingResponse>("/org/secret-backend/bindings", {
    env,
    vaultId,
    vaultName: vaultName || "",
    connectToken,
    connectEndpoint: connectEndpoint || "",
  });
}

export function removeBinding(env: string): Promise<void> {
  return api.del(
    `/org/secret-backend/bindings/${encodeURIComponent(env)}`,
  );
}

// ── Secret sync ────────────────────────────────────────────────────────────────

export function syncSecrets(
  project: string,
  app: string,
): Promise<{ status: string; syncToken: string }> {
  return api.post(
    `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/secrets/sync`,
    {},
  );
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

// ── Cluster-level secrets CRUD ─────────────────────────────────────────────────

export function listClusterSecretKeys(
  cluster: string,
): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(
    `/clusters/${encodeURIComponent(cluster)}/secrets`,
  );
}

export function upsertClusterSecrets(
  cluster: string,
  entries: Record<string, string>,
): Promise<void> {
  return api.post(
    `/clusters/${encodeURIComponent(cluster)}/secrets`,
    { entries },
  );
}

export function deleteClusterSecretKey(
  cluster: string,
  key: string,
): Promise<void> {
  return api.del(
    `/clusters/${encodeURIComponent(cluster)}/secrets/${encodeURIComponent(key)}`,
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
