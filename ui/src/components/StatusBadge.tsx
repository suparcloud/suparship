import type { AppEnvironmentSummary } from "../types";

// Shared deployment-status badge + phase helpers, used by the Dashboard,
// Project detail, Stack detail, and App detail pages so status presentation is
// consistent everywhere.

interface StatusStyle {
  dot: string;
  bg: string;
  label: string;
}

export const fallbackStatus: StatusStyle = {
  dot: "bg-gray-300",
  bg: "bg-gray-100 text-gray-500",
  label: "Unknown",
};

export const statusStyles: Record<string, StatusStyle> = {
  healthy: { dot: "bg-emerald-500", bg: "bg-emerald-50 text-emerald-700", label: "Healthy" },
  degraded: { dot: "bg-amber-500", bg: "bg-amber-50 text-amber-700", label: "Degraded" },
  progressing: { dot: "bg-blue-500", bg: "bg-blue-50 text-blue-700", label: "Syncing" },
  not_deployed: { dot: "bg-gray-300", bg: "bg-gray-100 text-gray-500", label: "Not deployed" },
  // Deployed but scaled to zero (e.g. KEDA idle off-hours) — distinct from
  // "not deployed": the workload exists, nothing is running right now.
  idle: { dot: "bg-indigo-400", bg: "bg-indigo-50 text-indigo-700", label: "Idle" },
};

export function StatusBadge({
  status,
  size = "sm",
}: {
  status: string;
  size?: "sm" | "lg";
}) {
  const cfg = statusStyles[status] ?? fallbackStatus;
  const cls =
    size === "lg"
      ? "px-3 py-1 text-sm font-medium"
      : "px-2.5 py-0.5 text-xs font-medium";
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full ${cls} ${cfg.bg}`}>
      <span
        className={`rounded-full ${cfg.dot} ${size === "lg" ? "h-2 w-2" : "h-1.5 w-1.5"}`}
      />
      {cfg.label}
    </span>
  );
}

// overallPhase aggregates per-env phases into a single phase for compact views.
// Mirrors the backend summaryPhase. "up" = running or intentionally idle.
export function overallPhase(envs: AppEnvironmentSummary[]): string {
  const active = envs.filter((e) => e.envType !== "preview");
  if (active.length === 0) return "not_deployed";
  const phases = active.map((e) => e.status.phase);
  const up = (p: string) => p === "healthy" || p === "idle";
  if (phases.every(up)) {
    return phases.every((p) => p === "idle") ? "idle" : "healthy";
  }
  if (phases.some((p) => p === "degraded")) return "degraded";
  if (phases.some((p) => p === "progressing")) return "progressing";
  if (phases.every((p) => p === "not_deployed")) return "not_deployed";
  if (phases.some(up)) return "healthy";
  return "degraded";
}
