import { api } from "./api";

// StuckApp mirrors server.StuckApp — an ArgoCD Application wedged in Terminating.
export interface StuckApp {
  name: string;
  project?: string;
  deletionTimestamp?: string;
  finalizers?: string[];
  reason: string;
}

interface StuckAppsResponse {
  stuckApps: StuckApp[];
}

// listStuckApps returns Applications stuck in Terminating. org_admin only;
// non-admins get 403 (callers should treat that as "nothing to show").
export function listStuckApps(): Promise<StuckAppsResponse> {
  return api.get<StuckAppsResponse>("/platform/stuck-apps");
}

// unstickApp removes ArgoCD's cascade-delete finalizer so a wedged deletion
// completes. org_admin only.
export function unstickApp(name: string): Promise<{ application: string; status: string }> {
  return api.post(`/platform/stuck-apps/${encodeURIComponent(name)}/unstick`);
}
