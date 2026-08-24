import { api } from "./api";
import type { AuthUser } from "../types";

export function login(username: string, password: string): Promise<AuthUser> {
  return api.post<AuthUser>("/auth/login", { username, password });
}

export function logout(): Promise<void> {
  return api.post<void>("/auth/logout");
}

export function fetchMe(): Promise<AuthUser> {
  return api.get<AuthUser>("/auth/me");
}

export interface InviteInfo {
  valid: boolean;
  username?: string;
}

/** Validates an invite link without consuming it (set-password page greeting). */
export function getInvite(token: string): Promise<InviteInfo> {
  return api.get<InviteInfo>(`/auth/invite/${encodeURIComponent(token)}`);
}

/** Redeems a one-time invite: sets the password and signs the user in. */
export function acceptInvite(token: string, password: string): Promise<AuthUser> {
  return api.post<AuthUser>("/auth/invite/accept", { token, password });
}
