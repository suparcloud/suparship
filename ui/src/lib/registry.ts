import { api } from "./api";

export interface RegistryConfig {
  enabled: boolean;
  url: string;
  username?: string;
  authSecretRef?: string;
  credentialExpiresAt?: string;
  environments?: string[];
}

export interface RegistryConfigResponse {
  configured: boolean;
  config: RegistryConfig;
}

export function fetchRegistryConfig(): Promise<RegistryConfigResponse> {
  return api.get<RegistryConfigResponse>("/registry/config");
}

export function updateRegistryConfig(
  config: RegistryConfig,
): Promise<RegistryConfigResponse> {
  return api.put<RegistryConfigResponse>("/registry/config", config);
}
