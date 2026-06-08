import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";

import { fetchAppLogs, getApp, getAppDeploymentHistory, getAppEnvironment, getKargoAppPipeline, getKargoPromotionStatus, promoteApp, syncApp, deleteApp, updateApp, upgradeAppTemplate } from "../lib/apps";
import { listTemplateVersions } from "../lib/templates";
import type { TemplateVersionInfo } from "../types";
import { createPreview, deletePreview } from "../lib/previews";
import {
  getAppEnvConfig,
  getAppEnvEnvConfig,
  getResolvedEnvConfig,
  updateAppEnvConfig,
  updateAppEnvEnvConfig,
} from "../lib/envconfig";
import type { EnvConfig, ResolvedEnvVar } from "../lib/envconfig";
import { EnvConfigEditor } from "../components/EnvConfigEditor";
import { SecretEditor } from "../components/SecretEditor";
import {
  listAppGlobalSecretKeys,
  upsertAppGlobalSecrets,
  deleteAppGlobalSecretKey,
  listAppEnvSecretKeys,
  upsertAppEnvSecrets,
  deleteAppEnvSecretKey,
  listAppClusterSecretKeys,
  upsertAppClusterSecrets,
  deleteAppClusterSecretKey,
  getResolvedSecrets,
} from "../lib/secrets";
import { listOrgEnvironments } from "../lib/settings";
import type { ResolvedSecretEntry } from "../lib/secrets";
import type {
  AppDeploymentHistoryResponse,
  AppDetail as AppDetailType,
  AppEnvironmentSummary,
  AppLogsResponse,
  ComponentSummary,
  DeploymentHistoryEntry,
  Diagnostic,
  KargoAppPipeline,
  KargoPromotion,
  KargoStageStatus,
  PromoteResponse,
} from "../types";

// ---------------------------------------------------------------------------
// Tab types
// ---------------------------------------------------------------------------

type TabId = "overview" | "deployments" | "previews" | "logs" | "traffic" | "envvars";

const TABS: { id: TabId; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "deployments", label: "Deployments" },
  { id: "previews", label: "Previews" },
  { id: "logs", label: "Logs" },
  { id: "traffic", label: "Traffic" },
  { id: "envvars", label: "Env Vars" },
];

// ---------------------------------------------------------------------------
// Status helpers
// ---------------------------------------------------------------------------

interface StatusStyle {
  dot: string;
  bg: string;
  label: string;
}

const fallbackStatus: StatusStyle = {
  dot: "bg-gray-300",
  bg: "bg-gray-100 text-gray-500",
  label: "Unknown",
};

const statusStyles: Record<string, StatusStyle> = {
  healthy: {
    dot: "bg-emerald-500",
    bg: "bg-emerald-50 text-emerald-700",
    label: "Healthy",
  },
  degraded: {
    dot: "bg-amber-500",
    bg: "bg-amber-50 text-amber-700",
    label: "Degraded",
  },
  progressing: {
    dot: "bg-blue-500",
    bg: "bg-blue-50 text-blue-700",
    label: "Syncing",
  },
  not_deployed: {
    dot: "bg-gray-300",
    bg: "bg-gray-100 text-gray-500",
    label: "Not deployed",
  },
};

function StatusBadge({
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
    <span
      className={`inline-flex items-center gap-1.5 rounded-full ${cls} ${cfg.bg}`}
    >
      <span
        className={`rounded-full ${cfg.dot} ${size === "lg" ? "h-2 w-2" : "h-1.5 w-1.5"}`}
      />
      {cfg.label}
    </span>
  );
}

function overallPhase(envs: AppEnvironmentSummary[]): string {
  const active = envs.filter((e) => e.envType !== "preview");
  if (active.length === 0) return "not_deployed";
  const phases = active.map((e) => e.status.phase);
  if (phases.every((p) => p === "healthy")) return "healthy";
  if (phases.some((p) => p === "degraded")) return "degraded";
  if (phases.some((p) => p === "progressing")) return "progressing";
  if (phases.every((p) => p === "not_deployed")) return "not_deployed";
  // Mixed case: some envs healthy, rest not yet deployed (e.g. staging up,
  // prod not promoted yet). This is expected — not a degraded state.
  if (phases.some((p) => p === "healthy")) return "healthy";
  return "degraded";
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function formatTime(iso?: string): string {
  if (!iso) return "—";
  return new Date(iso).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/**
 * Returns the logical promotion source and target names for the currently
 * selected environment, or null if no promotion is applicable (e.g. prod).
 *
 * Promotion path: preview → staging → prod
 */
function getPromoteTarget(
  currentEnv: AppEnvironmentSummary | null,
  environments: AppEnvironmentSummary[],
): { source: string; target: string } | null {
  if (!currentEnv) return null;
  if (currentEnv.envType === "preview") {
    const staging = environments.find((e) => e.envType === "staging");
    return staging
      ? { source: currentEnv.envName, target: staging.envName }
      : null;
  }
  if (currentEnv.envType === "staging") {
    const prod = environments.find((e) => e.envType === "prod");
    return prod ? { source: currentEnv.envName, target: prod.envName } : null;
  }
  return null;
}

// ---------------------------------------------------------------------------
// Icons
// ---------------------------------------------------------------------------

const icons = {
  externalLink: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M13.5 6H5.25A2.25 2.25 0 0 0 3 8.25v10.5A2.25 2.25 0 0 0 5.25 21h10.5A2.25 2.25 0 0 0 18 18.75V10.5m-10.5 6L21 3m0 0h-5.25M21 3v5.25"
      />
    </svg>
  ),
  rocket: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M15.59 14.37a6 6 0 0 1-5.84 7.38v-4.8m5.84-2.58a14.98 14.98 0 0 0 6.16-12.12A14.98 14.98 0 0 0 9.631 8.41m5.96 5.96a14.926 14.926 0 0 1-5.841 2.58m-.119-8.54a6 6 0 0 0-7.381 5.84h4.8m2.581-5.84a14.927 14.927 0 0 0-2.58 5.84m2.699 2.7c-.103.021-.207.041-.311.06a15.09 15.09 0 0 1-2.448-2.448 14.9 14.9 0 0 1 .06-.312m-2.24 2.39a4.493 4.493 0 0 0-1.757 4.306 4.493 4.493 0 0 0 4.306-1.758M16.5 9a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0Z"
      />
    </svg>
  ),
  branch: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M7.217 10.907a2.25 2.25 0 1 0 0 2.186m0-2.186c.18.324.283.696.283 1.093s-.103.77-.283 1.093m0-2.186 9.566-5.314m-9.566 7.5 9.566 5.314m0 0a2.25 2.25 0 1 0 3.935 2.186 2.25 2.25 0 0 0-3.935-2.186Zm0-12.814a2.25 2.25 0 1 0 3.933-2.185 2.25 2.25 0 0 0-3.933 2.185Z"
      />
    </svg>
  ),
  terminal: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="m6.75 7.5 3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0 0 21 18V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v12a2.25 2.25 0 0 0 2.25 2.25Z"
      />
    </svg>
  ),
  lock: (
    <svg
      className="h-3.5 w-3.5"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M16.5 10.5V6.75a4.5 4.5 0 1 0-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 0 0 2.25-2.25v-6.75a2.25 2.25 0 0 0-2.25-2.25H6.75a2.25 2.25 0 0 0-2.25 2.25v6.75a2.25 2.25 0 0 0 2.25 2.25Z"
      />
    </svg>
  ),
  clock: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M12 6v6h4.5m4.5 0a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z"
      />
    </svg>
  ),
  cloudArrowUp: (
    <svg
      className="h-4 w-4"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={1.5}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M12 16.5V9.75m0 0 3 3m-3-3-3 3M6.75 19.5a4.5 4.5 0 0 1-1.41-8.775 5.25 5.25 0 0 1 10.338-2.32 5.75 5.75 0 0 1 1.046 11.095"
      />
    </svg>
  ),
  spinner: (
    <svg
      className="h-4 w-4 animate-spin"
      fill="none"
      viewBox="0 0 24 24"
    >
      <circle
        className="opacity-25"
        cx="12"
        cy="12"
        r="10"
        stroke="currentColor"
        strokeWidth="4"
      />
      <path
        className="opacity-75"
        fill="currentColor"
        d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
      />
    </svg>
  ),
};

// ---------------------------------------------------------------------------
// Promotion success view (Kargo-aware)
// ---------------------------------------------------------------------------

const kargoPhaseBadge: Record<string, { bg: string; label: string }> = {
  Pending: { bg: "bg-amber-50 text-amber-700", label: "Pending" },
  Running: { bg: "bg-blue-50 text-blue-700", label: "Running" },
  Succeeded: { bg: "bg-emerald-50 text-emerald-700", label: "Succeeded" },
  Failed: { bg: "bg-red-50 text-red-700", label: "Failed" },
};

