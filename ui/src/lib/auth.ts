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
