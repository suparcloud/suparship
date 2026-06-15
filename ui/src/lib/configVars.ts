import { api } from "./api";

// PlatformToken is a static {platform.*} variable available in app config.
export interface PlatformToken {
  token: string;
  label: string;
  group: string;
  description?: string;
}

// VarToken is a non-secret {vars.NAME} platform env var, scope-labelled.
export interface VarToken {
  token: string;
  name: string;
  scope: string;
}

export interface ConfigVariables {
  platform: PlatformToken[];
  vars: VarToken[];
}

// listConfigVariables fetches the variable catalog for a project's apps — the
// platform identity/routing tokens plus the non-secret env-var tokens. Used by
// the "Insert variable" picker. Secrets are never returned.
export async function listConfigVariables(
  project: string,
): Promise<ConfigVariables> {
  const res = await api.get<ConfigVariables>(
    `/projects/${encodeURIComponent(project)}/config-variables`,
  );
  return { platform: res.platform ?? [], vars: res.vars ?? [] };
}
