import { api } from "./api";

export interface CredentialStatus {
  name: string;
  secretRef?: string;
  status: "healthy" | "warning" | "expired" | "missing" | "not_configured";
  message?: string;
  expiresAt?: string;
  daysUntilExpiry?: number;
}

export interface CredentialHealthResponse {
  credentials: CredentialStatus[];
  overallStatus: string;
}

export function fetchCredentialHealth(): Promise<CredentialHealthResponse> {
  return api.get<CredentialHealthResponse>("/credentials/health");
}
