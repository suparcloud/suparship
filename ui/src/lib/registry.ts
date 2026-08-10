import { api } from "./api";

export interface RegistryConfig {
  enabled: boolean;
  url: string;
  username?: string;
  authSecretRef?: string;
  credentialExpiresAt?: string;
  environments?: string[];
  /** Disable TLS verification for Kargo Warehouse subscriptions — plain-HTTP
   *  registries only (e.g. the local kind registry). MUST round-trip through
   *  the form: the PUT replaces the whole config, so omitting it here would
   *  silently wipe the flag on every save. */
  insecure?: boolean;
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
