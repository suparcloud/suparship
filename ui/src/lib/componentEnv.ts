import type { ComponentEnvOverride, ComponentEnvVar } from "../types";

// mergeComponentEnvVars layers an environment's entries over the component's
// app-wide list by variable name (the env wins), keeping base order and
// appending env-only entries — the same rule the server applies at publish.
export function mergeComponentEnvVars(
  base: ComponentEnvVar[],
  override: ComponentEnvVar[],
): ComponentEnvVar[] {
  if (override.length === 0) return [...base];
  const byName = new Map(override.map((e) => [e.name, e] as const));
  const seen = new Set<string>();
  const out: ComponentEnvVar[] = [];
  for (const e of base) {
    seen.add(e.name);
    out.push(byName.get(e.name) ?? e);
  }
  for (const e of override) {
    if (seen.has(e.name)) continue;
    seen.add(e.name);
    out.push(e);
  }
  return out;
}

// effectiveComponentEnvVars returns what a component renders with in `env`:
// its app-wide posture + list with that env's override layered on top. With
// no env (nothing selected) it is the app-wide settings.
export function effectiveComponentEnvVars(
  base: { inheritAppVars?: boolean; envVars?: ComponentEnvVar[] },
  envEnvVars: Record<string, ComponentEnvOverride> | undefined,
  env: string | null,
): { inheritAppVars: boolean; envVars: ComponentEnvVar[] } {
  const inheritBase = base.inheritAppVars !== false;
  const ov = env ? envEnvVars?.[env] : undefined;
  if (!ov) return { inheritAppVars: inheritBase, envVars: [...(base.envVars ?? [])] };
  return {
    inheritAppVars: ov.inheritAppVars ?? inheritBase,
    envVars: mergeComponentEnvVars(base.envVars ?? [], ov.envVars ?? []),
  };
}
