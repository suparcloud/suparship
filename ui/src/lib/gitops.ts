import { api } from "./api";

export interface GitOpsConfig {
  provider: string;
  repoURL: string;
  branch: string;
  subPath?: string;
  authSecretRef?: string;
  credentialExpiresAt?: string;
  argoCDRepoURL?: string;
  kargoGitRepoURL?: string;
  initializeRepo: boolean;
  initialized: boolean;
  github?: { appId?: string; installationId?: string };
  bitbucket?: { workspace?: string };
}

export interface GitOpsConfigResponse {
  configured: boolean;
  config?: GitOpsConfig;
}

export interface TestConnectionRequest {
  repoURL: string;
  username?: string;
  password?: string;
}

export interface TestConnectionResponse {
  success: boolean;
  message: string;
  durationMs: number;
}

export function fetchGitOpsConfig(): Promise<GitOpsConfigResponse> {
  return api.get<GitOpsConfigResponse>("/gitops/config");
}

export function updateGitOpsConfig(
  config: GitOpsConfig,
): Promise<GitOpsConfigResponse> {
  return api.put<GitOpsConfigResponse>("/gitops/config", config);
}

export function testGitOpsConnection(
  req: TestConnectionRequest,
): Promise<TestConnectionResponse> {
  return api.post<TestConnectionResponse>("/gitops/test-connection", req);
}
