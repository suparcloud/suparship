import { api } from "./api";

export interface Cluster {
  name: string;
  displayName?: string;
  apiServer: string;
  status?: string;
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
}

export async function registerCluster(
  req: RegisterClusterRequest,
): Promise<Cluster> {
  return api.post<Cluster>("/clusters", req);
}

export async function removeCluster(name: string): Promise<void> {
  return api.del(`/clusters/${name}`);
}
