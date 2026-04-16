import { api } from "./api";

export interface ComponentStatus {
  installed: boolean;
  namespace?: string;
  version?: string;
  healthy: boolean;
}

export interface InClusterInfo {
  apiServer: string;
  clusterName?: string;
}

export interface PrerequisitesResponse {
  argocd: ComponentStatus;
  ingressController: ComponentStatus;
  eso: ComponentStatus;
  inCluster: InClusterInfo;
  detectedDomain?: string;
}

export function fetchPrerequisites(): Promise<PrerequisitesResponse> {
  return api.get<PrerequisitesResponse>("/prerequisites");
}
