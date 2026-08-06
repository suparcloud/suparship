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

export interface ExternalSecretSettings {
  // refreshInterval is how often ESO re-pulls secret values (Go duration, e.g.
  // "1m", "30s", "1h"). Empty defaults to "1m" server-side.
  refreshInterval?: string;
}

// HCVaultConfig mirrors the server's HashiCorp Vault backend config. Unlike
// 1Password there is NO per-scope vault registration: containers are derived
// paths inside one KV v2 mount, so setup is just the address, the mount, and
// one sealed token per workload cluster.
export interface HCVaultConfig {
  address?: string;
  mount?: string;
  namespace?: string;
  caCert?: string;
  clusterTokens?: ClusterTokenRef[];
}

export interface SecretBackendConfig {
  type: string;
  onePassword?: OnePasswordConfig;
  vault?: HCVaultConfig;
  externalSecrets?: ExternalSecretSettings;
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

// saveVaultToken saves suparship's HashiCorp Vault WRITE token (the data
// plane it writes items with) and validates it against the configured Vault
// address + mount. Set the address first.
export function saveVaultToken(token: string): Promise<SATokenResponse> {
  return api.post<SATokenResponse>("/org/secret-backend/vault-token", { token });
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

// unregisterEnvVault removes an env's vault binding. Editing a binding is just
// registerEnvVault again with a different vault.
export function unregisterEnvVault(env: string): Promise<void> {
  return api.del(`/org/secret-backend/vaults/env/${encodeURIComponent(env)}`);
}

// ── Per-cluster credential (1Password Connect token / Vault token) ──────────
// One credential per cluster: for 1Password a Connect token with access to
// every vault the cluster reads; for Vault a read token for the suparship
// mount. suparship stashes it, seals it, and publishes the cluster's single
// unified ClusterSecretStore. `token` is the backend-neutral field;
// `connectToken` is its pre-Vault alias.

export function setClusterConnectToken(
  cluster: string,
  body: { token?: string; connectToken?: string; connectEndpoint?: string },
): Promise<void> {
  return api.post(
    `/org/secret-backend/clusters/${encodeURIComponent(cluster)}/connect-token`,
    body,
  );
}

// ── Vault least-privilege policies ──────────────────────────────────────────
// For the Vault backend a suparship "vault" is a path prefix in one KV mount, so
// a path-scoped POLICY is what isolates one env's secrets from another's. These
// are computed, never applied: suparship holds a write token for the KV mount,
// not the sys/policy rights that writing policies and minting tokens need.

export interface VaultPolicy {
  name: string;
  /** "global", or the environment name. Absent on the write policy. */
  env?: string;
  hcl: string;
}

export interface VaultClusterPolicy {
  cluster: string;
  /** The envs bound to this cluster — why it is entitled to those policies. */
  boundEnvs: string[];
  policies: string[];
  /** Ready-to-run `vault token create`, one -policy flag per entitled scope. */
  tokenCommand: string;
}

export interface VaultPoliciesResponse {
  mount: string;
  /** suparship's own control-plane policy (mount-wide by design). */
  writePolicy: VaultPolicy;
  /** Global read policy plus one per environment. Clusters compose these. */
  readPolicies: VaultPolicy[];
  clusters: VaultClusterPolicy[];
}

export function getVaultPolicies(): Promise<VaultPoliciesResponse> {
  return api.get<VaultPoliciesResponse>("/org/secret-backend/vault-policies");
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

// ── Project-scope shared secrets (shared by every app in the project) ────────

const projectBase = (project: string) =>
  `/projects/${encodeURIComponent(project)}/secrets`;

export function listProjectGlobalSecretKeys(project: string): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(`${projectBase(project)}/global`);
}
export function upsertProjectGlobalSecrets(project: string, entries: Record<string, string>): Promise<void> {
  return api.post(`${projectBase(project)}/global`, { entries });
}
export function deleteProjectGlobalSecretKey(project: string, key: string): Promise<void> {
  return api.del(`${projectBase(project)}/global/${encodeURIComponent(key)}`);
}

export function listProjectEnvSecretKeys(project: string, env: string): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(`${projectBase(project)}/env/${encodeURIComponent(env)}`);
}
export function upsertProjectEnvSecrets(project: string, env: string, entries: Record<string, string>): Promise<void> {
  return api.post(`${projectBase(project)}/env/${encodeURIComponent(env)}`, { entries });
}
export function deleteProjectEnvSecretKey(project: string, env: string, key: string): Promise<void> {
  return api.del(`${projectBase(project)}/env/${encodeURIComponent(env)}/${encodeURIComponent(key)}`);
}

// ── Stack-scope shared secrets (shared by every app in the stack) ────────────

const stackBase = (project: string, stack: string) =>
  `/projects/${encodeURIComponent(project)}/stacks/${encodeURIComponent(stack)}/secrets`;

export function listStackGlobalSecretKeys(project: string, stack: string): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(`${stackBase(project, stack)}/global`);
}
export function upsertStackGlobalSecrets(project: string, stack: string, entries: Record<string, string>): Promise<void> {
  return api.post(`${stackBase(project, stack)}/global`, { entries });
}
export function deleteStackGlobalSecretKey(project: string, stack: string, key: string): Promise<void> {
  return api.del(`${stackBase(project, stack)}/global/${encodeURIComponent(key)}`);
}

export function listStackEnvSecretKeys(project: string, stack: string, env: string): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(`${stackBase(project, stack)}/env/${encodeURIComponent(env)}`);
}
export function upsertStackEnvSecrets(project: string, stack: string, env: string, entries: Record<string, string>): Promise<void> {
  return api.post(`${stackBase(project, stack)}/env/${encodeURIComponent(env)}`, { entries });
}
export function deleteStackEnvSecretKey(project: string, stack: string, env: string, key: string): Promise<void> {
  return api.del(`${stackBase(project, stack)}/env/${encodeURIComponent(env)}/${encodeURIComponent(key)}`);
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

// Preview band: app secrets applied to every preview, on top of base env {env}.
// Stored as the <app>-env-preview item inside the {env} vault — no preview vault.
export function listAppPreviewSecretKeys(
  project: string,
  app: string,
  env: string,
): Promise<SecretKeysResponse> {
  return api.get<SecretKeysResponse>(
    `${appBase(project, app)}/env/${encodeURIComponent(env)}/preview`,
  );
}
export function upsertAppPreviewSecrets(
  project: string,
  app: string,
  env: string,
  entries: Record<string, string>,
): Promise<void> {
  return api.post(
    `${appBase(project, app)}/env/${encodeURIComponent(env)}/preview`,
    { entries },
  );
}
export function deleteAppPreviewSecretKey(
  project: string,
  app: string,
  env: string,
  key: string,
): Promise<void> {
  return api.del(
    `${appBase(project, app)}/env/${encodeURIComponent(env)}/preview/${encodeURIComponent(key)}`,
  );
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