function KargoPromotionDetail({
  kargo,
  project,
  app,
}: {
  kargo: KargoPromotion;
  project?: string;
  app?: string;
}) {
  const [phase, setPhase] = useState(kargo.phase ?? "Pending");
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Poll the promotion status every 3 s until it reaches a terminal state.
  useEffect(() => {
    const terminal = new Set(["Succeeded", "Failed", "Errored", "Aborted"]);

    function stopPolling() {
      if (pollRef.current !== null) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    }

    if (!project || !app || terminal.has(phase)) return stopPolling;

    async function fetchPhase() {
      try {
        const status = await getKargoPromotionStatus(project!, app!, kargo.name);
        setPhase(status.phase);
        if (terminal.has(status.phase)) {
          stopPolling();
          if (status.phase === "Succeeded") {
            toast.success("Kargo promotion succeeded", {
              description: `${kargo.stage} is now running the new freight.`,
            });
          } else {
            toast.error("Kargo promotion failed", {
              description: `Phase: ${status.phase}. Check Kargo for details.`,
            });
          }
        }
      } catch {
        // Silently swallow; we keep the last known phase.
      }
    }

    fetchPhase();
    pollRef.current = setInterval(fetchPhase, 2000);
    return stopPolling;
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [project, app, kargo.name]);

  const badge = kargoPhaseBadge[phase] ?? {
    bg: "bg-gray-100 text-gray-500",
    label: phase,
  };
  const isActive = phase === "Pending" || phase === "Running";

  return (
    <div className="mt-3 rounded-xl border border-violet-100 bg-violet-50 p-3">
      <div className="mb-2 flex items-center gap-2">
        <span className="rounded-md bg-violet-100 px-2 py-0.5 text-xs font-semibold uppercase tracking-wide text-violet-700">
          Kargo
        </span>
        <span
          className={`inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium ${badge.bg}`}
        >
          {isActive && (
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current" />
          )}
          {badge.label}
        </span>
        {isActive && (
          <svg
            className="h-3.5 w-3.5 animate-spin text-violet-500"
            fill="none"
            viewBox="0 0 24 24"
          >
            <circle
              className="opacity-25"
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              strokeWidth="4"
            />
            <path
              className="opacity-75"
              fill="currentColor"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
            />
          </svg>
        )}
      </div>
      <dl className="space-y-1.5 text-sm">
        <div className="flex justify-between gap-4">
          <dt className="shrink-0 text-violet-500">Promotion</dt>
          <dd className="truncate font-mono text-violet-900">{kargo.name}</dd>
        </div>
        <div className="flex justify-between gap-4">
          <dt className="shrink-0 text-violet-500">Stage</dt>
          <dd className="font-medium capitalize text-violet-900">
            {kargo.stage}
          </dd>
        </div>
        <div className="flex justify-between gap-4">
          <dt className="shrink-0 text-violet-500">Freight</dt>
          <dd className="truncate font-mono text-violet-900">
            {kargo.freight}
          </dd>
        </div>
      </dl>
      <p className="mt-2 text-xs text-violet-500">
        Track with:{" "}
        <code className="rounded bg-violet-100 px-1 py-0.5 text-violet-700">
          kubectl get promotions -n {kargo.stage}
        </code>
      </p>
    </div>
  );
}

function PromoteSuccessView({
  result,
  promoteTarget,
  project,
  app,
  onDone,
}: {
  result: PromoteResponse;
  promoteTarget: string;
  project?: string;
  app?: string;
  onDone: () => void;
}) {
  return (
    <>
      <div className="mb-4 flex h-10 w-10 items-center justify-center rounded-full bg-emerald-100">
        <svg
          className="h-5 w-5 text-emerald-600"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          strokeWidth={2}
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            d="m4.5 12.75 6 6 9-13.5"
          />
        </svg>
      </div>
      <h3 className="text-lg font-semibold text-gray-900">
        Promotion initiated
      </h3>
      <p className="mt-1 text-sm text-gray-500">{result.message}</p>

      <dl className="mt-4 space-y-2 text-sm">
        <div className="flex justify-between">
          <dt className="text-gray-400">From</dt>
          <dd className="font-medium capitalize text-gray-900">
            {result.source}
          </dd>
        </div>
        <div className="flex justify-between">
          <dt className="text-gray-400">To</dt>
          <dd className="font-medium capitalize text-gray-900">
            {result.destination}
          </dd>
        </div>
        <div className="flex justify-between">
          <dt className="text-gray-400">Namespace</dt>
          <dd className="font-mono text-gray-600">{result.namespace}</dd>
        </div>
        {result.release?.image && (
          <div className="flex justify-between gap-4">
            <dt className="shrink-0 text-gray-400">Image</dt>
            <dd className="truncate font-mono text-gray-600">
              {result.release.image}
              {result.release.tag ? `:${result.release.tag}` : ""}
            </dd>
          </div>
        )}
      </dl>

      {result.kargoPromotion && (
        <KargoPromotionDetail kargo={result.kargoPromotion} project={project} app={app} />
      )}

      <button
        onClick={onDone}
        className="mt-6 w-full rounded-lg bg-gray-900 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700"
      >
        {promoteTarget ? `View ${promoteTarget}` : "Done"}
      </button>
    </>
  );
}

// ---------------------------------------------------------------------------
// Env pipeline bar — replaces the flat env switcher with a live pipeline view.
// Each stable environment is a clickable node showing Kargo phase + freight.
// Promotion arrows connect nodes to show the progression direction.
// Preview envs remain as simple pills below the pipeline.
// When Kargo is unavailable, nodes degrade to plain env selector buttons.
// ---------------------------------------------------------------------------

const stagePhaseCfg: Record<
  string,
  { dot: string; bg: string; border: string; label: string; spin?: boolean }
> = {
  Steady: {
    dot: "bg-emerald-500",
    bg: "bg-emerald-50",
    border: "border-emerald-200",
    label: "Steady",
  },
  Promoting: {
    dot: "bg-blue-500",
    bg: "bg-blue-50",
    border: "border-blue-200",
    label: "Promoting",
    spin: true,
  },
  NotReady: {
    dot: "bg-amber-500",
    bg: "bg-amber-50",
    border: "border-amber-200",
    label: "Not Ready",
  },
};
const fallbackStageCfg = {
  dot: "bg-gray-300",
  bg: "bg-white",
  border: "border-gray-200",
  label: "",
  spin: false,
};

function EnvPipelineBar({
  project,
  appName,
  nonPreviewEnvs,
  previewEnvs,
  selectedEnvName,
  onSelect,
}: {
  project: string;
  appName: string;
  nonPreviewEnvs: AppEnvironmentSummary[];
  previewEnvs: AppEnvironmentSummary[];
  selectedEnvName: string | null;
  onSelect: (envName: string) => void;
}) {
  const [pipeline, setPipeline] = useState<KargoAppPipeline | null>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function fetchPipeline() {
      try {
        const data = await getKargoAppPipeline(project, appName);
        if (!cancelled) setPipeline(data);
      } catch {
        // Kargo not configured — degrade to plain env switcher.
      }
    }

    fetchPipeline();
    pollRef.current = setInterval(fetchPipeline, 3000);
    return () => {
      cancelled = true;
      if (pollRef.current !== null) clearInterval(pollRef.current);
    };
  }, [project, appName]);

  const stageMap: Record<string, KargoStageStatus> = pipeline
    ? Object.fromEntries(pipeline.stages.map((s) => [s.envName, s]))
    : {};

  return (
    <div className="space-y-2">
      {/* Pipeline row: stable envs connected by promotion arrows */}
      <div className="flex flex-wrap items-stretch gap-0">
        {nonPreviewEnvs.map((env, i) => {
          const stage = stageMap[env.envName];
          const isSelected = selectedEnvName === env.envName;
          const phaseCfg = stage
            ? (stagePhaseCfg[stage.phase] ?? fallbackStageCfg)
            : fallbackStageCfg;
          const runtimeCfg = statusStyles[env.status.phase] ?? fallbackStatus;

          return (
            <div key={env.envName} className="flex items-center">
              <button
                onClick={() => onSelect(env.envName)}
                className={`flex min-w-[108px] flex-col gap-1.5 rounded-xl border px-3 py-2 text-left transition-all ${
                  isSelected
                    ? "border-gray-900 bg-gray-900 shadow-sm"
                    : env.status.phase === "progressing"
                      ? "border-blue-200 bg-blue-50"
                      : `${phaseCfg.bg} ${phaseCfg.border} hover:brightness-95`
                }`}
              >
                {/* Row 1: env name + runtime status dot */}
                <div className="flex items-center gap-1.5">
                  <span
                    className={`h-1.5 w-1.5 flex-shrink-0 rounded-full ${
                      isSelected ? "bg-white/60" : runtimeCfg.dot
                    } ${
                      (stage?.phase === "Promoting" || env.status.phase === "progressing") && !isSelected
                        ? "animate-pulse"
                        : ""
                    }`}
                  />
                  <span
                    className={`text-xs font-semibold capitalize ${
                      isSelected ? "text-white" : "text-gray-900"
                    }`}
                  >
                    {env.envName}
                  </span>
                </div>

                {/* Row 2: Kargo phase badge + ArgoCD runtime label — always shown */}
                <div className="flex flex-wrap items-center gap-1">
                  {/* ArgoCD runtime status */}
                  <span
                    className={`inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[10px] font-medium ${
                      isSelected
                        ? "bg-white/10 text-white/80"
                        : `${runtimeCfg.bg}`
                    }`}
                  >
                    {env.status.phase === "progressing" && !isSelected && (
                      <svg className="h-2 w-2 animate-spin" fill="none" viewBox="0 0 24 24">
                        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                      </svg>
                    )}
                    {runtimeCfg.label}
                  </span>

                  {/* Kargo phase badge (when data available) */}
                  {stage && phaseCfg.label && (
                    <span
                      className={`inline-flex items-center gap-1 rounded-full border px-1.5 py-0.5 text-[10px] font-medium ${
                        isSelected
                          ? "border-white/20 bg-white/10 text-white/70"
                          : `${phaseCfg.bg} ${phaseCfg.border}`
                      }`}
                    >
                      {phaseCfg.spin && (
                        <svg className="h-2 w-2 animate-spin" fill="none" viewBox="0 0 24 24">
                          <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                          <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
                        </svg>
                      )}
                      <span className={`h-1 w-1 rounded-full ${isSelected ? "bg-white/50" : phaseCfg.dot}`} />
                      {phaseCfg.label}
                    </span>
                  )}

                  {/* New freight badge */}
                  {stage && stage.availableFreightCount > 0 && (
                    <span className={`inline-flex items-center rounded-full border px-1.5 py-0.5 text-[10px] font-medium ${
                      isSelected
                        ? "border-amber-400/40 bg-amber-400/20 text-amber-200"
                        : "border-amber-300 bg-amber-50 text-amber-700"
                    }`}>
                      {stage.availableFreightCount} new
                    </span>
                  )}
                </div>
              </button>

              {/* Promotion arrow between nodes */}
              {i < nonPreviewEnvs.length - 1 && (
                <svg
                  className="mx-1 h-3.5 w-3.5 flex-shrink-0 text-gray-300"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                  strokeWidth={2}
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    d="M13.5 4.5 21 12m0 0-7.5 7.5M21 12H3"
                  />
                </svg>
              )}
            </div>
          );
        })}
      </div>

      {/* Preview env pills — not part of the promotion pipeline */}
      {previewEnvs.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          <span className="text-xs text-gray-400">Previews:</span>
          {previewEnvs.map((env) => (
            <button
              key={env.envName}
              onClick={() => onSelect(env.envName)}
              className={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium transition-colors ${
                selectedEnvName === env.envName
                  ? "bg-purple-700 text-white"
                  : "bg-purple-50 text-purple-700 hover:bg-purple-100"
              }`}
            >
              {env.preview?.previewName ?? env.envName}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// AppDetail page
// ---------------------------------------------------------------------------

export function AppDetail() {
  const { project, app: appName } = useParams<{
    project: string;
    app: string;
  }>();
  const navigate = useNavigate();

  const [data, setData] = useState<AppDetailType | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [activeTab, setActiveTab] = useState<TabId>("overview");
  const [selectedEnvName, setSelectedEnvName] = useState<string | null>(null);
  // Freshly-fetched detail for the selected environment; falls back to the
  // summary embedded in the app detail response when unavailable.
  const [selectedEnvDetail, setSelectedEnvDetail] =
    useState<AppEnvironmentSummary | null>(null);

  // Preview form
  const [showPreviewForm, setShowPreviewForm] = useState(false);
  const [previewName, setPreviewName] = useState("");
  const [previewError, setPreviewError] = useState<string | null>(null);
  const [previewSubmitting, setPreviewSubmitting] = useState(false);

  // Promote modal
  const [showPromoteModal, setShowPromoteModal] = useState(false);
  const [promoteSource, setPromoteSource] = useState("");
  const [promoteTarget, setPromoteTarget] = useState("");
  const [promoteSubmitting, setPromoteSubmitting] = useState(false);
  const [promoteError, setPromoteError] = useState<string | null>(null);
  const [promoteResult, setPromoteResult] = useState<PromoteResponse | null>(
    null,
  );

  // Sync to git
  type SyncState = "idle" | "syncing" | "success" | "error";
  const [syncState, setSyncState] = useState<SyncState>("idle");
  const [syncError, setSyncError] = useState<string | null>(null);

  // Delete app
  const [showDeleteConfirm, setShowDeleteConfirm] = useState(false);
  const [deleteConfirmInput, setDeleteConfirmInput] = useState("");
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  // Template upgrade
  const [templateVersions, setTemplateVersions] = useState<TemplateVersionInfo[]>([]);
  const [showUpgradeDialog, setShowUpgradeDialog] = useState(false);
  const [upgradeTarget, setUpgradeTarget] = useState<string>("");
  const [upgrading, setUpgrading] = useState(false);

  useEffect(() => {
    if (!project || !appName) return;
    let cancelled = false;

    getApp(project, appName)
      .then((res) => {
        if (cancelled) return;
        setData(res.app);
        const staging = res.app.environments.find(
          (e) => e.envType === "staging",
        );
        const first = res.app.environments[0];
        setSelectedEnvName(staging?.envName ?? first?.envName ?? null);
      })
      .catch((err) => {
        if (!cancelled)
          setError(err instanceof Error ? err.message : "Failed to load");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [project, appName]);

  // Pull the available versions for this app's template so we can surface
  // an "Upgrade available" affordance + populate the upgrade picker.
  // Silently no-ops on error: built-in templates have no archives, the
  // endpoint returns []; broken cluster fetch shouldn't break app detail.
  useEffect(() => {
    if (!data?.template?.name) return;
    let cancelled = false;
    listTemplateVersions(data.template.name)
      .then((res) => {
        if (!cancelled) setTemplateVersions(res.versions ?? []);
      })
      .catch(() => {
        if (!cancelled) setTemplateVersions([]);
      });
    return () => {
      cancelled = true;
    };
  }, [data?.template?.name]);

  // When the user switches environments, fetch the specific env detail for
  // fresh runtime data. Errors are silently swallowed; the embedded summary
  // from the app detail response is used as fallback.
  useEffect(() => {
    if (!project || !appName || !selectedEnvName) {
      setSelectedEnvDetail(null);
      return;
    }
    let cancelled = false;

    getAppEnvironment(project, appName, selectedEnvName)
      .then((res) => {
        if (!cancelled) setSelectedEnvDetail(res.environment);
      })
      .catch(() => {
        if (!cancelled) setSelectedEnvDetail(null);
      });

    return () => {
      cancelled = true;
    };
  }, [project, appName, selectedEnvName]);

  function handleDeletePreview(previewName: string) {
    deletePreview(previewName)
      .then(() => {
        setData((prev) => {
          if (!prev) return prev;
          return {
            ...prev,
            environments: prev.environments.filter(
              (e) => e.preview?.previewName !== previewName,
            ),
          };
        });
        toast.success("Preview deleted", {
          description: `Preview "${previewName}" has been removed.`,
        });
      })
      .catch((err) => {
        toast.error("Failed to delete preview", {
          description: err instanceof Error ? err.message : "Unknown error",
        });
      });
  }

  if (loading) return <DetailSkeleton />;

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">Failed to load app: {error}</p>
      </div>
    );
  }

  if (!data) return null;

  const ENV_ORDER: Record<string, number> = { staging: 0, prod: 1 };
  const nonPreviewEnvs = data.environments
    .filter((e) => e.envType !== "preview")
    .sort(
      (a, b) =>
        (ENV_ORDER[a.envName] ?? 99) - (ENV_ORDER[b.envName] ?? 99),
    );
  const previewEnvs = data.environments.filter((e) => e.envType === "preview");
  // The embedded summary from the app response; used as a fallback.
  const currentEnvSummary =
    data.environments.find((e) => e.envName === selectedEnvName) ?? null;
  // Prefer the freshly-fetched env detail when available; fall back to summary.
  const currentEnv: AppEnvironmentSummary | null =
    selectedEnvDetail ?? currentEnvSummary;
  const overallStatus = overallPhase(data.environments);
  const primaryUrl =
    currentEnv?.urls[0] ??
    data.environments.find((e) => e.urls.length > 0)?.urls[0];

  return (
    <div className="space-y-6">
      {/* Breadcrumb */}
      <nav className="flex items-center gap-1.5 text-sm text-gray-400">
        <Link to="/" className="hover:text-gray-600">
          Dashboard
        </Link>
        <span>/</span>
        <Link to={`/projects/${project}`} className="hover:text-gray-600">
          {project}
        </Link>
        <span>/</span>
        <span className="text-gray-600">{appName}</span>
      </nav>

      {/* App header */}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="flex items-center gap-3">
            <h1 className="truncate text-2xl font-semibold text-gray-900">
              {data.displayName ?? data.name}
            </h1>
            <StatusBadge status={overallStatus} size="lg" />
          </div>
          <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-gray-500">
            <Link
              to={`/templates/${data.template.name}`}
              className="inline-flex items-center gap-1 font-mono text-gray-600 hover:text-gray-900"
            >
              {data.template.name}
              {data.template.version && (
                <span className="text-gray-400">
                  v{data.template.version}
                </span>
              )}
            </Link>
            {(() => {
              const latest = templateVersions[0]?.version;
              if (!latest || !data.template.version) return null;
              if (latest === data.template.version) return null;
              return (
                <button
                  type="button"
                  onClick={() => {
                    setUpgradeTarget(latest);
                    setShowUpgradeDialog(true);
                  }}
                  className="inline-flex items-center gap-1 rounded-full border border-amber-300 bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-800 hover:bg-amber-100"
                  title={`Upgrade available: v${latest}`}
                >
                  upgrade → v{latest}
                </button>
              );
            })()}
            {data.description && (
              <span className="text-gray-400">{data.description}</span>
            )}
          </div>
        </div>

        {/* Quick actions */}
        <div className="flex flex-shrink-0 flex-wrap gap-2">
          {primaryUrl && (
            <a
              href={primaryUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm transition-colors hover:bg-gray-50"
            >
              {icons.externalLink}
              Open app
            </a>
          )}
          <button
            onClick={() => setActiveTab("logs")}
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm transition-colors hover:bg-gray-50"
            title="View logs"
          >
            {icons.terminal}
            Logs
          </button>
          <button
            onClick={() => {
              setShowPreviewForm(true);
              setPreviewName("");
              setPreviewError(null);
            }}
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm transition-colors hover:bg-gray-50"
            title="Create preview environment"
          >
            {icons.branch}
            Preview
          </button>
          <button
            onClick={async () => {
              if (!project || !appName || syncState === "syncing") return;
              setSyncState("syncing");
              setSyncError(null);
              try {
                await syncApp(project, appName);
                setSyncState("success");
                toast.success("Synced to Git", {
                  description: "ArgoCD will pick up the latest changes shortly.",
                });
                setTimeout(() => setSyncState("idle"), 3000);
              } catch (err) {
                const msg = err instanceof Error ? err.message : "Sync failed";
                setSyncError(msg);
                setSyncState("error");
                toast.error("Sync to Git failed", { description: msg });
              }
            }}
            disabled={syncState === "syncing"}
            className={`inline-flex items-center gap-1.5 rounded-lg border px-3.5 py-2 text-sm font-medium shadow-sm transition-colors disabled:opacity-60 ${
              syncState === "success"
                ? "border-emerald-200 bg-emerald-50 text-emerald-700"
                : syncState === "error"
                  ? "border-red-200 bg-red-50 text-red-700 hover:bg-red-100"
                  : "border-gray-200 bg-white text-gray-700 hover:bg-gray-50"
            }`}
            title="Re-publish this app to the gitops repo so ArgoCD picks it up"
          >
            {syncState === "syncing"
              ? icons.spinner
              : icons.cloudArrowUp}
            {syncState === "syncing"
              ? "Syncing…"
              : syncState === "success"
                ? "Synced ✓"
                : "Sync to Git"}
          </button>
          {(() => {
            const promotion = getPromoteTarget(currentEnv, data.environments);
            if (!promotion) return null;
            return (
              <button
                onClick={() => {
                  setPromoteSource(promotion.source);
                  setPromoteTarget(promotion.target);
                  setPromoteError(null);
                  setPromoteResult(null);
                  setShowPromoteModal(true);
                }}
                className="inline-flex items-center gap-1.5 rounded-lg bg-gray-900 px-3.5 py-2 text-sm font-medium text-white shadow-sm transition-colors hover:bg-gray-700"
                title={`Promote from ${promotion.source} to ${promotion.target}`}
              >
                {icons.rocket}
                Promote to {promotion.target}
              </button>
            );
          })()}
          <button
            onClick={() => {
              setDeleteConfirmInput("");
              setDeleteError(null);
              setShowDeleteConfirm(true);
            }}
            className="inline-flex items-center gap-1.5 rounded-lg border border-red-200 bg-white px-3.5 py-2 text-sm font-medium text-red-600 shadow-sm transition-colors hover:bg-red-50"
            title="Delete this app"
          >
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
              <path strokeLinecap="round" strokeLinejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
            </svg>
            Delete
          </button>
        </div>
      </div>
      {syncState === "error" && syncError && (
        <div className="flex items-start gap-3 rounded-lg border border-red-200 bg-red-50 px-4 py-3">
          <svg
            className="mt-0.5 h-4 w-4 flex-shrink-0 text-red-500"
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
            strokeWidth={2}
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z"
            />
          </svg>
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium text-red-700">Sync to Git failed</p>
            <p className="mt-0.5 text-xs text-red-600">{syncError}</p>
          </div>
          <button
            onClick={() => { setSyncState("idle"); setSyncError(null); }}
            className="flex-shrink-0 text-red-400 hover:text-red-600"
            aria-label="Dismiss"
          >
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M6 18 18 6M6 6l12 12" />
            </svg>
          </button>
        </div>
      )}

      {/* Preview form */}
      {showPreviewForm && (
        <div className="rounded-xl border border-indigo-200 bg-indigo-50/50 p-5">
          <div className="flex items-start gap-3">
            <div className="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-lg bg-indigo-100 text-indigo-600">
              {icons.branch}
            </div>
            <div className="min-w-0 flex-1">
              <h3 className="text-sm font-medium text-gray-900">
                Create preview environment
              </h3>
              <p className="mt-0.5 text-xs text-gray-500">
                Deploy{" "}
                <span className="font-medium">{appName}</span> to an isolated
                preview namespace.
              </p>
              <form
                className="mt-3 flex items-end gap-2"
                onSubmit={async (e) => {
                  e.preventDefault();
                  if (!previewName.trim() || !project || !appName) return;
                  setPreviewSubmitting(true);
                  setPreviewError(null);
                  try {
                    await createPreview({
                      name: previewName.trim(),
                      project,
                      service: appName,
                    });
                    setShowPreviewForm(false);
                    toast.success("Preview created", {
                      description: `Preview environment "${previewName.trim()}" is being provisioned.`,
                    });
                    navigate("/previews");
                  } catch (err) {
                    const msg = err instanceof Error ? err.message : "Failed to create";
                    setPreviewError(msg);
                    toast.error("Failed to create preview", { description: msg });
                  } finally {
                    setPreviewSubmitting(false);
                  }
                }}
              >
                <div className="flex-1">
                  <label
                    htmlFor="preview-name"
                    className="mb-1 block text-xs font-medium text-gray-700"
                  >
                    Preview name
                  </label>
                  <input
                    id="preview-name"
                    type="text"
                    value={previewName}
                    onChange={(e) => setPreviewName(e.target.value)}
                    placeholder="pr-42"
                    pattern="[a-z][a-z0-9-]*[a-z0-9]"
                    required
                    className="w-full rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-900 placeholder-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                  />
                </div>
                <button
                  type="submit"
                  disabled={previewSubmitting || !previewName.trim()}
                  className="rounded-md bg-indigo-600 px-4 py-1.5 text-sm font-medium text-white transition-colors hover:bg-indigo-700 disabled:opacity-50"
                >
                  {previewSubmitting ? "Creating…" : "Create"}
                </button>
                <button
                  type="button"
                  onClick={() => setShowPreviewForm(false)}
                  className="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-600 transition-colors hover:bg-gray-50"
                >
                  Cancel
                </button>
              </form>
              {previewError && (
                <p className="mt-2 text-xs text-red-600">{previewError}</p>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Promote modal */}
      {showPromoteModal && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="mx-4 w-full max-w-md rounded-2xl bg-white p-6 shadow-xl">
            {promoteResult ? (
              <PromoteSuccessView
                result={promoteResult}
                promoteTarget={promoteTarget}
                project={project}
                app={appName}
                onDone={() => {
                  setShowPromoteModal(false);
                  if (promoteTarget) setSelectedEnvName(promoteTarget);
                }}
              />
            ) : (
              <>
                <div className="mb-4 flex h-10 w-10 items-center justify-center rounded-full bg-gray-100 text-gray-600">
                  {icons.rocket}
                </div>
                <h3 className="text-lg font-semibold text-gray-900">
                  Promote {appName}
                </h3>
                <p className="mt-1 text-sm text-gray-500">
                  Promote this app from{" "}
                  <span className="font-medium capitalize">{promoteSource}</span>{" "}
                  to{" "}
                  <span className="font-medium capitalize">{promoteTarget}</span>
                  .
                </p>

                {/* Source → target visual */}
                <div className="mt-4 flex items-center gap-3">
                  <div className="flex-1 rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 text-center">
                    <p className="text-xs text-gray-400">From</p>
                    <p className="font-medium capitalize text-gray-900">
                      {promoteSource}
                    </p>
                  </div>
                  <svg
                    className="h-4 w-4 flex-shrink-0 text-gray-400"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    strokeWidth={2}
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      d="M13.5 4.5 21 12m0 0-7.5 7.5M21 12H3"
                    />
                  </svg>
                  <div className="flex-1 rounded-lg border border-emerald-200 bg-emerald-50 px-3 py-2 text-center">
                    <p className="text-xs text-emerald-600">To</p>
                    <p className="font-medium capitalize text-emerald-700">
                      {promoteTarget}
                    </p>
                  </div>
                </div>

                {promoteError && (
                  <div className="mt-3 rounded-md bg-red-50 px-3 py-2">
                    <p className="text-sm text-red-700">{promoteError}</p>
                  </div>
                )}
                <div className="mt-6 flex gap-3">
                  <button
                    onClick={async () => {
                      if (!project || !appName || !promoteTarget) return;
                      setPromoteSubmitting(true);
                      setPromoteError(null);
                      try {
                        const result = await promoteApp(project, appName, {
                          targetEnvironment: promoteTarget,
                        });
                        setPromoteResult(result);
                        if (result.kargoPromotion) {
                          toast.info("Kargo promotion initiated", {
                            description: `Promoting to ${promoteTarget} — tracking phase…`,
                          });
                        } else {
                          toast.success("Promotion succeeded", {
                            description: `${appName} promoted to ${promoteTarget}.`,
                          });
                        }
                      } catch (err) {
                        const msg = err instanceof Error ? err.message : "Promotion failed";
                        setPromoteError(msg);
                        toast.error("Promotion failed", { description: msg });
                      } finally {
                        setPromoteSubmitting(false);
                      }
                    }}
                    disabled={promoteSubmitting || !promoteTarget}
                    className="flex-1 rounded-lg bg-gray-900 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700 disabled:opacity-50"
                  >
                    {promoteSubmitting ? "Promoting…" : `Promote to ${promoteTarget}`}
                  </button>
                  <button
                    onClick={() => setShowPromoteModal(false)}
                    disabled={promoteSubmitting}
                    className="flex-1 rounded-lg border border-gray-300 bg-white py-2 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50 disabled:opacity-50"
                  >
                    Cancel
                  </button>
                </div>
              </>
            )}
          </div>
        </div>
      )}

      {/* Template upgrade dialog */}
      {showUpgradeDialog && data && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="mx-4 w-full max-w-md rounded-2xl bg-white p-6 shadow-xl">
            <h3 className="text-lg font-semibold text-gray-900">
              Upgrade template
            </h3>
            <p className="mt-1 text-sm text-gray-500">
              Pin {appName} to a different version of{" "}
              <span className="font-mono">{data.template.name}</span> and re-publish.
              ArgoCD will roll the new chart bytes out on its next sync.
            </p>
            <p className="mt-2 text-xs text-amber-700">
              No values migration is performed. If the new version's input schema
              differs from{" "}
              <span className="font-mono">v{data.template.version}</span>, edit
              the app's values via the existing flow before upgrading.
            </p>
            <label className="mt-4 block">
              <span className="text-sm font-medium text-gray-700">Target version</span>
              <select
                className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-gray-500 focus:ring-1 focus:ring-gray-500"
                value={upgradeTarget}
                onChange={(e) => setUpgradeTarget(e.target.value)}
              >
                {templateVersions.map((v) => (
                  <option key={v.version} value={v.version}>
                    v{v.version}
                    {v.version === data.template.version ? " (current)" : ""}
                  </option>
                ))}
              </select>
            </label>
            <div className="mt-6 flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setShowUpgradeDialog(false)}
                disabled={upgrading}
                className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
              >
                Cancel
              </button>
              <button
                type="button"
                disabled={
                  upgrading ||
                  !upgradeTarget ||
                  upgradeTarget === data.template.version
                }
                onClick={async () => {
                  if (!project || !appName) return;
                  setUpgrading(true);
                  try {
                    const res = await upgradeAppTemplate(project, appName, upgradeTarget);
                    toast.success(
                      `Upgraded ${appName}: v${res.fromVersion ?? "?"} → v${res.toVersion ?? upgradeTarget}`,
                    );
                    setShowUpgradeDialog(false);
                    // Refresh app detail so the version pin reflects the new state.
                    const refreshed = await getApp(project, appName);
                    setData(refreshed.app);
                  } catch (err) {
                    toast.error(err instanceof Error ? err.message : "Upgrade failed");
                  } finally {
                    setUpgrading(false);
                  }
                }}
                className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-800 disabled:opacity-50"
              >
                {upgrading ? "Upgrading…" : "Upgrade"}
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Delete app confirmation modal */}
      {showDeleteConfirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
          <div className="mx-4 w-full max-w-md rounded-2xl bg-white p-6 shadow-xl">
            <div className="mb-4 flex h-10 w-10 items-center justify-center rounded-full bg-red-100 text-red-600">
              <svg className="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                <path strokeLinecap="round" strokeLinejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0" />
              </svg>
            </div>
            <h3 className="text-lg font-semibold text-gray-900">Delete {appName}</h3>
            <p className="mt-1 text-sm text-gray-500">
              This action cannot be undone. The app and all its environments will be permanently removed.
              GitOps manifests will be deleted from the repository.
            </p>
            <p className="mt-3 text-sm text-gray-700">
              Type <span className="font-mono font-semibold text-gray-900">{appName}</span> to confirm.
            </p>
            <input
              type="text"
              value={deleteConfirmInput}
              onChange={(e) => setDeleteConfirmInput(e.target.value)}
              placeholder={appName}
              className="mt-2 w-full rounded-lg border border-gray-300 px-3 py-2 text-sm focus:border-red-400 focus:outline-none focus:ring-1 focus:ring-red-400"
              autoFocus
            />
            {deleteError && (
              <div className="mt-3 rounded-md bg-red-50 px-3 py-2">
                <p className="text-sm text-red-700">{deleteError}</p>
              </div>
            )}
            <div className="mt-6 flex gap-3">
              <button
                onClick={async () => {
                  if (!project || !appName || deleteConfirmInput !== appName) return;
                  setDeleting(true);
                  setDeleteError(null);
                  try {
                    await deleteApp(project, appName);
                    toast.success("App deleted", { description: `${appName} has been removed.` });
                    navigate(`/projects/${project}`);
                  } catch (err) {
                    const msg = err instanceof Error ? err.message : "Delete failed";
                    setDeleteError(msg);
                    toast.error("Failed to delete app", { description: msg });
                  } finally {
                    setDeleting(false);
                  }
                }}
                disabled={deleting || deleteConfirmInput !== appName}
                className="flex-1 rounded-lg bg-red-600 py-2 text-sm font-medium text-white transition-colors hover:bg-red-700 disabled:opacity-50"
              >
                {deleting ? "Deleting…" : "Delete app"}
              </button>
              <button
                onClick={() => setShowDeleteConfirm(false)}
                disabled={deleting}
                className="flex-1 rounded-lg border border-gray-300 bg-white py-2 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50 disabled:opacity-50"
              >
                Cancel
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Environment pipeline bar: stable envs as pipeline nodes, previews as pills */}
      <EnvPipelineBar
        project={project ?? ""}
        appName={appName ?? ""}
        nonPreviewEnvs={nonPreviewEnvs}
        previewEnvs={previewEnvs}
        selectedEnvName={selectedEnvName}
        onSelect={setSelectedEnvName}
      />

      {/* Tab bar */}
      <div className="border-b border-gray-200">
        <nav className="-mb-px flex gap-6">
          {TABS.map((tab) => (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={`pb-3 text-sm font-medium transition-colors ${
                activeTab === tab.id
                  ? "border-b-2 border-gray-900 text-gray-900"
                  : "text-gray-500 hover:text-gray-700"
              }`}
            >
              {tab.label}
            </button>
          ))}
        </nav>
      </div>

      {/* Tab panels */}
      {activeTab === "overview" && (
        <OverviewTab
          data={data}
          currentEnv={currentEnv}
          project={data.project}
          onSaved={async () => {
            const refreshed = await getApp(data.project, data.name);
            setData(refreshed.app);
          }}
        />
      )}
      {activeTab === "deployments" && (
        <DeploymentsTab
          project={project ?? ""}
          appName={appName ?? ""}
          envName={selectedEnvName ?? data.environments.find((e) => e.envType !== "preview")?.envName ?? ""}
        />
      )}
      {activeTab === "previews" && (
        <PreviewsTab
          previewEnvs={previewEnvs}
          onDeletePreview={handleDeletePreview}
        />
      )}
      {activeTab === "logs" && (
        <LogsTab
          project={project ?? ""}
          appName={appName ?? ""}
          selectedEnvName={selectedEnvName ?? ""}
          components={data.components}
          environments={data.environments}
        />
      )}
      {activeTab === "traffic" && <TrafficTab />}
      {activeTab === "envvars" && (
        <EnvVarsTab
          project={project ?? ""}
          appName={appName ?? ""}
          environments={data.environments}
          selectedEnvName={selectedEnvName}
          onSelectEnv={setSelectedEnvName}
        />
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab: Overview
// ---------------------------------------------------------------------------

// DiagnosticsPanel surfaces delivery problems (ArgoCD conditions/health,
// ExternalSecret errors) so an operator can see why an env is stuck or
// "not deployed" without leaving suparship. Renders nothing when healthy.
function DiagnosticsPanel({ diagnostics }: { diagnostics: Diagnostic[] }) {
  if (diagnostics.length === 0) return null;

  const sourceLabel = (s: string) =>
    s === "external-secrets"
      ? "Secrets"
      : s === "argocd" || s === "argocd-platform"
        ? "Delivery"
        : s;

  return (
    <div className="space-y-2">
      <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
        Diagnostics
      </h2>
      {diagnostics.map((d, i) => {
        const isError = d.level === "error";
        return (
          <div
            key={`${d.source}-${i}`}
            className={`rounded-lg border px-4 py-3 ${
              isError
                ? "border-red-200 bg-red-50"
                : "border-amber-200 bg-amber-50"
            }`}
          >
            <div className="flex items-center gap-2">
              <span
                className={`inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide ${
                  isError
                    ? "bg-red-100 text-red-700"
                    : "bg-amber-100 text-amber-700"
                }`}
              >
                {sourceLabel(d.source)}
              </span>
              <span
                className={`text-sm font-medium ${
                  isError ? "text-red-900" : "text-amber-900"
                }`}
              >
                {d.title}
              </span>
            </div>
            {d.detail && (
              <p className="mt-1 whitespace-pre-wrap break-words font-mono text-xs text-gray-600">
                {d.detail}
              </p>
            )}
            {d.hint && (
              <p
                className={`mt-2 text-xs ${
                  isError ? "text-red-800" : "text-amber-800"
                }`}
              >
                <span className="font-medium">Suggested fix: </span>
                {d.hint}
              </p>
            )}
          </div>
        );
      })}
    </div>
  );
}

// AppConfigEditor shows the app's template input Values (and display
// name/description) and lets a project_admin edit them inline. Saving PATCHes
// the app — re-validated server-side against the template's input schema — and
// re-publishes so values.yaml regenerates. Existing values are string-edited;
// non-string values (numbers/bools/objects) are shown read-only with a hint to
// keep the edit surface honest about what it round-trips.
function AppConfigEditor({
  data,
  project,
  onSaved,
}: {
  data: AppDetailType;
  project: string;
  onSaved: () => Promise<void>;
}) {
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [displayName, setDisplayName] = useState(data.displayName ?? "");
  const [description, setDescription] = useState(data.description ?? "");
  // Only string-valued entries are editable; others are passed through untouched.
  const stringEntries = Object.entries(data.values).filter(
    ([, v]) => typeof v === "string",
  ) as [string, string][];
  const [vals, setVals] = useState<Record<string, string>>(() =>
    Object.fromEntries(stringEntries),
  );

  function reset() {
    setDisplayName(data.displayName ?? "");
    setDescription(data.description ?? "");
    setVals(Object.fromEntries(stringEntries));
    setEditing(false);
  }

  async function save() {
    setSaving(true);
    try {
      // Merge edited string values back over the original values map so
      // non-string entries are preserved verbatim.
      const merged: Record<string, unknown> = { ...data.values };
      for (const [k, v] of Object.entries(vals)) merged[k] = v;
      await updateApp(project, data.name, {
        displayName,
        description,
        values: merged,
      });
      toast.success("App config updated — re-publishing to GitOps.");
      await onSaved();
      setEditing(false);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to update app config");
    } finally {
      setSaving(false);
    }
  }

  const nonStringKeys = Object.keys(data.values).filter(
    (k) => typeof data.values[k] !== "string",
  );

  return (
    <div className="rounded-xl border border-gray-200 bg-white">
      <div className="flex items-center justify-between border-b border-gray-100 px-5 py-3">
        <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
          Configuration
        </h2>
        {editing ? (
          <div className="flex items-center gap-2">
            <button
              onClick={reset}
              disabled={saving}
              className="rounded-md px-3 py-1 text-xs font-medium text-gray-500 hover:bg-gray-50"
            >
              Cancel
            </button>
            <button
              onClick={save}
              disabled={saving}
              className="rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              {saving ? "Saving…" : "Save"}
            </button>
          </div>
        ) : (
          <button
            onClick={() => setEditing(true)}
            className="text-xs font-medium text-indigo-600 hover:text-indigo-800"
          >
            Edit
          </button>
        )}
      </div>

      {editing ? (
        <div className="space-y-3 px-5 py-4">
          <label className="block">
            <span className="text-xs font-medium text-gray-600">Display name</span>
            <input
              type="text"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              className="mt-1 w-full rounded-md border border-gray-300 px-3 py-1.5 text-sm"
            />
          </label>
          <label className="block">
            <span className="text-xs font-medium text-gray-600">Description</span>
            <input
              type="text"
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              className="mt-1 w-full rounded-md border border-gray-300 px-3 py-1.5 text-sm"
            />
          </label>
          {stringEntries.map(([key]) => (
            <label key={key} className="block">
              <span className="font-mono text-xs text-gray-600">{key}</span>
              <input
                type="text"
                value={vals[key] ?? ""}
                onChange={(e) => setVals((cur) => ({ ...cur, [key]: e.target.value }))}
                className="mt-1 w-full rounded-md border border-gray-300 px-3 py-1.5 text-sm font-mono"
              />
            </label>
          ))}
          {nonStringKeys.length > 0 && (
            <p className="text-xs text-gray-400">
              {nonStringKeys.length} non-text value(s) ({nonStringKeys.join(", ")})
              are preserved unchanged and editable via the template/values flow.
            </p>
          )}
        </div>
      ) : Object.keys(data.values).length > 0 || data.description ? (
        <dl className="divide-y divide-gray-50">
          {Object.entries(data.values).map(([key, val]) => (
            <div key={key} className="flex items-center justify-between px-5 py-2.5">
              <dt className="font-mono text-sm text-gray-500">{key}</dt>
              <dd className="text-sm text-gray-900">{String(val)}</dd>
            </div>
          ))}
        </dl>
      ) : (
        <p className="px-5 py-4 text-sm text-gray-400">
          No configuration values. Click Edit to add display name/description.
        </p>
      )}
    </div>
  );
}

function OverviewTab({
  data,
  currentEnv,
  project,
  onSaved,
}: {
  data: AppDetailType;
  currentEnv: AppEnvironmentSummary | null;
  project: string;
  onSaved: () => Promise<void>;
}) {
  const replicas = currentEnv
    ? `${currentEnv.status.available}/${currentEnv.status.replicas}`
    : "—";
  const releaseTag =
    currentEnv?.release?.tag ??
    (currentEnv?.release?.image
      ? currentEnv.release.image.split(":").pop() ?? "—"
      : "—");
  const lastDeployed = formatTime(currentEnv?.status.lastDeployed);
  const urls = currentEnv?.urls ?? [];

  return (
    <div className="space-y-6">
      {/* Environment context bar: status, primary URL, namespace */}
      {currentEnv ? (
        <div className="flex flex-wrap items-center justify-between gap-y-1.5 rounded-lg border border-gray-100 bg-gray-50/50 px-4 py-2.5">
          <div className="flex flex-wrap items-center gap-3">
            <StatusBadge status={currentEnv.status.phase} />
          </div>
          {/* Namespace shown subtly for advanced users; not the focus of the view */}
          <span
            className="font-mono text-xs text-gray-400"
            title="Kubernetes namespace"
          >
            {currentEnv.namespace}
          </span>
        </div>
      ) : (
        <div className="rounded-lg border border-dashed border-gray-200 bg-gray-50/50 px-4 py-8 text-center">
          <p className="text-sm text-gray-500">Not yet deployed</p>
          <p className="mt-1 text-xs text-gray-400">
            No environment data available for this selection.
          </p>
        </div>
      )}

      {/* Delivery diagnostics — why an env is stuck / "not deployed" */}
      <DiagnosticsPanel diagnostics={currentEnv?.status.diagnostics ?? []} />

      {/* Quick stats */}
      <div className="grid gap-4 sm:grid-cols-3">
        <QuickStat label="Replicas" value={replicas} />
        <QuickStat
          label="Release"
          value={releaseTag !== "—" ? releaseTag : "—"}
          mono
        />
        <QuickStat label="Last deployed" value={lastDeployed} />
      </div>

      {/* Endpoints */}
      {urls.length > 0 && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-5 py-3">
            <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
              Endpoints
            </h2>
          </div>
          <div className="divide-y divide-gray-50">
            {urls.map((url) => (
              <div
                key={url}
                className="flex items-center justify-between px-5 py-2.5"
              >
                <a
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-sm text-blue-600 hover:text-blue-800 hover:underline"
                >
                  {url}
                </a>
                <a
                  href={url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-gray-400 hover:text-gray-600"
                >
                  <svg
                    className="h-4 w-4"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    strokeWidth={1.5}
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      d="M13.5 6H5.25A2.25 2.25 0 0 0 3 8.25v10.5A2.25 2.25 0 0 0 5.25 21h10.5A2.25 2.25 0 0 0 18 18.75V10.5m-10.5 6L21 3m0 0h-5.25M21 3v5.25"
                    />
                  </svg>
                </a>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Runtime component summaries for the selected environment */}
      <ComponentsTable components={data.components} currentEnv={currentEnv} />

      {/* Configuration — editable */}
      <AppConfigEditor
        data={data}
        project={project}
        onSaved={onSaved}
      />

      {/* Secrets */}
      {data.secretRefs.length > 0 && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-5 py-3">
            <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
              Secrets
            </h2>
          </div>
          <dl className="divide-y divide-gray-50">
            {data.secretRefs.map((ref) => (
              <div
                key={ref.name}
                className="flex items-center justify-between px-5 py-2.5"
              >
                <dt className="font-mono text-sm text-gray-500">{ref.name}</dt>
                <dd className="flex items-center gap-1.5 text-sm text-gray-600">
                  {icons.lock}
                  <span className="font-mono">{ref.secretRef}</span>
                </dd>
              </div>
            ))}
          </dl>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab: Deployments
// ---------------------------------------------------------------------------

function DeploymentsTab({
  project,
  appName,
  envName,
}: {
  project: string;
  appName: string;
  envName: string;
}) {
  const [historyData, setHistoryData] = useState<AppDeploymentHistoryResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [unavailable, setUnavailable] = useState(false);

  useEffect(() => {
    if (!project || !appName || !envName) return;
    let cancelled = false;
    setLoading(true);
    setUnavailable(false);
    setHistoryData(null);

    getAppDeploymentHistory(project, appName, envName)
      .then((res) => {
        if (!cancelled) {
          setHistoryData(res);
          setLoading(false);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          const status = (err as { status?: number })?.status;
          setUnavailable(status === 501);
          setHistoryData(null);
          setLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [project, appName, envName]);

  return (
    <div className="rounded-xl border border-gray-200 bg-white">
      <div className="border-b border-gray-100 px-5 py-3">
        <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
          Deployment history
          {envName && (
            <span className="ml-2 font-mono normal-case text-gray-300">
              · {envName}
            </span>
          )}
        </h2>
      </div>

      {loading && (
        <div className="px-5 py-10 text-center">
          <div className="mx-auto mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-gray-100">
            {icons.spinner}
          </div>
          <p className="text-sm text-gray-400">Loading history…</p>
        </div>
      )}

      {!loading && unavailable && (
        <div className="px-5 py-10 text-center">
          <div className="mx-auto mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-gray-100 text-gray-400">
            {icons.clock}
          </div>
          <p className="text-sm font-medium text-gray-500">
            History unavailable
          </p>
          <p className="mt-1 text-xs text-gray-400">
            ArgoCD integration is not configured on this server.
          </p>
        </div>
      )}

      {!loading && !unavailable && historyData?.history.length === 0 && (
        <div className="px-5 py-10 text-center">
          <div className="mx-auto mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-gray-100 text-gray-400">
            {icons.clock}
          </div>
          <p className="text-sm font-medium text-gray-500">
            No deployment history yet
          </p>
          <p className="mt-1 text-xs text-gray-400">
            Syncs will appear here once ArgoCD deploys to{" "}
            <span className="font-mono">{envName}</span>.
          </p>
        </div>
      )}

      {!loading && !unavailable && historyData && historyData.history.length > 0 && (
        <ul className="divide-y divide-gray-50">
          {historyData.history.map((entry) => (
            <DeploymentHistoryRow key={entry.id} entry={entry} />
          ))}
        </ul>
      )}
    </div>
  );
}

function DeploymentHistoryRow({ entry }: { entry: DeploymentHistoryEntry }) {
  const shortRev = entry.revision ? entry.revision.slice(0, 8) : null;
  const deployedAtFmt = entry.deployedAt
    ? new Date(entry.deployedAt).toLocaleString(undefined, {
        month: "short",
        day: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      })
    : null;

  let durationSec: number | null = null;
  if (entry.deployedAt && entry.deployStartedAt) {
    const diff =
      new Date(entry.deployedAt).getTime() -
      new Date(entry.deployStartedAt).getTime();
    if (diff > 0) durationSec = Math.round(diff / 1000);
  }

  return (
    <li className="flex items-start justify-between gap-4 px-5 py-3.5">
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          {/* Sequence badge */}
          <span className="rounded bg-gray-100 px-1.5 py-0.5 font-mono text-xs text-gray-500">
            #{entry.id}
          </span>
          {/* Commit SHA */}
          {shortRev && (
            <span className="font-mono text-xs font-medium text-gray-800">
              {shortRev}
            </span>
          )}
          {/* Duration */}
          {durationSec !== null && (
            <span className="text-xs text-gray-400">
              {durationSec}s
            </span>
          )}
        </div>
        {/* Path */}
        {entry.path && (
          <p className="mt-1 truncate font-mono text-xs text-gray-400">
            {entry.path}
          </p>
        )}
      </div>
      {/* Timestamp */}
      <div className="shrink-0 text-right">
        {deployedAtFmt && (
          <time
            dateTime={entry.deployedAt}
            className="text-xs text-gray-400"
            title={entry.deployedAt}
          >
            {deployedAtFmt}
          </time>
        )}
        {entry.targetRevision && (
          <p className="mt-0.5 font-mono text-xs text-gray-300">
            {entry.targetRevision}
          </p>
        )}
      </div>
    </li>
  );
}

// ---------------------------------------------------------------------------
// Tab: Previews
// ---------------------------------------------------------------------------

function PreviewsTab({
  previewEnvs,
  onDeletePreview,
}: {
  previewEnvs: AppEnvironmentSummary[];
  onDeletePreview: (previewName: string) => void;
}) {
  const [confirmingDelete, setConfirmingDelete] = useState<string | null>(null);

  if (previewEnvs.length === 0) {
    return (
      <div className="rounded-xl border border-dashed border-gray-200 bg-white px-6 py-12 text-center">
        <p className="text-sm font-medium text-gray-500">
          No active preview environments
        </p>
        <p className="mt-1 text-xs text-gray-400">
          Use the Preview button above to create one.
        </p>
      </div>
    );
  }

  return (
    <div className="divide-y divide-gray-50 rounded-xl border border-gray-200 bg-white">
      {previewEnvs.map((env) => {
        const previewName = env.preview?.previewName ?? env.envName;
        const isConfirming = confirmingDelete === previewName;
        return (
          <div
            key={env.envName}
            className="flex items-center justify-between px-5 py-3"
          >
            <div className="min-w-0">
              <span className="text-sm font-medium text-gray-900">
                {previewName}
              </span>
              <span className="ml-2 font-mono text-xs text-gray-400">
                {env.namespace}
              </span>
              {env.preview?.createdAt && (
                <span className="ml-3 text-xs text-gray-400">
                  {new Date(env.preview.createdAt).toLocaleDateString(
                    undefined,
                    { month: "short", day: "numeric" },
                  )}
                </span>
              )}
            </div>
            <div className="ml-4 flex flex-shrink-0 items-center gap-3">
              <StatusBadge status={env.status.phase} />
              {env.urls[0] && (
                <a
                  href={env.urls[0]}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-xs text-blue-600 hover:underline"
                >
                  Open ↗
                </a>
              )}
              {isConfirming ? (
                <div className="flex items-center gap-1.5">
                  <span className="text-xs text-gray-500">Delete?</span>
                  <button
                    onClick={() => {
                      setConfirmingDelete(null);
                      onDeletePreview(previewName);
                    }}
                    className="rounded-md bg-red-600 px-2 py-0.5 text-xs font-medium text-white hover:bg-red-700"
                  >
                    Yes
                  </button>
                  <button
                    onClick={() => setConfirmingDelete(null)}
                    className="rounded-md border border-gray-200 px-2 py-0.5 text-xs font-medium text-gray-600 hover:bg-gray-50"
                  >
                    No
                  </button>
                </div>
              ) : (
                <button
                  onClick={() => setConfirmingDelete(previewName)}
                  className="rounded p-1 text-gray-300 transition-colors hover:bg-red-50 hover:text-red-500"
                  title="Delete preview"
                >
                  <svg
                    className="h-3.5 w-3.5"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    strokeWidth={1.5}
                  >
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0"
                    />
                  </svg>
                </button>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab: Logs
// ---------------------------------------------------------------------------

const LOGS_TAIL_LINES = 200;
const LOGS_POLL_INTERVAL_MS = 30_000;

interface LogsTabProps {
  project: string;
  appName: string;
  selectedEnvName: string;
  components: ComponentSummary[];
  environments: AppEnvironmentSummary[];
}

function LogsTab({
  project,
  appName,
  selectedEnvName,
  components,
  environments,
}: LogsTabProps) {
  const [env, setEnv] = useState(
    selectedEnvName || environments[0]?.envName || "",
  );
  const [component, setComponent] = useState("");
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [pod, setPod] = useState("");
  const [container, setContainer] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<AppLogsResponse | null>(null);
  const scrollRef = useRef<HTMLPreElement>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Keep local env in sync when the parent environment switcher changes.
  useEffect(() => {
    if (selectedEnvName) setEnv(selectedEnvName);
  }, [selectedEnvName]);

  const loadLogs = useCallback(async () => {
    if (!env) return;
    setLoading(true);
    setError(null);
    try {
      const data = await fetchAppLogs(project, appName, {
        environment: env,
        component: component || undefined,
        pod: pod || undefined,
        container: container || undefined,
        tailLines: LOGS_TAIL_LINES,
      });
      setResult(data);
      requestAnimationFrame(() => {
        scrollRef.current?.scrollTo(0, scrollRef.current.scrollHeight);
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to fetch logs");
      setResult(null);
    } finally {
      setLoading(false);
    }
  }, [project, appName, env, component, pod, container]);

  // Initial fetch + polling.
  useEffect(() => {
    loadLogs();
    pollRef.current = setInterval(loadLogs, LOGS_POLL_INTERVAL_MS);
    return () => {
      if (pollRef.current !== null) clearInterval(pollRef.current);
    };
  }, [loadLogs]);

  const multiComponent = components.length > 1;

  return (
    <div className="flex flex-col gap-4">
      {/* Primary selectors: environment (+ component when multiple) */}
      <div className="flex flex-wrap items-end gap-3 rounded-lg border border-gray-100 bg-gray-50 px-4 py-3">
        <div>
          <label className="mb-1 block text-xs font-medium text-gray-500">
            Environment
          </label>
          <select
            value={env}
            onChange={(e) => {
              setEnv(e.target.value);
              setPod("");
              setContainer("");
            }}
            className="rounded-md border border-gray-300 bg-white px-2.5 py-1.5 text-xs text-gray-900 focus:border-gray-500 focus:outline-none focus:ring-1 focus:ring-gray-500"
          >
            {environments.map((e) => (
              <option key={e.envName} value={e.envName}>
                {e.envName}
              </option>
            ))}
          </select>
        </div>

        {multiComponent && (
          <div>
            <label className="mb-1 block text-xs font-medium text-gray-500">
              Component
            </label>
            <select
              value={component}
              onChange={(e) => {
                setComponent(e.target.value);
                setPod("");
                setContainer("");
              }}
              className="rounded-md border border-gray-300 bg-white px-2.5 py-1.5 text-xs text-gray-900 focus:border-gray-500 focus:outline-none focus:ring-1 focus:ring-gray-500"
            >
              <option value="">All components</option>
              {components.map((c) => (
                <option key={c.name} value={c.name}>
                  {c.name} ({c.type})
                </option>
              ))}
            </select>
          </div>
        )}

        <button
          onClick={loadLogs}
          disabled={loading || !env}
          className="ml-auto inline-flex items-center gap-1.5 rounded-md border border-gray-200 bg-white px-2.5 py-1.5 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-50 disabled:opacity-50"
          title="Refresh logs"
        >
          <svg
            className={`h-3.5 w-3.5 ${loading ? "animate-spin" : ""}`}
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
            strokeWidth={2}
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182"
            />
          </svg>
          Refresh
        </button>
      </div>

      {/* Advanced: pod / container — collapsible */}
      <div>
        <button
          onClick={() => setShowAdvanced((v) => !v)}
          className="flex items-center gap-1 text-xs text-gray-400 hover:text-gray-600"
        >
          <svg
            className={`h-3 w-3 transition-transform ${showAdvanced ? "rotate-90" : ""}`}
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
            strokeWidth={2}
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="m9 18 6-6-6-6"
            />
          </svg>
          Advanced (pod / container)
        </button>
        {showAdvanced && (
          <div className="mt-2 flex flex-wrap gap-3 rounded-lg border border-gray-100 bg-gray-50/60 px-4 py-3">
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-500">
                Pod
              </label>
              <input
                type="text"
                value={pod}
                onChange={(e) => setPod(e.target.value)}
                placeholder="auto-select"
                className="w-40 rounded-md border border-gray-300 bg-white px-2.5 py-1.5 text-xs text-gray-900 placeholder-gray-400 focus:border-gray-500 focus:outline-none focus:ring-1 focus:ring-gray-500"
              />
            </div>
            <div>
              <label className="mb-1 block text-xs font-medium text-gray-500">
                Container
              </label>
              <input
                type="text"
                value={container}
                onChange={(e) => setContainer(e.target.value)}
                placeholder="default"
                className="w-36 rounded-md border border-gray-300 bg-white px-2.5 py-1.5 text-xs text-gray-900 placeholder-gray-400 focus:border-gray-500 focus:outline-none focus:ring-1 focus:ring-gray-500"
              />
            </div>
          </div>
        )}
      </div>

      {/* Resolved runtime unit info */}
      {result && (
        <div className="flex flex-wrap items-center gap-3 text-xs text-gray-400">
          <span>
            pod:{" "}
            <span className="font-mono text-gray-600">{result.pod}</span>
          </span>
          <span>
            container:{" "}
            <span className="font-mono text-gray-600">{result.container}</span>
          </span>
          {result.namespace && (
            <span>
              namespace:{" "}
              <span className="font-mono text-gray-600">
                {result.namespace}
              </span>
            </span>
          )}
        </div>
      )}

      {/* Log output */}
      <div className="relative min-h-64 overflow-hidden rounded-xl border border-gray-200 bg-gray-950">
        {loading && !result && <LogsTabSkeleton />}

        {error && (
          <div className="flex h-64 flex-col items-center justify-center px-6 text-center">
            <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-red-900/30">
              <svg
                className="h-5 w-5 text-red-400"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                strokeWidth={1.5}
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z"
                />
              </svg>
            </div>
            <p className="text-sm font-medium text-gray-300">{error}</p>
            <button
              onClick={loadLogs}
              className="mt-3 text-sm text-blue-400 hover:text-blue-300"
            >
              Try again
            </button>
          </div>
        )}

        {!loading && !error && result && result.logs.length === 0 && (
          <div className="flex h-64 flex-col items-center justify-center px-6 text-center">
            <p className="text-sm font-medium text-gray-500">No log output</p>
            <p className="mt-1 text-xs text-gray-600">
              The container has not produced any logs yet.
            </p>
          </div>
        )}

        {result && result.logs.length > 0 && (
          <pre
            ref={scrollRef}
            className="max-h-[32rem] overflow-auto px-4 py-3 font-mono text-xs leading-5 text-gray-200"
          >
            {result.logs}
          </pre>
        )}

        {loading && result && (
          <div className="absolute right-4 top-3">
            <div className="h-4 w-4 animate-spin rounded-full border-2 border-gray-400 border-t-transparent" />
          </div>
        )}
      </div>

      <p className="text-right text-xs text-gray-400">
        Showing last {LOGS_TAIL_LINES} lines &middot; auto-refreshes every{" "}
        {LOGS_POLL_INTERVAL_MS / 1000}s
      </p>
    </div>
  );
}

function LogsTabSkeleton() {
  return (
    <div className="space-y-2 px-4 py-3">
      {Array.from({ length: 12 }).map((_, i) => (
        <div
          key={i}
          className="h-3.5 animate-pulse rounded bg-gray-800"
          style={{ width: `${40 + (i * 7) % 50}%` }}
        />
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab: Traffic (placeholder)
// ---------------------------------------------------------------------------

function TrafficTab() {
  return (
    <div className="rounded-xl border border-dashed border-gray-200 bg-white px-6 py-12 text-center">
      <p className="text-sm font-medium text-gray-500">
        Traffic management coming soon
      </p>
      <p className="mt-1 text-xs text-gray-400">
        Canary and blue/green traffic controls will appear here.
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Subcomponents
// ---------------------------------------------------------------------------

/**
 * Derives whether a component type exposes an inbound network surface.
 * web → exposed (receives traffic); worker / cron → internal (no inbound route).
 */
function componentVisibilityBadge(type: ComponentSummary["type"]) {
  if (type === "web") {
    return (
      <span className="rounded-full bg-blue-50 px-2 py-0.5 text-xs font-medium text-blue-600">
        exposed
      </span>
    );
  }
  return (
    <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-500">
      internal
    </span>
  );
}

/**
 * Runtime components section.
 *
 * Visibility rules:
 *  - 0 components → renders nothing (topology not yet derived).
 *  - 1+ components → expanded by default so the runtime topology is always
 *    immediately visible without requiring extra interaction.
 */
function ComponentsTable({
  components,
  currentEnv,
}: {
  components: ComponentSummary[];
  currentEnv: AppEnvironmentSummary | null;
}) {
  const isMulti = components.length > 1;
  const [expanded, setExpanded] = useState(true);

  if (components.length === 0) return null;

  const phase = currentEnv?.status.phase ?? "not_deployed";
  const totalReplicas = currentEnv?.status.replicas ?? 0;
  const availableReplicas = currentEnv?.status.available ?? 0;

  // Replica attribution: only unambiguous when exactly one scalable component
  // (web / worker) exists. For multi-component apps we surface the aggregate
  // in the section header and show "—" per row to avoid misleading fractions.
  const scalableComponents = components.filter(
    (c) => c.type === "web" || c.type === "worker",
  );
  const singleScalable = scalableComponents.length === 1;

  function replicaLabel(comp: ComponentSummary): string {
    if (comp.type === "cron") return "—";
    if (singleScalable && totalReplicas > 0) {
      return `${availableReplicas}/${totalReplicas}`;
    }
    return "—";
  }

  const headerTitle = "Runtime components";

  return (
    <div className="rounded-xl border border-gray-200 bg-white">
      {/* Header doubles as a toggle for compact/expand control */}
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
        className="flex w-full items-center justify-between border-b border-gray-100 px-5 py-3 text-left"
      >
        <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
          {headerTitle}
        </h2>
        <div className="flex items-center gap-2">
          {isMulti && totalReplicas > 0 && (
            <span
              className="text-xs text-gray-400"
              title="Aggregate replicas across all scalable components"
            >
              {availableReplicas}/{totalReplicas} replicas
            </span>
          )}
          {isMulti && (
            <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-500">
              {components.length}
            </span>
          )}
          <svg
            className={`h-3.5 w-3.5 text-gray-400 transition-transform ${expanded ? "rotate-90" : ""}`}
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
            strokeWidth={2}
            aria-hidden="true"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              d="m9 18 6-6-6-6"
            />
          </svg>
        </div>
      </button>

      {expanded && (
        <div className="divide-y divide-gray-50">
          {components.map((comp) => {
            const replicas = replicaLabel(comp);
            return (
              <div
                key={comp.name}
                className="flex items-center justify-between px-5 py-2.5"
              >
                {/* Left: name + type + visibility */}
                <div className="flex min-w-0 items-center gap-2">
                  <span className="font-mono text-sm text-gray-900">
                    {comp.name}
                  </span>
                  <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs capitalize text-gray-500">
                    {comp.type}
                  </span>
                  {componentVisibilityBadge(comp.type)}
                </div>

                {/* Right: replicas + status + preview eligibility */}
                <div className="ml-4 flex flex-shrink-0 items-center gap-3">
                  {replicas !== "—" && (
                    <span className="text-xs text-gray-400">
                      {replicas}{" "}
                      <span className="text-gray-300">replicas</span>
                    </span>
                  )}
                  <StatusBadge status={phase} />
                  {!comp.enabledInPreview && (
                    <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-500">
                      no preview
                    </span>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function QuickStat({
  label,
  value,
  mono,
}: {
  label: string;
  value: string;
  mono?: boolean;
}) {
  return (
    <div className="rounded-xl border border-gray-200 bg-white px-4 py-3">
      <p className="text-xs text-gray-400">{label}</p>
      <p
        className={`mt-0.5 text-lg font-semibold text-gray-900 ${mono ? "font-mono" : ""}`}
      >
        {value}
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab: Env Vars
// ---------------------------------------------------------------------------

// Source badge colours matching the hierarchy levels
const sourceBadge: Record<string, { bg: string; label: string }> = {
  org: { bg: "bg-blue-50 text-blue-700", label: "Org" },
  environment: { bg: "bg-purple-50 text-purple-700", label: "Env-type" },
  project: { bg: "bg-amber-50 text-amber-700", label: "Project" },
  app: { bg: "bg-emerald-50 text-emerald-700", label: "App" },
  "app-environment": { bg: "bg-gray-100 text-gray-700", label: "App-Env" },
};

function SourceBadge({ source }: { source: string }) {
  const cfg = sourceBadge[source] ?? { bg: "bg-gray-100 text-gray-600", label: source };
  return (
    <span className={`inline-block rounded-full px-2 py-0.5 text-xs font-medium ${cfg.bg}`}>
      {cfg.label}
    </span>
  );
}

function ResolvedEnvPanel({
  project,
  appName,
  envName,
}: {
  project: string;
  appName: string;
  envName: string;
}) {
  const [vars, setVars] = useState<ResolvedEnvVar[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getResolvedEnvConfig(project, appName, envName)
      .then((res) => setVars(res.vars ?? []))
      .catch((err) => setError(err instanceof Error ? err.message : "Failed to load"))
      .finally(() => setLoading(false));
  }, [project, appName, envName]);

  useEffect(() => { load(); }, [load]);

  return (
    <div className="rounded-lg border border-gray-200 bg-white">
      <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
        <div>
          <div className="flex items-center gap-2">
            <h3 className="text-sm font-medium text-gray-900">Resolved variables</h3>
            {!loading && vars.length > 0 && (
              <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600">
                {vars.length}
              </span>
            )}
          </div>
          <p className="mt-0.5 text-xs text-gray-500">
            Merged view for <span className="font-mono font-medium">{envName}</span> — shows which hierarchy level wins each key.
          </p>
        </div>
        <button
          onClick={load}
          disabled={loading}
          className="rounded border border-gray-200 px-2.5 py-1 text-xs text-gray-600 hover:bg-gray-50 disabled:opacity-50"
        >
          {loading ? "Loading…" : "Refresh"}
        </button>
      </div>
      <div className="px-6 py-4">
        {loading ? (
          <div className="space-y-2">
            {[0, 1, 2].map((i) => (
              <div key={i} className="h-7 animate-pulse rounded bg-gray-100" />
            ))}
          </div>
        ) : error ? (
          <p className="text-sm text-red-600">{error}</p>
        ) : vars.length === 0 ? (
          <p className="text-sm italic text-gray-400">No variables at this environment.</p>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="text-left text-xs font-medium uppercase tracking-wider text-gray-400">
                <th className="pb-2 pr-4">Key</th>
                <th className="pb-2 pr-4">Value</th>
                <th className="pb-2">Source</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {vars.map((v) => (
                <tr key={v.key} className="hover:bg-gray-50">
                  <td className="py-1.5 pr-4 font-mono text-xs font-medium text-gray-900">
                    {v.key}
                  </td>
                  <td className="py-1.5 pr-4 font-mono text-xs text-gray-600 max-w-xs truncate">
                    {v.isSecret ? (
                      <span className="italic text-gray-400">••••••</span>
                    ) : (
                      v.value || <span className="italic text-gray-300">(empty)</span>
                    )}
                  </td>
                  <td className="py-1.5">
                    <SourceBadge source={v.source} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

// AppClusterSecrets renders one SecretEditor per cluster bound to the given
// environment. Cluster overrides are per-(env, cluster) — the items live in
// the env vault — so the editor needs the env context.
function AppClusterSecrets({
  project,
  appName,
  env,
}: {
  project: string;
  appName: string;
  env: string;
}) {
  const [boundClusters, setBoundClusters] = useState<string[]>([]);

  useEffect(() => {
    let cancelled = false;
    listOrgEnvironments()
      .then((resp) => {
        if (cancelled) return;
        const orgEnv = (resp.environments || []).find((e) => e.name === env);
        const refs = orgEnv?.clusterRefs?.length
          ? orgEnv.clusterRefs
          : orgEnv?.activeClusterRef
            ? [orgEnv.activeClusterRef]
            : [];
        setBoundClusters(refs);
      })
      .catch(() => {
        /* best-effort: no bound clusters resolved */
      });
    return () => {
      cancelled = true;
    };
  }, [env]);

  if (boundClusters.length === 0) {
    return null;
  }

  return (
    <>
      {boundClusters.map((cluster) => (
        <SecretEditor
          key={`cluster-secrets-${env}-${cluster}`}
          title={`"${cluster}" cluster secrets in "${env}"`}
          description={`Secrets for this app only when ${env} runs on the ${cluster} cluster (cluster scope, stored in the "${env}" env vault). Override global and env secrets.`}
          fetchFn={() => listAppClusterSecretKeys(project, appName, env, cluster)}
          upsertFn={(entries) =>
            upsertAppClusterSecrets(project, appName, env, cluster, entries)
          }
          deleteFn={(key) =>
            deleteAppClusterSecretKey(project, appName, env, cluster, key)
          }
        />
      ))}
    </>
  );
}

function ResolvedSecretsPanel({
  project,
  appName,
  envName,
}: {
  project: string;
  appName: string;
  envName: string;
}) {
  const [secrets, setSecrets] = useState<ResolvedSecretEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    setError(null);
    getResolvedSecrets(project, appName, envName)
      .then((res) => setSecrets(res.secrets ?? []))
      .catch((err) => setError(err instanceof Error ? err.message : "Failed to load"))
      .finally(() => setLoading(false));
  }, [project, appName, envName]);

  useEffect(() => { load(); }, [load]);

  return (
    <div className="rounded-lg border border-gray-200 bg-white">
      <div className="flex items-center justify-between border-b border-gray-100 px-6 py-4">
        <div>
          <div className="flex items-center gap-2">
            <h3 className="text-sm font-medium text-gray-900">Resolved secrets</h3>
            {!loading && secrets.length > 0 && (
              <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-600">
                {secrets.length}
              </span>
            )}
          </div>
          <p className="mt-0.5 text-xs text-gray-500">
            Merged secret keys for <span className="font-mono font-medium">{envName}</span> — shows which hierarchy level provides each key. Values are never shown.
          </p>
        </div>
        <button
          onClick={load}
          disabled={loading}
          className="rounded border border-gray-200 px-2.5 py-1 text-xs text-gray-600 hover:bg-gray-50 disabled:opacity-50"
        >
          {loading ? "Loading\u2026" : "Refresh"}
        </button>
      </div>
      <div className="px-6 py-4">
        {loading ? (
          <div className="space-y-2">
            {[0, 1].map((i) => (
              <div key={i} className="h-7 animate-pulse rounded bg-gray-100" />
            ))}
          </div>
        ) : error ? (
          <p className="text-sm text-red-600">{error}</p>
        ) : secrets.length === 0 ? (
          <p className="text-sm italic text-gray-400">No secrets at this environment.</p>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="text-left text-xs font-medium uppercase tracking-wider text-gray-400">
                <th className="pb-2 pr-4">Key</th>
                <th className="pb-2 pr-4">Value</th>
                <th className="pb-2">Source</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {secrets.map((s) => (
                <tr key={s.key} className="hover:bg-gray-50">
                  <td className="py-1.5 pr-4 font-mono text-xs font-medium text-gray-900">
                    {s.key}
                  </td>
                  <td className="py-1.5 pr-4 font-mono text-xs text-gray-400 italic">
                    ••••••
                  </td>
                  <td className="py-1.5">
                    <SourceBadge source={s.source} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function EnvVarsTab({
  project,
  appName,
  environments,
  selectedEnvName,
  onSelectEnv,
}: {
  project: string;
  appName: string;
  environments: import("../types").AppEnvironmentSummary[];
  selectedEnvName: string | null;
  onSelectEnv: (name: string) => void;
}) {
  // Only non-preview environments are meaningful for per-env env-var overrides.
  const stableEnvs = environments.filter((e) => e.envType !== "preview");
  const activeEnv = selectedEnvName ?? stableEnvs[0]?.envName ?? null;

  const fetchAppCfg = useCallback(
    () => getAppEnvConfig(project, appName),
    [project, appName],
  );
  const saveAppCfg = useCallback(
    (cfg: EnvConfig) => updateAppEnvConfig(project, appName, cfg),
    [project, appName],
  );
  const fetchEnvCfg = useCallback(
    () =>
      activeEnv
        ? getAppEnvEnvConfig(project, appName, activeEnv)
        : Promise.resolve<EnvConfig>({}),
    [project, appName, activeEnv],
  );
  const saveEnvCfg = useCallback(
    (cfg: EnvConfig) =>
      activeEnv
        ? updateAppEnvEnvConfig(project, appName, activeEnv, cfg)
        : Promise.resolve(null),
    [project, appName, activeEnv],
  );

  return (
    <div className="space-y-6">
      {/* App-level variables — always visible */}
      <EnvConfigEditor
        title="App-level variables"
        description="Applied to this app in all environments. Overrides org, environment-type, and project defaults."
        fetchFn={fetchAppCfg}
        saveFn={saveAppCfg}
      />
      <SecretEditor
        title="Global secrets"
        description="App secrets that are the same in every environment (global scope). Overridden by env- and cluster-scoped secrets."
        fetchFn={() => listAppGlobalSecretKeys(project, appName)}
        upsertFn={(entries) => upsertAppGlobalSecrets(project, appName, entries)}
        deleteFn={(key) => deleteAppGlobalSecretKey(project, appName, key)}
      />

      {/* Per-environment section */}
      <div className="space-y-3">
        <div className="flex items-center gap-3">
          <span className="text-sm font-medium text-gray-700">Environment</span>
          <div className="flex gap-1.5">
            {stableEnvs.map((env) => (
              <button
                key={env.envName}
                onClick={() => onSelectEnv(env.envName)}
                className={`rounded-full px-3 py-0.5 text-xs font-medium transition-colors ${
                  activeEnv === env.envName
                    ? "bg-gray-900 text-white"
                    : "border border-gray-200 text-gray-600 hover:bg-gray-50"
                }`}
              >
                {env.envName}
              </button>
            ))}
          </div>
        </div>

        {activeEnv ? (
          <>
            <EnvConfigEditor
              key={`appenv-${activeEnv}`}
              title={`"${activeEnv}" overrides`}
              description={`Variables that apply only when the app runs in ${activeEnv}. Overrides all upper levels.`}
              fetchFn={fetchEnvCfg}
              saveFn={saveEnvCfg}
            />
            <SecretEditor
              key={`secrets-${activeEnv}`}
              title={`"${activeEnv}" secrets`}
              description={`Secrets for the ${activeEnv} environment (env scope). Override global secrets; overridden by cluster-scoped secrets.`}
              fetchFn={() => listAppEnvSecretKeys(project, appName, activeEnv)}
              upsertFn={(entries) => upsertAppEnvSecrets(project, appName, activeEnv, entries)}
              deleteFn={(key) => deleteAppEnvSecretKey(project, appName, activeEnv, key)}
            />
            {/* Cluster-scoped overrides for this env's bound cluster(s) */}
            <AppClusterSecrets
              key={`cluster-secrets-${activeEnv}`}
              project={project}
              appName={appName}
              env={activeEnv}
            />
            <ResolvedEnvPanel
              key={`resolved-${activeEnv}`}
              project={project}
              appName={appName}
              envName={activeEnv}
            />
            <ResolvedSecretsPanel
              key={`resolved-secrets-${activeEnv}`}
              project={project}
              appName={appName}
              envName={activeEnv}
            />
          </>
        ) : (
          <p className="text-sm text-gray-400 italic">
            No stable environments found. Deploy the app first.
          </p>
        )}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Skeleton
// ---------------------------------------------------------------------------

function DetailSkeleton() {
  return (
    <div className="space-y-6">
      <div className="h-4 w-40 animate-pulse rounded bg-gray-100" />
      <div className="flex items-start justify-between">
        <div className="space-y-2">
          <div className="h-8 w-56 animate-pulse rounded bg-gray-100" />
          <div className="h-5 w-72 animate-pulse rounded bg-gray-50" />
        </div>
        <div className="flex gap-2">
          {[1, 2, 3, 4].map((n) => (
            <div
              key={n}
              className="h-9 w-24 animate-pulse rounded-lg bg-gray-100"
            />
          ))}
        </div>
      </div>
      <div className="flex gap-2">
        {[1, 2].map((n) => (
          <div key={n} className="h-7 w-20 animate-pulse rounded-full bg-gray-100" />
        ))}
      </div>
      <div className="h-px w-full bg-gray-200" />
      <div className="grid gap-4 sm:grid-cols-4">
        {[1, 2, 3, 4].map((n) => (
          <div key={n} className="h-16 animate-pulse rounded-xl bg-gray-50" />
        ))}
      </div>
      <div className="h-40 animate-pulse rounded-xl bg-gray-50" />
    </div>
  );
}
