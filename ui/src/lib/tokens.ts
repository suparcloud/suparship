import { api } from "./api";
import type {
  CreatedToken,
  CreateTokenRequest,
  TokensResponse,
} from "../types";

const base = (project: string) =>
  `/projects/${encodeURIComponent(project)}/tokens`;

// listProjectTokens returns metadata for every API token scoped to the project
// (never the secret). Requires project_admin.
export function listProjectTokens(project: string): Promise<TokensResponse> {
  return api.get<TokensResponse>(base(project));
}

// createProjectToken mints a token and returns its one-time plaintext value
// (CreatedToken.token) plus metadata. Requires project_admin.
export function createProjectToken(
  project: string,
  req: CreateTokenRequest,
): Promise<CreatedToken> {
  return api.post<CreatedToken>(base(project), req);
}

// deleteProjectToken revokes a token by id. Requires project_admin.
export function deleteProjectToken(
  project: string,
  id: string,
): Promise<void> {
  return api.del(`${base(project)}/${encodeURIComponent(id)}`);
}
