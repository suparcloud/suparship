import { api } from "./api";

export interface GitOpsConfig {
  provider: string;
  repoURL: string;
  branch: string;
  subPath?: string;
  credentialExpiresAt?: string;
  argoCDRepoURL?: string;
  kargoGitRepoURL?: string;
  initializeRepo: boolean;
  initialized: boolean;
  github?: { appId?: string; installationId?: string };
  bitbucket?: { workspace?: string };
}

/** Plaintext credentials submitted from the UI — never stored in Git. */
export interface GitOpsCredentials {
  /** Personal access token for GitHub, GitLab, Gitea. */
  token?: string;
  /** Username for Bitbucket and generic providers. */
  username?: string;
  /** Password or app-password for Bitbucket / generic providers. */
  password?: string;
}

export interface GitOpsConfigResponse {
  configured: boolean;
  /** True when a credential Secret already exists in the cluster. */
  credentialsSet: boolean;
  config?: GitOpsConfig;
  /**
   * Non-empty when post-save ArgoCD registration or publisher hot-reload
   * encountered a non-fatal error. Config was saved successfully.
   */
  activationWarning?: string;
}

export interface UpdateGitOpsConfigRequest extends GitOpsConfig {
  credentials?: GitOpsCredentials;
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
  request: UpdateGitOpsConfigRequest,
): Promise<GitOpsConfigResponse> {
  return api.put<GitOpsConfigResponse>("/gitops/config", request);
}

export function testGitOpsConnection(
  req: TestConnectionRequest,
): Promise<TestConnectionResponse> {
  return api.post<TestConnectionResponse>("/gitops/test-connection", req);
}
