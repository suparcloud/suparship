import { api } from "./api";

// ── Types ──────────────────────────────────────────────────────────────────────

export interface SecretKeyEntry {
  key: string;
}

export interface SecretKeysResponse {
  keys: SecretKeyEntry[];
  secretName: string;
}

// VaultRef describes one registered 1Password vault (global / an env).
// Key is the env name, empty for the global vault. Cluster overrides are
// items inside the env vault — clusters have no vault of their own.
// Registration records the vault ID only; Connect tokens are per cluster
// (see ClusterTokenRef).
export interface VaultRef {
  key?: string;
  vaultId: string;
  vaultName: string;
  provisioned?: boolean;
  lastProvisioned?: string;
  lastError?: string;
}

// ClusterTokenRef tracks one cluster's single Connect token + unified store
// seal state. The token covers every vault the cluster reads (global + its
// bound env vaults).
export interface ClusterTokenRef {
  cluster: string;
  connectEndpoint?: string;
  sealed?: boolean;
  lastSealed?: string;
  lastError?: string;
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
  globalVault?: VaultRef;
  envVaults?: VaultRef[];
  clusterTokens?: ClusterTokenRef[];
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

export interface ResolvedSecretEntry {
  key: string;
  source: string; // "global" | "env" | "cluster"
  tier?: string; // "shared" | "app"
}

export interface ResolvedSecretsResponse {
  secrets: ResolvedSecretEntry[];
}

// ── Backend state ─────────────────────────────────────────────────────────────

export function getSecretsBackend(): Promise<SecretBackendConfig> {
  return api.get<SecretBackendConfig>("/org/secret-backend");
}

export function updateSecretsBackend(
  cfg: Partial<SecretBackendConfig>,
): Promise<SecretBackendConfig> {
  return api.put<SecretBackendConfig>("/org/secret-backend", cfg);
}

export function saveSAToken(token: string): Promise<SATokenResponse> {
  return api.post<SATokenResponse>("/org/secret-backend/sa-token", { token });
}

export function listVaults(): Promise<VaultInfo[]> {
  return api.get<VaultInfo[]>("/org/secret-backend/vaults");
}

// ── Global vault picker ─────────────────────────────────────────────────────

export interface SetGlobalVaultResponse {
  vaultId: string;
  vaultName: string;
}

export function setGlobalVault(
  vaultId: string,
  vaultName?: string,
): Promise<SetGlobalVaultResponse> {
  return api.put<SetGlobalVaultResponse>("/org/secret-backend/global-vault", {
    vaultId,
    vaultName: vaultName || "",
  });
}

// ── Env vault registration (1Password) ──────────────────────────────────────
// Records which vault backs the env (ID only). Connect tokens are per cluster
// (setClusterConnectToken). Cluster overrides are items inside the env vault,
// so clusters need no vault registration of their own.

export function registerEnvVault(
  env: string,
  body: { vaultId: string; vaultName?: string },
): Promise<void> {
  return api.post(`/org/secret-backend/vaults/env/${encodeURIComponent(env)}`, body);
}

// ── Per-cluster Connect token (1Password) ───────────────────────────────────
// One token per cluster, with access to every vault the cluster reads (the
// global vault + its bound env vaults). suparShip stashes it, seals it, and
// publishes the cluster's single unified ClusterSecretStore.

export function setClusterConnectToken(
  cluster: string,
  body: { connectToken: string; connectEndpoint?: string },
): Promise<void> {
  return api.post(
    `/org/secret-backend/clusters/${encodeURIComponent(cluster)}/connect-token`,
    body,
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

// ── Shared-tier secrets (org-admin) ──────────────────────────────────────────

export function listSharedGlobalSecretKeys(): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>("/org/secrets/global");
}
export function upsertSharedGlobalSecrets(entries: Record<string, string>): Promise<void> {
  return api.post("/org/secrets/global", { entries });
}
export function deleteSharedGlobalSecretKey(key: string): Promise<void> {
  return api.del(`/org/secrets/global/${encodeURIComponent(key)}`);
}

export function listSharedEnvSecretKeys(env: string): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(`/org/secrets/env/${encodeURIComponent(env)}`);
}
export function upsertSharedEnvSecrets(env: string, entries: Record<string, string>): Promise<void> {
  return api.post(`/org/secrets/env/${encodeURIComponent(env)}`, { entries });
}
export function deleteSharedEnvSecretKey(env: string, key: string): Promise<void> {
  return api.del(`/org/secrets/env/${encodeURIComponent(env)}/${encodeURIComponent(key)}`);
}

// Cluster overrides are per-(env, cluster): items live in the env vault.
export function listSharedClusterSecretKeys(env: string, cluster: string): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(
    `/org/secrets/env/${encodeURIComponent(env)}/cluster/${encodeURIComponent(cluster)}`,
  );
}
export function upsertSharedClusterSecrets(
  env: string,
  cluster: string,
  entries: Record<string, string>,
): Promise<void> {
  return api.post(`/org/secrets/env/${encodeURIComponent(env)}/cluster/${encodeURIComponent(cluster)}`, {
    entries,
  });
}
export function deleteSharedClusterSecretKey(env: string, cluster: string, key: string): Promise<void> {
  return api.del(
    `/org/secrets/env/${encodeURIComponent(env)}/cluster/${encodeURIComponent(cluster)}/${encodeURIComponent(key)}`,
  );
}

// ── App-tier secrets (project devs) ──────────────────────────────────────────

const appBase = (project: string, app: string) =>
  `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(app)}/secrets`;

export function listAppGlobalSecretKeys(project: string, app: string): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(`${appBase(project, app)}/global`);
}
export function upsertAppGlobalSecrets(project: string, app: string, entries: Record<string, string>): Promise<void> {
  return api.post(`${appBase(project, app)}/global`, { entries });
}
export function deleteAppGlobalSecretKey(project: string, app: string, key: string): Promise<void> {
  return api.del(`${appBase(project, app)}/global/${encodeURIComponent(key)}`);
}

export function listAppEnvSecretKeys(project: string, app: string, env: string): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(`${appBase(project, app)}/env/${encodeURIComponent(env)}`);
}
export function upsertAppEnvSecrets(project: string, app: string, env: string, entries: Record<string, string>): Promise<void> {
  return api.post(`${appBase(project, app)}/env/${encodeURIComponent(env)}`, { entries });
}
export function deleteAppEnvSecretKey(project: string, app: string, env: string, key: string): Promise<void> {
  return api.del(`${appBase(project, app)}/env/${encodeURIComponent(env)}/${encodeURIComponent(key)}`);
}

// Cluster overrides are per-(env, cluster): items live in the env vault.
export function listAppClusterSecretKeys(
  project: string,
  app: string,
  env: string,
  cluster: string,
): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(
    `${appBase(project, app)}/env/${encodeURIComponent(env)}/cluster/${encodeURIComponent(cluster)}`,
  );
}
export function upsertAppClusterSecrets(
  project: string,
  app: string,
  env: string,
  cluster: string,
  entries: Record<string, string>,
): Promise<void> {
  return api.post(
    `${appBase(project, app)}/env/${encodeURIComponent(env)}/cluster/${encodeURIComponent(cluster)}`,
    { entries },
  );
}
export function deleteAppClusterSecretKey(
  project: string,
  app: string,
  env: string,
  cluster: string,
  key: string,
): Promise<void> {
  return api.del(
    `${appBase(project, app)}/env/${encodeURIComponent(env)}/cluster/${encodeURIComponent(cluster)}/${encodeURIComponent(key)}`,
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
