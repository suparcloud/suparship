import { api } from "./api";

// RoutingProfile mirrors the backend per-ExposeMode routing config.
export interface RoutingProfile {
  ingressClassName: string;
  clusterIssuer?: string;
  baseDomain?: string;
}

// RoutingProfiles is keyed by ExposeMode ("internal" | "external").
export type RoutingProfiles = Record<string, RoutingProfile>;

export interface Cluster {
  name: string;
  displayName?: string;
  apiServer: string;
  /** Namespace where External Secrets Operator is installed on this cluster.
   *  Defaults to "external-secrets" when omitted. */
  esoNamespace?: string;
  status?: string;
  /** Default ingress DNS zone for this cluster (multi-cloud). */
  baseDomain?: string;
  /** Per-ExposeMode routing overrides for apps on this cluster. */
  routingProfiles?: RoutingProfiles;
}

interface ClustersResponse {
  clusters: Cluster[];
}

export async function listClusters(): Promise<Cluster[]> {
  const res = await api.get<ClustersResponse>("/clusters");
  return res.clusters ?? [];
}

export async function getCluster(name: string): Promise<Cluster> {
  return api.get<Cluster>(`/clusters/${name}`);
}

export interface RegisterClusterRequest {
  name: string;
  displayName?: string;
  apiServer: string;
  /** Base64-encoded kubeconfig bytes */
  kubeconfig: string;
  /** Namespace where External Secrets Operator is installed on this cluster.
   *  Leave blank to use the default ("external-secrets"). */
  esoNamespace?: string;
  baseDomain?: string;
  routingProfiles?: RoutingProfiles;
}

export async function registerCluster(
  req: RegisterClusterRequest,
): Promise<Cluster> {
  return api.post<Cluster>("/clusters", req);
}

// UpdateClusterRequest edits cluster metadata (routing) without re-registering.
export interface UpdateClusterRequest {
  displayName?: string;
  baseDomain?: string;
  esoNamespace?: string;
  routingProfiles?: RoutingProfiles;
}

export async function updateCluster(
  name: string,
  req: UpdateClusterRequest,
): Promise<Cluster> {
  return api.put<Cluster>(`/clusters/${encodeURIComponent(name)}`, req);
}

export async function removeCluster(name: string): Promise<void> {
  return api.del(`/clusters/${name}`);
}

// ── Import from ArgoCD ────────────────────────────────────────────────────────

// ArgoCDClusterCandidate is a cluster ArgoCD already has registered, surfaced as
// an import candidate.
export interface ArgoCDClusterCandidate {
  name: string;
  server: string;
  /** "token" | "clientCert" | "basic" | "exec" | "unknown" */
  authType: string;
  importable: boolean;
  reason?: string;
  alreadyRegistered: boolean;
}

export async function listArgoCDClusters(): Promise<ArgoCDClusterCandidate[]> {
  const res = await api.get<{ clusters: ArgoCDClusterCandidate[] }>(
    "/clusters/argocd",
  );
  return res.clusters ?? [];
}

export interface ImportSkip {
  name: string;
  reason: string;
}

export interface ImportClustersResult {
  imported: Cluster[];
  skipped: ImportSkip[];
}

export async function importClusters(
  names: string[],
): Promise<ImportClustersResult> {
  return api.post<ImportClustersResult>("/clusters/import", { names });
}

export interface SealingCertRefreshResponse {
  cluster: string;
  cached: boolean;
  message?: string;
}

/** Fetches the sealed-secrets controller certificate from the target cluster
 *  and caches it, replacing any stale or incorrect cached value. */
export async function refreshSealingCert(
  clusterName: string,
): Promise<SealingCertRefreshResponse> {
  return api.post<SealingCertRefreshResponse>(
    `/clusters/${clusterName}/sealing-cert/refresh`,
    {},
  );
}
