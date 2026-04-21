import { api } from "./api";

export interface PrerequisitesResponse {
  ready: boolean;
  prerequisites: PrerequisiteItem[];
}

export interface PrerequisiteItem {
  name: string;
  installed: boolean;
  namespace?: string;
  message?: string;
}

export function fetchPrerequisites(): Promise<PrerequisitesResponse> {
  return api.get<PrerequisitesResponse>("/prerequisites");
}
