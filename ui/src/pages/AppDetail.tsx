import { Suspense, lazy, useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";

import { fetchAppLogs, getApp, getAppDeploymentHistory, getAppEnvironment, getKargoAppPipeline, getKargoPromotionStatus, previewAppValues, pinAppEnv, promoteApp, resumeAppEnv, suspendAppEnv, syncApp, deleteApp, renameApp, undeployAppEnv, unpinAppEnv, updateApp, upgradeAppTemplate } from "../lib/apps";
import type { ClusterValueOverride, UpdateAppRequest } from "../lib/apps";
import { listConfigVariables } from "../lib/configVars";
import type { ConfigVariables } from "../lib/configVars";
import { parseYamlOverlay, stringifyOverlay } from "../lib/yamlDoc";

// CodeMirror is heavy; only the values editor needs it.
const ValuesEditor = lazy(() => import("../components/ValuesEditor"));
import { listTemplateVersions, fetchTemplateEffectiveValues, fetchTemplates } from "../lib/templates";
import type { TemplateVersionInfo, TemplateImage, AppImageBinding, TemplateSummary } from "../types";
import {
  ComposeComponents,
  type ComponentDraft,
  draftFromSummary,
  toComponentCreate,
} from "../components/ComposeComponents";
import { createAppPreview, deleteAppPreview } from "../lib/previews";
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
  StatusBadge,
  statusStyles,
  fallbackStatus,
  overallPhase,
} from "../components/StatusBadge";
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
  listAppPreviewSecretKeys,
  upsertAppPreviewSecrets,
  deleteAppPreviewSecretKey,
  getResolvedSecrets,
} from "../lib/secrets";
import { listOrgEnvironments } from "../lib/settings";
import type { OrgEnvironment } from "../lib/settings";
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
  { id: "envvars", label: "Variables" },
];

// ---------------------------------------------------------------------------
// Status helpers
// ---------------------------------------------------------------------------

// StatusBadge, statusStyles, fallbackStatus, and overallPhase live in the shared
// ../components/StatusBadge module (imported above) so all pages stay consistent.

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
  // Promotion advances freight between Kargo Stages (stable→stable only).
  // Previews have no Stage: their build reaches staging by merging the PR
  // (auto-promotes) or via Pin (deploy without merging) — not by promotion.
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
        // available=false → no Kargo status reader; keep the last known phase.
        if (status.available === false) return;
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
        // available=false → no Kargo pipeline reader; degrade to plain switcher.
        if (!cancelled) setPipeline(data.available === false ? null : data);
      } catch {
        // Kargo not configured / request failed — degrade to plain env switcher.
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

  // Group previews under the stable env each was cloned from. Previews with an
  // unknown/empty baseEnv (older previews) fall back to the first stable env —
  // the default preview base.
  const defaultBaseEnv = nonPreviewEnvs[0]?.envName;
  const knownEnvs = new Set(nonPreviewEnvs.map((e) => e.envName));
  const previewsByBase: Record<string, AppEnvironmentSummary[]> = {};
  for (const p of previewEnvs) {
    const declared = p.preview?.baseEnv;
    const base = declared && knownEnvs.has(declared) ? declared : defaultBaseEnv;
    if (!base) continue;
    (previewsByBase[base] ??= []).push(p);
  }

  return (
    <div className="space-y-2">
      {/* Pipeline row: stable envs connected by promotion arrows; each env
          carries its previews stacked beneath it. */}
      <div className="flex flex-wrap items-start gap-0">
        {nonPreviewEnvs.map((env, i) => {
          const stage = stageMap[env.envName];
          const isSelected = selectedEnvName === env.envName;
          const phaseCfg = stage
            ? (stagePhaseCfg[stage.phase] ?? fallbackStageCfg)
            : fallbackStageCfg;
          const runtimeCfg = statusStyles[env.status.phase] ?? fallbackStatus;

          const envPreviews = previewsByBase[env.envName] ?? [];
          return (
            <div key={env.envName} className="flex flex-col gap-1.5">
              <div className="flex items-center">
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

                  {/* Pinned badge: env held to a specific preview image, CD paused. */}
                  {env.pinnedTag && (
                    <span
                      title={`Pinned to ${env.pinnedFrom || env.pinnedTag} — auto-promote paused`}
                      className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-medium ${
                        isSelected ? "bg-white/10 text-white/80" : "bg-purple-100 text-purple-700"
                      }`}
                    >
                      📌 {env.pinnedFrom || "pinned"}
                    </span>
                  )}

                  {/* Suspended badge: env's workload scaled down (still published). */}
                  {env.suspended && (
                    <span
                      title="Suspended — workload scaled down; resume to bring it back"
                      className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-medium ${
                        isSelected ? "bg-white/10 text-white/80" : "bg-amber-100 text-amber-700"
                      }`}
                    >
                      ⏸ suspended
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

              {/* Previews cloned from this env, stacked beneath its card. */}
              {envPreviews.length > 0 && (
                <div className="flex flex-col items-start gap-1">
                  {envPreviews.map((p) => (
                    <button
                      key={p.envName}
                      onClick={() => onSelect(p.envName)}
                      title={`Preview cloned from ${env.envName}`}
                      className={`inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-xs font-medium transition-colors ${
                        selectedEnvName === p.envName
                          ? "bg-purple-700 text-white"
                          : "bg-purple-50 text-purple-700 hover:bg-purple-100"
                      }`}
                    >
                      <span
                        className={
                          selectedEnvName === p.envName
                            ? "text-purple-200"
                            : "text-purple-300"
                        }
                      >
                        ↳
                      </span>
                      {p.preview?.previewName ?? p.envName}
                    </button>
                  ))}
                </div>
              )}
            </div>
          );
        })}
      </div>
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
  const [previewBaseEnv, setPreviewBaseEnv] = useState("");
  const [previewImageTag, setPreviewImageTag] = useState("");
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

  // Rename app
  const [showRename, setShowRename] = useState(false);
  const [renameInput, setRenameInput] = useState("");
  const [renaming, setRenaming] = useState(false);
  const [renameError, setRenameError] = useState<string | null>(null);

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
    if (!project || !appName) return;
    deleteAppPreview(project, appName, previewName)
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

  const nonPreviewEnvs = data.environments
    .filter((e) => e.envType !== "preview")
    .sort((a, b) => a.order - b.order || a.envName.localeCompare(b.envName));
  const previewEnvs = data.environments.filter((e) => e.envType === "preview");

  // Each preview belongs to the stable env it clones (its base env); previews
  // with an unknown/empty base env (older ones) fall back to the first stable
  // env. The env-scoped tabs (incl. Previews) show only the previews of the env
  // in context — never previews belonging to another env.
  const defaultBaseEnv = nonPreviewEnvs[0]?.envName;
  const knownEnvNames = new Set(nonPreviewEnvs.map((e) => e.envName));
  const baseEnvOf = (p: AppEnvironmentSummary): string | undefined => {
    const declared = p.preview?.baseEnv;
    return declared && knownEnvNames.has(declared) ? declared : defaultBaseEnv;
  };
  // The base env in context: the selected stable env, or the base env of the
  // selected preview.
  const selectedEnvObj = data.environments.find(
    (e) => e.envName === selectedEnvName,
  );
  const currentBaseEnv =
    selectedEnvObj?.envType === "preview"
      ? baseEnvOf(selectedEnvObj)
      : (selectedEnvName ?? defaultBaseEnv);
  const visiblePreviewEnvs = previewEnvs.filter(
    (p) => baseEnvOf(p) === currentBaseEnv,
  );

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
            {(data.components?.length ?? 0) > 1 ? (
              // Composed app: a single template link would be misleading (it's
              // just the primary). Show the composed makeup; per-component
              // templates are listed in the Components section below.
              <span className="inline-flex items-center gap-2">
                <span className="rounded-full bg-indigo-50 px-2 py-0.5 text-xs font-medium text-indigo-600">
                  composed
                </span>
                <span className="text-gray-500">
                  {data.components?.length} components
                </span>
              </span>
            ) : (
              <>
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
              </>
            )}
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
            disabled={data?.previewsEnabled === false}
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm transition-colors hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-50"
            title={
              data?.previewsEnabled === false
                ? "Previews are disabled for this app (enable them in the values/settings tab)"
                : "Create preview environment"
            }
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
            // Direct-delivery apps have no promotion — each env deploys from values.
            if (data.deliveryMode === "direct") return null;
            const promotion = getPromoteTarget(currentEnv, data.environments);
            if (!promotion) return null;
            // A pinned target is frozen — promotion is paused until it's unpinned.
            const targetEnv = data.environments.find((e) => e.envName === promotion.target);
            if (targetEnv?.pinnedTag) {
              return (
                <button
                  disabled
                  className="inline-flex cursor-not-allowed items-center gap-1.5 rounded-lg bg-gray-200 px-3.5 py-2 text-sm font-medium text-gray-400 shadow-sm"
                  title={`${promotion.target} is pinned to ${targetEnv.pinnedFrom || targetEnv.pinnedTag}; unpin it first`}
                >
                  {icons.rocket}
                  Promote to {promotion.target}
                </button>
              );
            }
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
              setRenameInput("");
              setRenameError(null);
              setShowRename(true);
            }}
            className="inline-flex items-center gap-1.5 rounded-lg border border-gray-300 bg-white px-3.5 py-2 text-sm font-medium text-gray-700 shadow-sm transition-colors hover:bg-gray-50"
            title="Rename this app"
          >
            <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M16.862 4.487l1.687-1.688a1.875 1.875 0 1 1 2.652 2.652L10.582 16.07a4.5 4.5 0 0 1-1.897 1.13L6 18l.8-2.685a4.5 4.5 0 0 1 1.13-1.897l8.932-8.931Z" />
            </svg>
            Rename
          </button>
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
                Deploy <span className="font-medium">{appName}</span> to an
                isolated preview namespace. It clones the base env's cluster,
                config and secrets, then applies this app's preview overrides
                (Values dropdown + Variables → "Preview band").
              </p>
              <form
                className="mt-3 flex items-end gap-2"
                onSubmit={async (e) => {
                  e.preventDefault();
                  if (!previewName.trim() || !project || !appName) return;
                  setPreviewSubmitting(true);
                  setPreviewError(null);
                  try {
                    await createAppPreview(project, appName, {
                      name: previewName.trim(),
                      // Omit baseEnv to default to the first stable env (staging).
                      baseEnv: previewBaseEnv || undefined,
                      // Omit imageTag to inherit the base env's image.
                      imageTag: previewImageTag.trim() || undefined,
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
                {(() => {
                  const stableEnvNames = (data?.environments ?? [])
                    .filter((e) => e.envType !== "preview")
                    .map((e) => e.envName);
                  if (stableEnvNames.length === 0) return null;
                  return (
                    <div>
                      <label
                        htmlFor="preview-base-env"
                        className="mb-1 block text-xs font-medium text-gray-700"
                      >
                        Base env
                      </label>
                      <select
                        id="preview-base-env"
                        value={previewBaseEnv || stableEnvNames[0]}
                        onChange={(e) => setPreviewBaseEnv(e.target.value)}
                        className="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-900 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                      >
                        {stableEnvNames.map((name) => (
                          <option key={name} value={name}>
                            {name}
                          </option>
                        ))}
                      </select>
                    </div>
                  );
                })()}
                <div>
                  <label
                    htmlFor="preview-image-tag"
                    className="mb-1 block text-xs font-medium text-gray-700"
                  >
                    Image tag <span className="text-gray-400">(optional)</span>
                  </label>
                  <input
                    id="preview-image-tag"
                    type="text"
                    value={previewImageTag}
                    onChange={(e) => setPreviewImageTag(e.target.value)}
                    placeholder="inherit base env"
                    className="w-40 rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm text-gray-900 placeholder-gray-400 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
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

      {/* Rename app modal */}
      {showRename &&
        (() => {
          const trimmed = renameInput.trim();
          const validName = /^[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(trimmed);
          const canRename =
            !renaming && validName && trimmed !== appName;
          return (
            <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
              <div className="mx-4 w-full max-w-md rounded-2xl bg-white p-6 shadow-xl">
                <h3 className="text-lg font-semibold text-gray-900">
                  Rename {appName}
                </h3>
                <div className="mt-2 rounded-md bg-amber-50 px-3 py-2 text-xs text-amber-700">
                  Renaming recreates the app under the new name and tears down the
                  old one. If it's deployed it will briefly redeploy into a new
                  namespace, and <strong>app-level secrets must be re-entered</strong>{" "}
                  under the new name (they can't be migrated). Values that hardcode
                  the old name aren't rewritten.
                </div>
                <p className="mt-3 text-sm text-gray-700">New name</p>
                <input
                  type="text"
                  value={renameInput}
                  onChange={(e) => setRenameInput(e.target.value)}
                  placeholder="new-app-name"
                  className="mt-1 w-full rounded-lg border border-gray-300 px-3 py-2 text-sm font-mono focus:border-indigo-400 focus:outline-none focus:ring-1 focus:ring-indigo-400"
                  autoFocus
                />
                <p className="mt-1 text-xs text-gray-400">
                  Lowercase letters, digits and hyphens (DNS label), 2–63 chars,
                  starting with a letter.
                </p>
                {renameError && (
                  <div className="mt-3 rounded-md bg-red-50 px-3 py-2">
                    <p className="text-sm text-red-700">{renameError}</p>
                  </div>
                )}
                <div className="mt-6 flex gap-3">
                  <button
                    onClick={async () => {
                      if (!project || !appName || !canRename) return;
                      setRenaming(true);
                      setRenameError(null);
                      try {
                        await renameApp(project, appName, trimmed);
                        toast.success("App renamed", {
                          description: `${appName} → ${trimmed}`,
                        });
                        navigate(
                          `/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(trimmed)}`,
                        );
                      } catch (err) {
                        const msg =
                          err instanceof Error ? err.message : "Rename failed";
                        setRenameError(msg);
                        toast.error("Failed to rename app", { description: msg });
                      } finally {
                        setRenaming(false);
                      }
                    }}
                    disabled={!canRename}
                    className="flex-1 rounded-lg bg-gray-900 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-700 disabled:opacity-50"
                  >
                    {renaming ? "Renaming…" : "Rename app"}
                  </button>
                  <button
                    onClick={() => setShowRename(false)}
                    disabled={renaming}
                    className="flex-1 rounded-lg border border-gray-300 bg-white py-2 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50 disabled:opacity-50"
                  >
                    Cancel
                  </button>
                </div>
              </div>
            </div>
          );
        })()}

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
          previewEnvs={visiblePreviewEnvs}
          baseEnv={currentBaseEnv}
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
              {d.cluster && (
                <span className="inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 font-mono text-[10px] font-medium text-gray-600">
                  {d.cluster}
                </span>
              )}
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
// Disclosure is a small collapsible wrapper for advanced/secondary sections.
function Disclosure({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="rounded-xl border border-gray-200 bg-white">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 px-5 py-3 text-left text-xs font-medium uppercase tracking-wider text-gray-400 hover:text-gray-600"
      >
        <svg
          className={`h-3.5 w-3.5 transition-transform ${open ? "rotate-90" : ""}`}
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          strokeWidth={2.5}
        >
          <path strokeLinecap="round" strokeLinejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
        </svg>
        {title}
      </button>
      {open && <div className="border-t border-gray-100 px-5 py-4">{children}</div>}
    </div>
  );
}

// LegacyConfigNotice surfaces structured config persisted by the old form
// (template input values / per-component config). It's frozen — still published
// by the backend but no longer editable here — shown read-only for discovery.
function LegacyConfigNotice({ data }: { data: AppDetailType }) {
  const hasValues = Object.keys(data.values ?? {}).length > 0;
  const hasComponents =
    Object.keys(data.componentConfigs ?? {}).length > 0 ||
    Object.keys(data.envComponents ?? {}).length > 0;
  if (!hasValues && !hasComponents) return null;

  const legacy = {
    ...(hasValues ? { values: data.values } : {}),
    ...(data.componentConfigs && Object.keys(data.componentConfigs).length > 0
      ? { componentConfigs: data.componentConfigs }
      : {}),
    ...(data.envComponents && Object.keys(data.envComponents).length > 0
      ? { envComponents: data.envComponents }
      : {}),
  };

  return (
    <Disclosure title="Legacy structured config (read-only)">
      <p className="mb-2 text-xs text-gray-500">
        This app carries structured config from the older form. It's still applied
        at deploy but is no longer editable here — express changes as values
        overrides above. It will be migrated/cleared in a future step.
      </p>
      <pre className="max-h-72 overflow-auto rounded-lg bg-gray-50 p-3 font-mono text-xs text-gray-700">
        {JSON.stringify(legacy, null, 2)}
      </pre>
    </Disclosure>
  );
}

// AppValuesEditor edits the developer's override layers — the app-level base
// (rawValues, all envs) and per-environment overrides (envRawValues[env]) — and
// shows a read-only effective preview (chart ⊕ platform/env ⊕ overrides) computed
// by the backend, reflecting unsaved edits.
const BASE_SCOPE = "__base__";
// Reserved EnvironmentDefaults key for the preview band (values applied to every
// preview on top of its base env). Mirrors the backend's domain.PreviewOverrideKey.
const PREVIEW_SCOPE = "preview";

// ValuesLayers shows, read-only, the override layers that compose the effective
// values for the selected env in precedence order (low → high), each collapsible.
// Empty layers render a muted "inherits" note. The chart/canonical/stack
// sub-layers are folded into the template-effective and effective maps.
function ValuesLayers({
  envLabel,
  templateLayer,
  appBase,
  envOverride,
  envOverrideLabel,
  effective,
}: {
  envLabel: string;
  templateLayer: string;
  appBase: string;
  envOverride: string;
  envOverrideLabel: string;
  effective: string;
}) {
  const layers = [
    {
      n: 1,
      title: "Template & platform defaults",
      note: "read-only — chart defaults ⊕ template defaults ⊕ operator override",
      body: templateLayer,
    },
    { n: 2, title: "App base overrides", note: "all environments", body: appBase },
    {
      n: 3,
      title: `${envOverrideLabel} overrides`,
      note: "this environment, on top of the base",
      body: envOverride,
    },
    {
      n: 4,
      title: `Effective — ${envLabel || "selected env"} (as deployed)`,
      note: "everything merged, low → high",
      body: effective,
      open: true,
    },
  ];
  return (
    <div>
      <p className="mb-3 text-xs text-gray-400">
        How the values compose for{" "}
        <span className="font-medium">{envLabel || "the selected environment"}</span>,
        lowest → highest precedence. Read-only.{" "}
        <code className="font-mono">{"{…}"}</code> tokens resolve at deploy.
      </p>
      <div className="space-y-2">
        {layers.map((l) => (
          <details
            key={l.n}
            open={l.open}
            className="group rounded-lg border border-gray-200 bg-white"
          >
            <summary className="flex cursor-pointer list-none items-center gap-2 px-3 py-2 text-sm">
              <svg
                className="h-3.5 w-3.5 text-gray-400 transition-transform group-open:rotate-90"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                strokeWidth={2}
              >
                <path strokeLinecap="round" strokeLinejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
              </svg>
              <span className="flex h-5 w-5 items-center justify-center rounded bg-gray-100 text-xs font-medium text-gray-500">
                {l.n}
              </span>
              <span className="font-medium text-gray-800">{l.title}</span>
              <span className="text-xs text-gray-400">{l.note}</span>
            </summary>
            <div className="border-t border-gray-100 p-3">
              {l.body && l.body.trim() ? (
                <pre className="overflow-x-auto rounded bg-gray-50 p-3 font-mono text-xs text-gray-700">
                  {l.body}
                </pre>
              ) : (
                <p className="text-xs text-gray-400">— inherits (no overrides at this layer)</p>
              )}
            </div>
          </details>
        ))}
      </div>
    </div>
  );
}

// Defaults for a CD-managed image's Kargo tag selection (mirror the backend's
// gitops.DefaultImageTagPattern / DefaultImageSelectionStrategy): match a 7-char
// git commit SHA and promote the newest build. Editable per image.
const DEFAULT_TAG_PATTERN = "^[0-9a-f]{7}$";
const DEFAULT_SELECTION_STRATEGY = "NewestBuild";
const SELECTION_STRATEGIES = ["NewestBuild", "SemVer", "Digest", "Lexical"];

type ImageSel = { tagPattern: string; selectionStrategy: string };

function AppValuesEditor({
  data,
  project,
  currentEnvName,
  onSaved,
  composed = false,
}: {
  data: AppDetailType;
  project: string;
  currentEnvName: string | null;
  onSaved: () => Promise<void>;
  // composed hides the app-level VALUES editor (inert for composed apps — each
  // component has its own values), while keeping the delivery/CD/images/previews
  // settings below it, which ARE app-level and apply to composed apps too.
  composed?: boolean;
}) {
  const envs = (data.environments ?? [])
    .filter((e) => e.envType !== "preview")
    .map((e) => e.envName);

  const [scope, setScope] = useState<string>(BASE_SCOPE);
  const [baseText, setBaseText] = useState("");
  const [envTexts, setEnvTexts] = useState<Record<string, string>>({});
  const [yamlError, setYamlError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [configVars, setConfigVars] = useState<ConfigVariables | null>(null);
  const [preview, setPreview] = useState<string>("");
  const [chartAvailable, setChartAvailable] = useState(true);
  // Read-only "Layers" view: the override layers that compose the effective values
  // for the selected env, lowest→highest precedence.
  const [view, setView] = useState<"edit" | "layers">("edit");
  const [templateLayer, setTemplateLayer] = useState<string>("");
  const [cdManaged, setCdManaged] = useState(false);
  const [autoPromote, setAutoPromote] = useState(false);
  const [cdSaving, setCdSaving] = useState(false);
  const [previewsEnabled, setPreviewsEnabled] = useState(true);
  const [deliveryMode, setDeliveryMode] = useState("pipeline");
  const [deliverySaving, setDeliverySaving] = useState(false);
  // Per-env deploy opt-in for direct apps, keyed by env name. Seeded from each
  // env's effective `deploy`.
  const [deployEnvs, setDeployEnvs] = useState<Record<string, boolean>>({});
  const [deploySaving, setDeploySaving] = useState(false);
  const [previewsSaving, setPreviewsSaving] = useState(false);
  // Org environments (for resolving each env's clusterRefs / active cluster).
  const [orgEnvs, setOrgEnvs] = useState<OrgEnvironment[]>([]);
  // Per-env target-cluster selection draft, keyed env name → selected cluster
  // names. ["*"] means all of that env's clusters; any other list is an explicit
  // subset. The inherit state (env default only) is shown as the active cluster
  // selected and omitted from the save payload.
  const [targetClusters, setTargetClusters] = useState<Record<string, string[]>>(
    {},
  );
  const [targetClustersSaving, setTargetClustersSaving] = useState(false);

  // Keep the values scope in step with the environment selected above: a preview
  // selects the shared preview band, a stable env selects its own overrides. So
  // the editor (and effective pane) reflect what you're looking at.
  useEffect(() => {
    if (!currentEnvName) return;
    const env = (data.environments ?? []).find((e) => e.envName === currentEnvName);
    if (env?.envType === "preview") setScope(PREVIEW_SCOPE);
    else if (env && envs.includes(env.envName)) setScope(env.envName);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentEnvName]);

  // Template & platform layer (read-only) for the Layers view — chart defaults ⊕
  // template defaults/env ⊕ operator override, scoped to the selected env (a
  // preview uses its base env, since it inherits that env's template values).
  useEffect(() => {
    if (view !== "layers" || !currentEnvName) return;
    const env = (data.environments ?? []).find((e) => e.envName === currentEnvName);
    const tEnv = env?.envType === "preview" ? env.preview?.baseEnv : currentEnvName;
    let cancelled = false;
    fetchTemplateEffectiveValues(data.template.name, tEnv || undefined)
      .then((res) => {
        if (!cancelled) setTemplateLayer(stringifyOverlay(res.values));
      })
      .catch(() => {
        if (!cancelled) setTemplateLayer("");
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view, currentEnvName, data.template.name]);
  // Images discovered in the effective values (from the values preview below);
  // the user checks which ones Kargo manages.
  const [discoveredImages, setDiscoveredImages] = useState<TemplateImage[]>([]);
  // Images selected for CD, keyed by tag key → per-image Kargo settings. Presence
  // means selected. Seeded from the app's saved selection.
  const [selectedImages, setSelectedImages] = useState<Record<string, ImageSel>>(
    {},
  );
  const [imagesSaving, setImagesSaving] = useState(false);

  // Seed editors from the persisted overlays whenever the app data changes.
  useEffect(() => {
    setBaseText(stringifyOverlay(data.rawValues));
    const next: Record<string, string> = {};
    for (const env of envs) {
      next[env] = stringifyOverlay(data.envRawValues?.[env]);
    }
    // The preview band is keyed under the reserved "preview" env name.
    next[PREVIEW_SCOPE] = stringifyOverlay(data.envRawValues?.[PREVIEW_SCOPE]);
    setEnvTexts(next);
    setCdManaged(data.cd?.managed ?? false);
    setAutoPromote(data.cd?.autoPromote ?? false);
    setPreviewsEnabled(data.previewsEnabled ?? true);
    setDeliveryMode(data.deliveryMode === "direct" ? "direct" : "pipeline");
    const de: Record<string, boolean> = {};
    for (const e of data.environments ?? []) {
      if (e.envType !== "preview") de[e.envName] = e.deploy;
    }
    setDeployEnvs(de);
    const sel: Record<string, ImageSel> = {};
    for (const b of data.images ?? []) {
      sel[b.tagKey] = {
        tagPattern: b.tagPattern ?? DEFAULT_TAG_PATTERN,
        selectionStrategy: b.selectionStrategy ?? DEFAULT_SELECTION_STRATEGY,
      };
    }
    setSelectedImages(sel);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data]);

  useEffect(() => {
    listConfigVariables(project)
      .then(setConfigVars)
      .catch(() => setConfigVars({ platform: [], vars: [] }));
  }, [project]);

  useEffect(() => {
    let cancelled = false;
    listOrgEnvironments()
      .then((res) => {
        if (!cancelled) setOrgEnvs(res.environments ?? []);
      })
      .catch(() => {
        /* org envs optional; the target-clusters picker just won't show */
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const activeText = scope === BASE_SCOPE ? baseText : (envTexts[scope] ?? "");
  function setActiveText(text: string) {
    if (scope === BASE_SCOPE) setBaseText(text);
    else setEnvTexts((cur) => ({ ...cur, [scope]: text }));
  }

  // The env whose effective values we preview: the selected scope env, else the
  // currently-viewed env, else the first env.
  const previewEnv =
    scope !== BASE_SCOPE ? scope : (currentEnvName ?? envs[0] ?? "");

  // Debounced effective-values preview, reflecting unsaved edits to both layers.
  useEffect(() => {
    if (!previewEnv) return;
    const baseParsed = parseYamlOverlay(baseText);
    const envParsed = parseYamlOverlay(envTexts[previewEnv] ?? "");
    if (baseParsed.error || envParsed.error) return; // keep last good preview
    const handle = setTimeout(() => {
      previewAppValues(project, data.name, previewEnv, {
        rawValues: baseParsed.value ?? {},
        envRawValues: { [previewEnv]: envParsed.value ?? {} },
      })
        .then((res) => {
          setPreview(stringifyOverlay(res.values));
          setChartAvailable(res.chartDefaultsAvailable);
          setDiscoveredImages(res.discoveredImages ?? []);
        })
        .catch(() => {
          /* leave last good preview */
        });
    }, 400);
    return () => clearTimeout(handle);
  }, [project, data.name, previewEnv, baseText, envTexts]);

  async function save() {
    const parsed = parseYamlOverlay(activeText);
    if (parsed.error) {
      setYamlError(parsed.error);
      return;
    }
    setSaving(true);
    try {
      const req: UpdateAppRequest =
        scope === BASE_SCOPE
          ? { rawValues: parsed.value ?? {} }
          : { envRawValues: { [scope]: parsed.value ?? {} } };
      await updateApp(project, data.name, req);
      toast.success("Values saved — re-publishing to GitOps.");
      await onSaved();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save values");
    } finally {
      setSaving(false);
    }
  }

  const cdDirty =
    cdManaged !== (data.cd?.managed ?? false) ||
    autoPromote !== (data.cd?.autoPromote ?? false);
  // Direct-delivery apps deploy each env from values — no Kargo, so the CD +
  // image-selection sections don't apply. Derived from the live selector so the
  // sections toggle as the user switches mode.
  const isDirect = deliveryMode === "direct";
  const deliveryDirty =
    deliveryMode !== (data.deliveryMode === "direct" ? "direct" : "pipeline");

  async function saveDelivery() {
    setDeliverySaving(true);
    try {
      await updateApp(project, data.name, { deliveryMode });
      toast.success(
        deliveryMode === "direct"
          ? "Switched to direct delivery — re-publishing (Kargo removed)."
          : "Switched to pipeline delivery — re-publishing.",
      );
      await onSaved();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to save delivery mode",
      );
    } finally {
      setDeliverySaving(false);
    }
  }

  // Stable envs in promotion order, for the per-env deploy toggles (direct apps).
  const stableEnvList = (data.environments ?? []).filter(
    (e) => e.envType !== "preview",
  );
  const deployDirty = stableEnvList.some(
    (e) => (deployEnvs[e.envName] ?? e.deploy) !== e.deploy,
  );

  async function saveDeployEnvs() {
    setDeploySaving(true);
    try {
      await updateApp(project, data.name, { deployEnvs });
      toast.success("Environment deployment updated — re-publishing.");
      await onSaved();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to update environments",
      );
    } finally {
      setDeploySaving(false);
    }
  }

  // Resolve an env's bound clusters + its default (active) cluster from the org
  // env registry, mirroring AppClusterSecrets: clusterRefs, else [activeClusterRef].
  function resolveEnvClusters(envName: string): {
    clusters: string[];
    defaultCluster: string;
  } {
    const oe = orgEnvs.find((e) => e.name === envName);
    const clusters = oe?.clusterRefs?.length
      ? oe.clusterRefs
      : oe?.activeClusterRef
        ? [oe.activeClusterRef]
        : [];
    const defaultCluster = oe?.activeClusterRef || clusters[0] || "";
    return { clusters, defaultCluster };
  }

  // Only envs with more than one cluster to choose from get a picker.
  const targetClusterEnvs = stableEnvList
    .map((e) => ({ envName: e.envName, ...resolveEnvClusters(e.envName) }))
    .filter((e) => e.clusters.length > 1);

  // Seed the target-cluster draft from the persisted selection whenever the app
  // data or the resolved org envs change. ["*"] → all; a stored subset → those;
  // absent/empty → the env default (active cluster), shown as the inherit state.
  useEffect(() => {
    const next: Record<string, string[]> = {};
    for (const e of targetClusterEnvs) {
      const saved = data.targetClusters?.[e.envName];
      if (saved && saved.length === 1 && saved[0] === "*") {
        next[e.envName] = ["*"];
      } else if (saved && saved.length > 0) {
        next[e.envName] = saved.filter((c) => e.clusters.includes(c));
      } else {
        next[e.envName] = e.defaultCluster ? [e.defaultCluster] : [];
      }
    }
    setTargetClusters(next);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, orgEnvs]);

  // Normalize a per-env selection to its wire value, or null to inherit (omit).
  // ["*"] → all; just the default cluster (or nothing) → inherit; else the subset.
  function targetClusterValue(
    envName: string,
    sel: string[],
  ): string[] | null {
    if (sel.length === 1 && sel[0] === "*") return ["*"];
    const { clusters, defaultCluster } = resolveEnvClusters(envName);
    const chosen = sel.filter((c) => clusters.includes(c));
    if (chosen.length === 0) return null;
    if (chosen.length === 1 && chosen[0] === defaultCluster) return null;
    return [...chosen].sort();
  }

  function buildTargetClusters(
    source: Record<string, string[]>,
  ): Record<string, string[]> {
    const out: Record<string, string[]> = {};
    for (const e of targetClusterEnvs) {
      const v = targetClusterValue(e.envName, source[e.envName] ?? []);
      if (v) out[e.envName] = v;
    }
    return out;
  }

  const savedTargetClusters = buildTargetClusters(
    Object.fromEntries(
      targetClusterEnvs.map((e) => [
        e.envName,
        data.targetClusters?.[e.envName] ?? [],
      ]),
    ),
  );
  const draftTargetClusters = buildTargetClusters(targetClusters);
  const targetClustersDirty =
    JSON.stringify(savedTargetClusters) !== JSON.stringify(draftTargetClusters);

  async function saveTargetClusters() {
    setTargetClustersSaving(true);
    try {
      await updateApp(project, data.name, {
        targetClusters: draftTargetClusters,
      });
      toast.success("Target clusters saved — re-publishing to GitOps.");
      await onSaved();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to save target clusters",
      );
    } finally {
      setTargetClustersSaving(false);
    }
  }

  async function removeFromCluster(envName: string) {
    if (
      !window.confirm(
        `Remove ${data.name} from "${envName}"? This deletes its resources in that environment` +
          ` — for stateful apps (databases/caches) this destroys that environment's data. This cannot be undone.`,
      )
    ) {
      return;
    }
    try {
      await undeployAppEnv(project, data.name, envName);
      toast.success(`Removed from ${envName}.`);
      await onSaved();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to remove from cluster",
      );
    }
  }

  async function redeployEnv(envName: string) {
    try {
      await updateApp(project, data.name, { deployEnvs: { [envName]: true } });
      toast.success(
        `Re-enabled ${envName} — its Kargo stage is back. Promote to deploy it.`,
      );
      await onSaved();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to re-enable");
    }
  }

  async function saveCD() {
    setCdSaving(true);
    try {
      await updateApp(project, data.name, { cd: { managed: cdManaged, autoPromote } });
      toast.success("CD settings saved — re-publishing to GitOps.");
      await onSaved();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save CD settings");
    } finally {
      setCdSaving(false);
    }
  }

  // Build the CD selection from the checked discovered images, carrying each
  // image's (possibly customized) tag pattern + selection strategy.
  function buildImageBindings(): AppImageBinding[] {
    const out: AppImageBinding[] = [];
    for (const img of discoveredImages) {
      const s = selectedImages[img.tagKey];
      if (!s) continue;
      out.push({
        name: img.name,
        tagKey: img.tagKey,
        tagPattern: s.tagPattern,
        selectionStrategy: s.selectionStrategy,
      });
    }
    return out;
  }

  const savedImages: Record<string, ImageSel> = {};
  for (const b of data.images ?? []) {
    savedImages[b.tagKey] = {
      tagPattern: b.tagPattern ?? DEFAULT_TAG_PATTERN,
      selectionStrategy: b.selectionStrategy ?? DEFAULT_SELECTION_STRATEGY,
    };
  }
  const selectedKeys = Object.keys(selectedImages);
  const imagesDirty =
    selectedKeys.length !== Object.keys(savedImages).length ||
    selectedKeys.some((k) => {
      const cur = selectedImages[k];
      const prev = savedImages[k];
      if (!cur) return false;
      return (
        !prev ||
        prev.tagPattern !== cur.tagPattern ||
        prev.selectionStrategy !== cur.selectionStrategy
      );
    });

  function toggleImage(tagKey: string) {
    setSelectedImages((cur) => {
      const next = { ...cur };
      if (next[tagKey]) delete next[tagKey];
      else
        next[tagKey] = {
          tagPattern: DEFAULT_TAG_PATTERN,
          selectionStrategy: DEFAULT_SELECTION_STRATEGY,
        };
      return next;
    });
  }

  function updateImageSel(tagKey: string, patch: Partial<ImageSel>) {
    setSelectedImages((cur) =>
      cur[tagKey] ? { ...cur, [tagKey]: { ...cur[tagKey], ...patch } } : cur,
    );
  }

  async function saveImages() {
    setImagesSaving(true);
    try {
      await updateApp(project, data.name, { images: buildImageBindings() });
      toast.success("CD images saved — re-publishing to GitOps.");
      await onSaved();
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to save CD images",
      );
    } finally {
      setImagesSaving(false);
    }
  }

  const previewsDirty = previewsEnabled !== (data.previewsEnabled ?? true);

  async function savePreviews() {
    setPreviewsSaving(true);
    try {
      await updateApp(project, data.name, { previewsEnabled });
      toast.success("Preview settings saved.");
      await onSaved();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save preview settings");
    } finally {
      setPreviewsSaving(false);
    }
  }

  return (
    <div className="rounded-xl border border-gray-200 bg-white">
      <div className="flex items-center justify-between border-b border-gray-100 px-5 py-3">
        <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
          {composed ? "Settings" : "Values"}
        </h2>
        {!composed && (
          <div className="flex items-center gap-2">
            {/* Edit (editable overrides) vs Layers (read-only precedence stack). */}
            <div className="flex rounded-md border border-gray-200 p-0.5 text-xs">
              {(["edit", "layers"] as const).map((v) => (
                <button
                  key={v}
                  onClick={() => setView(v)}
                  className={`rounded px-2 py-0.5 font-medium capitalize ${
                    view === v ? "bg-gray-900 text-white" : "text-gray-500 hover:text-gray-800"
                  }`}
                >
                  {v}
                </button>
              ))}
            </div>
            {view === "edit" && (
              <>
                <select
                  value={scope}
                  onChange={(e) => {
                    setScope(e.target.value);
                    setYamlError(null);
                  }}
                  className="rounded-md border border-gray-300 px-2 py-1 text-xs"
                >
                  <option value={BASE_SCOPE}>Base (all environments)</option>
                  {envs.map((env) => (
                    <option key={env} value={env}>
                      {env} overrides
                    </option>
                  ))}
                  {previewsEnabled && (
                    <option value={PREVIEW_SCOPE}>preview overrides (all previews)</option>
                  )}
                </select>
                <button
                  onClick={save}
                  disabled={saving || yamlError !== null}
                  className="rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
                >
                  {saving ? "Saving…" : "Save"}
                </button>
              </>
            )}
          </div>
        )}
      </div>
      <div className="px-5 py-4">
        {!composed && view === "layers" && (
          <ValuesLayers
            envLabel={currentEnvName ?? previewEnv}
            templateLayer={templateLayer}
            appBase={stringifyOverlay(data.rawValues)}
            envOverride={activeText}
            envOverrideLabel={scope === PREVIEW_SCOPE ? "preview" : (currentEnvName ?? scope)}
            effective={preview}
          />
        )}
        {!composed && view === "edit" && (
        <>
        <p className="mb-3 text-xs text-gray-400">
          {scope === BASE_SCOPE
            ? "App-level overrides applied to every environment."
            : scope === PREVIEW_SCOPE
              ? "Overrides applied to every preview, layered on top of its base env."
              : `Overrides for ${scope}, layered on top of the base.`}{" "}
          Edit only what you override — empty inherits the chart and platform
          defaults. Reference{" "}
          <code className="font-mono">{"((platform.*))"}</code> /{" "}
          <code className="font-mono">{"((vars.*))"}</code> tokens.
        </p>
        <Suspense
          fallback={
            <div className="rounded-lg border border-gray-200 p-4 text-xs text-gray-400">
              Loading editor…
            </div>
          }
        >
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
            <ValuesEditor
              label={scope === BASE_SCOPE ? "Base overrides" : `${scope} overrides`}
              value={activeText}
              configVars={configVars}
              placeholder={
                "# e.g.\nresources:\n  requests:\n    cpu: 200m"
              }
              onChange={setActiveText}
              onValidChange={(_, err) => setYamlError(err)}
            />
            <div>
              <ValuesEditor
                label={
                  scope === PREVIEW_SCOPE
                    ? "Effective — preview (computed)"
                    : previewEnv
                      ? `Effective — ${previewEnv} (as deployed)`
                      : "Effective (as deployed)"
                }
                value={preview}
                readOnly
              />
              {!chartAvailable && (
                <p className="mt-1 text-xs text-gray-400">
                  Chart defaults aren't readable for this template; preview shows
                  the platform base + overrides only.
                </p>
              )}
              <p className="mt-1 text-xs text-gray-400">
                Chart defaults ⊕ platform base ⊕ overrides. Routing/cluster
                resolution and <code className="font-mono">{"{…}"}</code> tokens
                are resolved at deploy.
              </p>
            </div>
          </div>
        </Suspense>
        {yamlError && (
          <p className="mt-2 text-xs text-red-600">Invalid YAML: {yamlError}</p>
        )}
        </>
        )}

        <div className={composed ? "" : "mt-4 border-t border-gray-100 pt-4"}>
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium text-gray-700">Delivery mode</p>
              <select
                value={deliveryMode}
                onChange={(e) => setDeliveryMode(e.target.value)}
                className="mt-2 w-full max-w-md rounded-md border border-gray-300 px-2 py-1 text-sm"
              >
                <option value="pipeline">Pipeline (Kargo + promotion)</option>
                <option value="direct">Direct (deploy each env from values)</option>
              </select>
              <p className="mt-1 text-xs text-gray-400">
                {isDirect
                  ? "Each environment deploys straight from its values — no Kargo, no promotion. Switching to direct removes this app's Kargo resources on the next publish."
                  : "CI image promoted staging→prod via Kargo. Switch to direct for off-the-shelf software with a pinned image."}
              </p>
            </div>
            <button
              onClick={saveDelivery}
              disabled={deliverySaving || !deliveryDirty}
              className="shrink-0 rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              {deliverySaving ? "Saving…" : "Save"}
            </button>
          </div>
        </div>

        {isDirect && (
          <div className="mt-4 border-t border-gray-100 pt-4">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-gray-700">Environments</p>
                <p className="mt-1 text-xs text-gray-400">
                  The base env deploys by default; check higher envs to deploy
                  them. Unchecking stops updates but leaves a running env in place
                  — use "Remove from cluster" to delete it.
                </p>
                <div className="mt-3 space-y-2">
                  {stableEnvList.map((e) => (
                    <div
                      key={e.envName}
                      className="flex items-center justify-between gap-3 text-xs text-gray-600"
                    >
                      <label className="flex items-center gap-2">
                        <input
                          type="checkbox"
                          checked={deployEnvs[e.envName] ?? e.deploy}
                          onChange={(ev) =>
                            setDeployEnvs((cur) => ({
                              ...cur,
                              [e.envName]: ev.target.checked,
                            }))
                          }
                          className="h-4 w-4 rounded border-gray-300"
                        />
                        <span className="font-medium capitalize">
                          {e.envName}
                        </span>
                        {e.isBase && (
                          <span className="rounded bg-indigo-50 px-1.5 py-0.5 text-[10px] font-medium text-indigo-600">
                            base
                          </span>
                        )}
                      </label>
                      {e.deploy && (
                        <button
                          onClick={() => removeFromCluster(e.envName)}
                          className="shrink-0 text-[11px] font-medium text-red-600 hover:text-red-800"
                        >
                          Remove from cluster
                        </button>
                      )}
                    </div>
                  ))}
                </div>
              </div>
              <button
                onClick={saveDeployEnvs}
                disabled={deploySaving || !deployDirty}
                className="shrink-0 rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
              >
                {deploySaving ? "Saving…" : "Save"}
              </button>
            </div>
          </div>
        )}

        {!isDirect && (
          <div className="mt-4 border-t border-gray-100 pt-4">
            <div className="min-w-0 flex-1">
              <p className="text-sm font-medium text-gray-700">Environments</p>
              <p className="mt-1 text-xs text-gray-400">
                Remove an app from an environment above the base to take it out
                of the promotion pipeline — its workload is pruned and its Kargo
                stage is dropped, re-linking the chain. The base env can't be
                removed. Re-deploy to add it back, then promote.
              </p>
              <div className="mt-3 space-y-2">
                {stableEnvList.map((e) => (
                  <div
                    key={e.envName}
                    className="flex items-center justify-between gap-3 text-xs text-gray-600"
                  >
                    <span className="flex items-center gap-2">
                      <span className="font-medium capitalize">
                        {e.envName}
                      </span>
                      {e.isBase && (
                        <span className="rounded bg-indigo-50 px-1.5 py-0.5 text-[10px] font-medium text-indigo-600">
                          base
                        </span>
                      )}
                      {!e.deploy && (
                        <span className="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium text-gray-500">
                          decommissioned
                        </span>
                      )}
                    </span>
                    {!e.isBase &&
                      (e.deploy ? (
                        <button
                          onClick={() => removeFromCluster(e.envName)}
                          className="shrink-0 text-[11px] font-medium text-red-600 hover:text-red-800"
                        >
                          Remove from cluster
                        </button>
                      ) : (
                        <button
                          onClick={() => redeployEnv(e.envName)}
                          className="shrink-0 text-[11px] font-medium text-indigo-600 hover:text-indigo-800"
                        >
                          Re-deploy
                        </button>
                      ))}
                  </div>
                ))}
              </div>
            </div>
          </div>
        )}

        {targetClusterEnvs.length > 0 && (
          <div className="mt-4 border-t border-gray-100 pt-4">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-gray-700">
                  Target clusters
                </p>
                <p className="mt-1 text-xs text-gray-400">
                  Choose which of each environment's clusters this app deploys
                  to. Leave the default (the environment's active cluster) to
                  inherit, pick a specific subset, or select “All clusters” to
                  follow every cluster the environment has.
                </p>
                <div className="mt-3 space-y-3">
                  {targetClusterEnvs.map((e) => {
                    const sel = targetClusters[e.envName] ?? [];
                    const isAll = sel.length === 1 && sel[0] === "*";
                    return (
                      <div
                        key={e.envName}
                        className="rounded-lg border border-gray-200 p-3"
                      >
                        <p className="mb-2 text-xs font-medium capitalize text-gray-700">
                          {e.envName}
                        </p>
                        <div className="flex flex-wrap gap-x-4 gap-y-2">
                          <label className="flex items-center gap-2 text-xs text-gray-600">
                            <input
                              type="checkbox"
                              checked={isAll}
                              onChange={(ev) =>
                                setTargetClusters((cur) => ({
                                  ...cur,
                                  [e.envName]: ev.target.checked
                                    ? ["*"]
                                    : e.defaultCluster
                                      ? [e.defaultCluster]
                                      : [],
                                }))
                              }
                              className="h-4 w-4 rounded border-gray-300"
                            />
                            <span className="font-medium">All clusters</span>
                          </label>
                          {e.clusters.map((cluster) => (
                            <label
                              key={cluster}
                              className="flex items-center gap-2 text-xs text-gray-600"
                            >
                              <input
                                type="checkbox"
                                disabled={isAll}
                                checked={isAll || sel.includes(cluster)}
                                onChange={(ev) =>
                                  setTargetClusters((cur) => {
                                    const prev = cur[e.envName] ?? [];
                                    const nextSel = ev.target.checked
                                      ? [
                                          ...prev.filter((c) => c !== "*"),
                                          cluster,
                                        ]
                                      : prev.filter((c) => c !== cluster);
                                    return { ...cur, [e.envName]: nextSel };
                                  })
                                }
                                className="h-4 w-4 rounded border-gray-300 disabled:opacity-50"
                              />
                              <span className="font-mono">{cluster}</span>
                              {cluster === e.defaultCluster && (
                                <span className="rounded bg-indigo-50 px-1.5 py-0.5 text-[10px] font-medium text-indigo-600">
                                  active
                                </span>
                              )}
                            </label>
                          ))}
                        </div>
                      </div>
                    );
                  })}
                </div>
              </div>
              <button
                onClick={saveTargetClusters}
                disabled={targetClustersSaving || !targetClustersDirty}
                className="shrink-0 rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
              >
                {targetClustersSaving ? "Saving…" : "Save"}
              </button>
            </div>
          </div>
        )}

        {!isDirect && (
          <div className="mt-4 border-t border-gray-100 pt-4">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                <label className="flex items-center gap-2 text-sm font-medium text-gray-700">
                  <input
                    type="checkbox"
                    checked={cdManaged}
                    onChange={(e) => setCdManaged(e.target.checked)}
                    className="h-4 w-4 rounded border-gray-300"
                  />
                  Image tag managed by Kargo
                </label>
                <p className="mt-1 text-xs text-gray-400">
                  When enabled, Kargo owns the image tag: it commits the
                  discovered/promoted tag and re-publishing preserves it instead of
                  resetting to the value in your overrides. Leave the tag out of the
                  overrides above once this is on. Which images Kargo watches is set
                  under Images below.
                </p>
                <label className="mt-3 flex items-center gap-2 text-sm font-medium text-gray-700">
                  <input
                    type="checkbox"
                    checked={autoPromote}
                    onChange={(e) => setAutoPromote(e.target.checked)}
                    className="h-4 w-4 rounded border-gray-300"
                  />
                  Auto-promote to prod
                </label>
                <p className="mt-1 text-xs text-gray-400">
                  Promotes the same image to prod automatically once it's deployed and
                  healthy in staging — no manual step. Manual “Promote” still works.
                </p>
              </div>
              <button
                onClick={saveCD}
                disabled={cdSaving || !cdDirty}
                className="shrink-0 rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
              >
                {cdSaving ? "Saving…" : "Save CD"}
              </button>
            </div>
          </div>
        )}

        {!isDirect && discoveredImages.length > 0 && (
          <div className="mt-4 border-t border-gray-100 pt-4">
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-gray-700">Images</p>
                <p className="mt-1 text-xs text-gray-400">
                  These images were found in this app's effective values. Check the
                  ones Kargo should watch and promote — leave out images you don't
                  want under CD (e.g. sidecars). The repository is read from the
                  values at deploy.
                </p>
                <div className="mt-3 space-y-3">
                  {discoveredImages.map((img) => {
                    const sel = selectedImages[img.tagKey];
                    return (
                      <div key={img.tagKey} className="text-xs text-gray-600">
                        <label className="flex items-start gap-2">
                          <input
                            type="checkbox"
                            checked={!!sel}
                            onChange={() => toggleImage(img.tagKey)}
                            className="mt-0.5 h-4 w-4 rounded border-gray-300"
                          />
                          <span className="min-w-0">
                            <span className="font-medium">{img.name}</span>{" "}
                            <span className="font-mono text-gray-500">
                              {img.repository}
                            </span>
                            <span className="ml-2 font-mono text-gray-400">
                              {img.tagKey}
                            </span>
                          </span>
                        </label>
                        {sel && (
                          <div className="mt-2 ml-6 flex flex-wrap items-center gap-x-4 gap-y-2">
                            <label className="flex items-center gap-1">
                              <span className="text-gray-500">Tag regex</span>
                              <input
                                type="text"
                                value={sel.tagPattern}
                                onChange={(e) =>
                                  updateImageSel(img.tagKey, {
                                    tagPattern: e.target.value,
                                  })
                                }
                                placeholder={DEFAULT_TAG_PATTERN}
                                className="w-48 rounded-md border border-gray-300 px-2 py-1 font-mono"
                              />
                            </label>
                            <label className="flex items-center gap-1">
                              <span className="text-gray-500">Strategy</span>
                              <select
                                value={sel.selectionStrategy}
                                onChange={(e) =>
                                  updateImageSel(img.tagKey, {
                                    selectionStrategy: e.target.value,
                                  })
                                }
                                className="rounded-md border border-gray-300 px-2 py-1"
                              >
                                {SELECTION_STRATEGIES.map((s) => (
                                  <option key={s} value={s}>
                                    {s}
                                  </option>
                                ))}
                              </select>
                            </label>
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
                <p className="mt-2 text-xs text-gray-400">
                  Defaults: tag regex{" "}
                  <code className="font-mono">{DEFAULT_TAG_PATTERN}</code>{" "}
                  (7-char commit SHA), strategy{" "}
                  <code className="font-mono">{DEFAULT_SELECTION_STRATEGY}</code>.
                  Override per image above.
                </p>
              </div>
              <button
                onClick={saveImages}
                disabled={imagesSaving || !imagesDirty}
                className="shrink-0 rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
              >
                {imagesSaving ? "Saving…" : "Save"}
              </button>
            </div>
          </div>
        )}

        <div className="mt-4 border-t border-gray-100 pt-4">
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <label className="flex items-center gap-2 text-sm font-medium text-gray-700">
                <input
                  type="checkbox"
                  checked={previewsEnabled}
                  onChange={(e) => setPreviewsEnabled(e.target.checked)}
                  className="h-4 w-4 rounded border-gray-300"
                />
                Previews enabled
              </label>
              <p className="mt-1 text-xs text-gray-400">
                When enabled, this app can be deployed to ephemeral preview
                (PR) environments. A preview clones a base env (default staging)
                and deploys all the app's enabled components.
              </p>
            </div>
            <button
              onClick={savePreviews}
              disabled={previewsSaving || !previewsDirty}
              className="shrink-0 rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              {previewsSaving ? "Saving…" : "Save"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

// ClusterOverridesEditor lets an operator override template values per cluster
// for fan-out ("all" deploy-mode) environments. It reads the env→cluster set
// from the org environments and the current overrides from the app detail, and
// saves via the app-edit PATCH (clusterOverrides), which re-publishes per
// cluster.
type ClusterDraft = { replicas: string; rows: { key: string; value: string }[] };

function ClusterOverridesEditor({
  data,
  project,
  onSaved,
}: {
  data: AppDetailType;
  project: string;
  onSaved: () => Promise<void>;
}) {
  const [fanoutEnvs, setFanoutEnvs] = useState<
    { name: string; clusters: string[] }[]
  >([]);
  const [loading, setLoading] = useState(true);
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  // draft[env][cluster] = { replicas, rows }
  const [draft, setDraft] = useState<Record<string, Record<string, ClusterDraft>>>({});

  const seed = useCallback(
    (envs: { name: string; clusters: string[] }[]) => {
      const d: Record<string, Record<string, ClusterDraft>> = {};
      for (const env of envs) {
        const envMap: Record<string, ClusterDraft> = {};
        for (const cluster of env.clusters) {
          const existing = data.clusterOverrides?.[env.name]?.[cluster];
          const rows = Object.entries(existing?.values ?? {}).map(([k, v]) => ({
            key: k,
            value: String(v),
          }));
          envMap[cluster] = {
            replicas: existing?.replicas ? String(existing.replicas) : "",
            rows,
          };
        }
        d[env.name] = envMap;
      }
      return d;
    },
    [data],
  );

  useEffect(() => {
    let cancelled = false;
    listOrgEnvironments()
      .then((res) => {
        if (cancelled) return;
        const envs = res.environments
          .filter((e) => e.deployMode === "all" && (e.clusterRefs?.length ?? 0) > 0)
          .map((e) => ({ name: e.name, clusters: e.clusterRefs ?? [] }));
        setFanoutEnvs(envs);
        setDraft(seed(envs));
      })
      .catch(() => {
        /* org envs optional; panel just won't show */
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [seed]);

  function setCluster(env: string, cluster: string, patch: Partial<ClusterDraft>) {
    setDraft((d) => {
      const envMap = d[env] ?? {};
      const cur = envMap[cluster] ?? { replicas: "", rows: [] };
      return { ...d, [env]: { ...envMap, [cluster]: { ...cur, ...patch } } };
    });
  }

  async function save() {
    setSaving(true);
    try {
      const payload: NonNullable<UpdateAppRequest["clusterOverrides"]> = {};
      for (const env of fanoutEnvs) {
        const envPayload: Record<string, ClusterValueOverride> = {};
        for (const cluster of env.clusters) {
          const cd = draft[env.name]?.[cluster];
          if (!cd) continue;
          const values: Record<string, unknown> = {};
          for (const r of cd.rows) {
            if (r.key.trim()) values[r.key.trim()] = r.value;
          }
          const ov: ClusterValueOverride = {};
          const n = parseInt(cd.replicas, 10);
          if (cd.replicas.trim() && !Number.isNaN(n)) ov.replicas = n;
          if (Object.keys(values).length > 0) ov.values = values;
          if (ov.replicas !== undefined || ov.values) {
            envPayload[cluster] = ov;
          }
        }
        payload[env.name] = envPayload;
      }
      await updateApp(project, data.name, { clusterOverrides: payload });
      toast.success("Per-cluster overrides saved — re-publishing to GitOps.");
      await onSaved();
      setEditing(false);
    } catch (err) {
      toast.error(
        err instanceof Error ? err.message : "Failed to save per-cluster overrides",
      );
    } finally {
      setSaving(false);
    }
  }

  if (loading || fanoutEnvs.length === 0) {
    // Nothing to show until at least one environment is in "all" deploy mode.
    return null;
  }

  const inputCls =
    "w-full rounded-md border border-gray-300 px-2 py-1 text-sm focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500";

  return (
    <div className="rounded-xl border border-gray-200 bg-white">
      <div className="flex items-center justify-between border-b border-gray-100 px-5 py-3">
        <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
          Per-cluster overrides
        </h2>
        {editing ? (
          <div className="flex gap-2">
            <button
              onClick={() => {
                setDraft(seed(fanoutEnvs));
                setEditing(false);
              }}
              className="text-xs text-gray-500 hover:text-gray-700"
            >
              Cancel
            </button>
            <button
              onClick={save}
              disabled={saving}
              className="text-xs font-medium text-blue-600 hover:text-blue-800 disabled:opacity-50"
            >
              {saving ? "Saving..." : "Save"}
            </button>
          </div>
        ) : (
          <button
            onClick={() => setEditing(true)}
            className="text-xs font-medium text-blue-600 hover:text-blue-800"
          >
            Edit
          </button>
        )}
      </div>

      <div className="space-y-5 px-5 py-4">
        <p className="text-xs text-gray-400">
          Override replicas and template values for individual clusters in
          fan-out (“All clusters”) environments. Empty fields inherit the
          environment value.
        </p>
        {fanoutEnvs.map((env) => (
          <div key={env.name}>
            <p className="mb-2 text-xs font-medium uppercase tracking-wider text-gray-500">
              {env.name}
            </p>
            <div className="grid gap-3 sm:grid-cols-2">
              {env.clusters.map((cluster) => {
                const cd = draft[env.name]?.[cluster] ?? { replicas: "", rows: [] };
                return (
                  <div
                    key={cluster}
                    className="rounded-lg border border-gray-200 p-3"
                  >
                    <p className="mb-2 font-mono text-xs text-gray-700">{cluster}</p>
                    {editing ? (
                      <div className="space-y-2">
                        <label className="block">
                          <span className="text-xs text-gray-500">Replicas</span>
                          <input
                            type="number"
                            min={0}
                            className={inputCls}
                            value={cd.replicas}
                            placeholder="inherit"
                            onChange={(e) =>
                              setCluster(env.name, cluster, { replicas: e.target.value })
                            }
                          />
                        </label>
                        <div>
                          <span className="text-xs text-gray-500">Values</span>
                          {cd.rows.map((row, i) => (
                            <div key={i} className="mt-1 flex gap-1">
                              <input
                                className={inputCls}
                                placeholder="key"
                                value={row.key}
                                onChange={(e) => {
                                  const rows = cd.rows.map((r, j) =>
                                    j === i ? { ...r, key: e.target.value } : r,
                                  );
                                  setCluster(env.name, cluster, { rows });
                                }}
                              />
                              <input
                                className={inputCls}
                                placeholder="value"
                                value={row.value}
                                onChange={(e) => {
                                  const rows = cd.rows.map((r, j) =>
                                    j === i ? { ...r, value: e.target.value } : r,
                                  );
                                  setCluster(env.name, cluster, { rows });
                                }}
                              />
                              <button
                                onClick={() => {
                                  const rows = cd.rows.filter((_, j) => j !== i);
                                  setCluster(env.name, cluster, { rows });
                                }}
                                className="px-1 text-xs text-red-500 hover:text-red-700"
                                aria-label="Remove"
                              >
                                ✕
                              </button>
                            </div>
                          ))}
                          <button
                            onClick={() =>
                              setCluster(env.name, cluster, {
                                rows: [...cd.rows, { key: "", value: "" }],
                              })
                            }
                            className="mt-1 text-xs text-blue-600 hover:text-blue-800"
                          >
                            + Add value
                          </button>
                        </div>
                      </div>
                    ) : cd.replicas || cd.rows.length > 0 ? (
                      <dl className="space-y-1 text-xs">
                        {cd.replicas && (
                          <div className="flex justify-between">
                            <dt className="text-gray-500">replicas</dt>
                            <dd className="font-mono text-gray-800">{cd.replicas}</dd>
                          </div>
                        )}
                        {cd.rows.map((r, i) => (
                          <div key={i} className="flex justify-between">
                            <dt className="text-gray-500">{r.key}</dt>
                            <dd className="font-mono text-gray-800">{r.value}</dd>
                          </div>
                        ))}
                      </dl>
                    ) : (
                      <p className="text-xs italic text-gray-400">No overrides</p>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}

// PinControls promotes a PR preview's image to a stable env without merging and
// pins it (CD paused for that env), or unpins a pinned env. Shown when a preview
// is selected (offer pin) or a pinned stable env is selected (offer unpin).
function PinControls({
  project,
  app,
  currentEnv,
  environments,
  isDirect,
  onChanged,
}: {
  project: string;
  app: string;
  currentEnv: AppEnvironmentSummary | null;
  environments: AppEnvironmentSummary[];
  isDirect: boolean;
  onChanged: () => Promise<void>;
}) {
  const stableEnvs = environments.filter((e) => e.envType !== "preview");
  const [target, setTarget] = useState<string>(stableEnvs[0]?.envName ?? "");
  const [busy, setBusy] = useState(false);

  // Pinning is pipeline-only, but suspend/resume works for direct apps too — so
  // gate only on currentEnv here and guard the pin branches on !isDirect below.
  if (!currentEnv) return null;

  // Pin state lives in the app spec; the per-env fetch that backs currentEnv may
  // not carry it, so read it from the enriched app-detail env list.
  const enriched = environments.find((e) => e.envName === currentEnv.envName);
  const pinnedTag = enriched?.pinnedTag ?? currentEnv.pinnedTag;
  const pinnedFrom = enriched?.pinnedFrom ?? currentEnv.pinnedFrom;

  // Pinned stable env → offer unpin (pipeline-only; direct apps never pin).
  if (!isDirect && currentEnv.envType !== "preview" && pinnedTag) {
    async function unpin() {
      setBusy(true);
      try {
        await unpinAppEnv(project, app, currentEnv!.envName);
        toast.success(`${currentEnv!.envName} unpinned — normal delivery resumed`);
        await onChanged();
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "Failed to unpin");
      } finally {
        setBusy(false);
      }
    }
    return (
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-purple-200 bg-purple-50 px-4 py-2.5">
        <span className="text-sm text-purple-900">
          📌 Pinned to{" "}
          <span className="font-medium">{pinnedFrom || "a preview"}</span>{" "}
          <code className="font-mono text-xs">{pinnedTag}</code> — auto-promote
          paused; new images won't deploy here until unpinned.
        </span>
        <button
          onClick={unpin}
          disabled={busy}
          className="shrink-0 rounded-md border border-purple-300 bg-white px-3 py-1 text-xs font-medium text-purple-700 hover:bg-purple-50 disabled:opacity-50"
        >
          {busy ? "Unpinning…" : "Unpin"}
        </button>
      </div>
    );
  }

  // Preview selected → offer pinning it to a stable env (pipeline-only).
  if (!isDirect && currentEnv.envType === "preview") {
    const previewName = currentEnv.preview?.previewName ?? currentEnv.envName;
    const hasImage = !!currentEnv.release?.tag;
    async function pin() {
      if (!target) return;
      setBusy(true);
      try {
        await pinAppEnv(project, app, target, currentEnv!.envName);
        toast.success(`${previewName} pinned to ${target}`);
        await onChanged();
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "Failed to pin");
      } finally {
        setBusy(false);
      }
    }
    return (
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-gray-200 bg-white px-4 py-2.5">
        <span className="text-sm text-gray-600">
          Pin this preview's image to a stable env (deploy without merging). The env
          holds it until unpinned.
        </span>
        <div className="flex shrink-0 items-center gap-2">
          <select
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            className="rounded-md border border-gray-300 px-2 py-1 text-xs"
          >
            {stableEnvs.map((e) => (
              <option key={e.envName} value={e.envName}>
                {e.envName}
              </option>
            ))}
          </select>
          <button
            onClick={pin}
            disabled={busy || !target || !hasImage}
            title={hasImage ? "" : "This preview has no image tag yet"}
            className="rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
          >
            {busy ? "Pinning…" : `Pin to ${target || "env"}`}
          </button>
        </div>
      </div>
    );
  }

  // Non-stable (preview) envs can't be suspended — nothing more to offer.
  if (currentEnv.envType === "preview") return null;

  // Stable, unpinned env → offer suspend/resume (scale the workload down/up
  // without deleting it — no data loss, unlike undeploy). Works for direct apps.
  const suspended = enriched?.suspended ?? currentEnv.suspended ?? false;
  async function toggleSuspend() {
    setBusy(true);
    try {
      if (suspended) {
        await resumeAppEnv(project, app, currentEnv!.envName);
        toast.success(`${currentEnv!.envName} resumed`);
      } else {
        await suspendAppEnv(project, app, currentEnv!.envName);
        toast.success(`${currentEnv!.envName} suspended`);
      }
      await onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to change suspend state");
    } finally {
      setBusy(false);
    }
  }
  return (
    <div
      className={`flex flex-wrap items-center justify-between gap-2 rounded-lg border px-4 py-2.5 ${
        suspended ? "border-amber-200 bg-amber-50" : "border-gray-200 bg-white"
      }`}
    >
      <span className={`text-sm ${suspended ? "text-amber-900" : "text-gray-600"}`}>
        {suspended
          ? "⏸ Suspended — this env's workload is scaled down. Resume to bring it back."
          : "Suspend this env to scale its workload down without deleting it (no data loss)."}
      </span>
      <button
        onClick={toggleSuspend}
        disabled={busy}
        className="shrink-0 rounded-md border border-gray-300 bg-white px-3 py-1 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
      >
        {busy ? "…" : suspended ? "Resume" : "Suspend"}
      </button>
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

      {/* Pin a preview to a stable env (promote without merging) / unpin */}
      <PinControls
        project={project}
        app={data.name}
        currentEnv={currentEnv}
        environments={data.environments}
        isDirect={data.deliveryMode === "direct"}
        onChanged={onSaved}
      />

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
      <ComponentsTable
        components={data.components}
        currentEnv={currentEnv}
        project={project}
        appName={data.name}
        envNames={data.environments
          .filter((e) => e.envType !== "preview")
          .map((e) => e.envName)}
        onSaved={onSaved}
      />

      {/* Add / remove / retemplate components (edit-composed). */}
      <ComponentsEditor data={data} project={project} onSaved={onSaved} />

      {/* App-level settings panel: values editor (single-source only — composed
          apps edit per-component values in the Components section above) PLUS the
          delivery/environments/clusters/CD/images/previews settings, which are
          app-level and apply to composed apps too. */}
      <AppValuesEditor
        data={data}
        project={project}
        currentEnvName={currentEnv?.envName ?? null}
        onSaved={onSaved}
        composed={data.components.length > 1}
      />

      {/* Legacy structured config (from the old form) — frozen, read-only. */}
      <LegacyConfigNotice data={data} />

      {/* Per-cluster overrides — only for fan-out environments. Advanced. */}
      <Disclosure title="Per-cluster overrides (advanced)">
        <ClusterOverridesEditor data={data} project={project} onSaved={onSaved} />
      </Disclosure>

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
          // available=false → no ArgoCD history reader wired; show "unavailable".
          // (Tolerate older servers that omit the field by treating undefined as available.)
          setUnavailable(res.available === false);
          setHistoryData(res.available === false ? null : res);
          setLoading(false);
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          // A 501 from an older server still maps to "unavailable".
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
  baseEnv,
  onDeletePreview,
}: {
  previewEnvs: AppEnvironmentSummary[];
  baseEnv?: string;
  onDeletePreview: (previewName: string) => void;
}) {
  const [confirmingDelete, setConfirmingDelete] = useState<string | null>(null);

  if (previewEnvs.length === 0) {
    return (
      <div className="rounded-xl border border-dashed border-gray-200 bg-white px-6 py-12 text-center">
        <p className="text-sm font-medium text-gray-500">
          No preview environments{baseEnv ? ` for ${baseEnv}` : ""}
        </p>
        <p className="mx-auto mt-1 max-w-lg text-xs text-gray-400">
          A preview is an ephemeral copy of this app — typically one per pull
          request. It clones a base env (default: staging) plus the app's
          preview overrides, runs in its own namespace, and is torn down when you
          delete it. Use the <span className="font-medium">Preview</span> button
          above to create one, or automate it from CI (see docs/previews.md).
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
              {env.preview?.baseEnv && (
                <span className="ml-2 text-xs text-gray-400">
                  ↳ from {env.preview.baseEnv}
                </span>
              )}
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
function componentVisibilityBadge(comp: ComponentSummary) {
  if (comp.type === "job") {
    return (
      <span className="rounded-full bg-amber-50 px-2 py-0.5 text-xs font-medium text-amber-600">
        one-shot
      </span>
    );
  }
  if (comp.type === "web") {
    // A composed web component carries an explicit exposeMode; a legacy web
    // component (no mode) is exposed by default.
    const label =
      comp.exposeMode === "internal"
        ? "internal"
        : comp.exposeMode === "disabled"
          ? "not exposed"
          : "external";
    return (
      <span className="rounded-full bg-blue-50 px-2 py-0.5 text-xs font-medium text-blue-600">
        {label}
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
  project,
  appName,
  envNames,
  onSaved,
}: {
  components: ComponentSummary[];
  currentEnv: AppEnvironmentSummary | null;
  project: string;
  appName: string;
  envNames: string[];
  onSaved: () => Promise<void>;
}) {
  const isMulti = components.length > 1;
  // Composed = a multi-source app (more than one component). In the unified model
  // every component carries a template, so the signal is component count.
  const composed = components.length > 1;
  const [expanded, setExpanded] = useState(true);

  if (components.length === 0) return null;

  const phase = currentEnv?.status.phase ?? "not_deployed";
  const totalReplicas = currentEnv?.status.replicas ?? 0;
  const availableReplicas = currentEnv?.status.available ?? 0;

  // Per-component live health (composed apps): the server reports each
  // component's own phase + replicas, keyed by component name. Empty for
  // single-component apps, where the env aggregate describes the sole workload.
  const componentStatus = new Map(
    (currentEnv?.status.components ?? []).map((c) => [c.component, c]),
  );

  // Replica attribution fallback for apps WITHOUT per-component status (older
  // server or single-component): only unambiguous when exactly one scalable
  // component exists; otherwise show "—" to avoid misleading fractions.
  const scalableComponents = components.filter(
    (c) => c.type === "web" || c.type === "worker",
  );
  const singleScalable = scalableComponents.length === 1;

  function replicaLabel(comp: ComponentSummary): string {
    if (comp.type === "cron" || comp.type === "job") return "—";
    if (singleScalable && totalReplicas > 0) {
      return `${availableReplicas}/${totalReplicas}`;
    }
    return "—";
  }

  const headerTitle = "Components";

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
          {composed && (
            <span
              className="rounded-full bg-indigo-50 px-2 py-0.5 text-xs font-medium text-indigo-600"
              title="Each component is rendered by its own template as one multi-source deployment"
            >
              composed
            </span>
          )}
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
            const cs = componentStatus.get(comp.name);
            // Prefer the component's own live status; fall back to the app-level
            // phase + the aggregate replica heuristic when it isn't reported.
            const compPhase = cs?.phase ?? phase;
            const replicas =
              comp.type === "cron" || comp.type === "job"
                ? "—"
                : cs
                  ? `${cs.available}/${cs.replicas}`
                  : replicaLabel(comp);
            return (
              <div key={comp.name} className="px-5 py-2.5">
                <div className="flex items-center justify-between">
                  {/* Left: name + type + visibility, plus the component's template. */}
                  <div className="flex min-w-0 flex-col gap-0.5">
                    <div className="flex min-w-0 items-center gap-2">
                      <span className="font-mono text-sm text-gray-900">
                        {comp.name}
                      </span>
                      <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs capitalize text-gray-500">
                        {comp.type}
                      </span>
                      {componentVisibilityBadge(comp)}
                    </div>
                    {comp.template && (
                      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-gray-400">
                        <span className="truncate">
                          template{" "}
                          <span className="font-medium text-gray-500">
                            {comp.template}
                          </span>
                        </span>
                        {comp.inheritAppVars === false && (
                          <span
                            className="rounded-full bg-amber-50 px-2 py-0.5 font-medium text-amber-700"
                            title={
                              (comp.envVars ?? []).length > 0
                                ? `Scoped env: ${(comp.envVars ?? [])
                                    .map((e) => e.name)
                                    .join(", ")}`
                                : "Does not inherit app vars"
                            }
                          >
                            scoped env
                            {(comp.envVars ?? []).length > 0
                              ? ` (${comp.envVars?.length})`
                              : ""}
                          </span>
                        )}
                      </div>
                    )}
                  </div>

                  {/* Right: replicas + status + preview eligibility */}
                  <div className="ml-4 flex flex-shrink-0 items-center gap-3">
                    {replicas !== "—" && (
                      <span className="text-xs text-gray-400">
                        {replicas}{" "}
                        <span className="text-gray-300">replicas</span>
                      </span>
                    )}
                    <StatusBadge status={compPhase} />
                    {!comp.enabledInPreview && (
                      <span className="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-500">
                        no preview
                      </span>
                    )}
                  </div>
                </div>

                {/* Composed apps: edit each component's own values overlay
                    (base + per-env). Single-component apps use the app-level
                    values editor, so keep a read-only disclosure here. */}
                {composed ? (
                  <ComponentValuesEditor
                    comp={comp}
                    project={project}
                    appName={appName}
                    envNames={envNames}
                    onSaved={onSaved}
                  />
                ) : (
                  comp.values &&
                  Object.keys(comp.values).length > 0 && (
                    <ComponentValues values={comp.values} />
                  )
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

// ComponentsEditor is the edit-composed surface: it opens the compose canvas
// seeded from the app's current components so you can add, remove, or retemplate
// them, then saves the whole list via updateApp({ components }). Turning an app
// composed (≥2 components) or back to single re-publishes in the new mode (the
// publisher prunes the stale-mode gitops tree). Available for every app so a
// single app can be split into components.
function ComponentsEditor({
  data,
  project,
  onSaved,
}: {
  data: AppDetailType;
  project: string;
  onSaved: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [drafts, setDrafts] = useState<ComponentDraft[]>([]);
  const [templates, setTemplates] = useState<TemplateSummary[]>([]);
  const [configVars, setConfigVars] = useState<ConfigVariables | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function begin() {
    setDrafts(data.components.map(draftFromSummary));
    setError(null);
    setOpen(true);
    if (templates.length === 0) {
      fetchTemplates()
        .then((r) => setTemplates(r.templates))
        .catch(() => {});
    }
    if (!configVars) {
      listConfigVariables(project)
        .then(setConfigVars)
        .catch(() => setConfigVars({ platform: [], vars: [] }));
    }
  }

  async function save() {
    if (drafts.length === 0) {
      setError("Add at least one component.");
      return;
    }
    if (drafts.some((d) => d.valuesError)) {
      setError("Fix the component values YAML before saving.");
      return;
    }
    const names = new Set<string>();
    for (const d of drafts) {
      const nm = d.name.trim();
      if (!nm) {
        setError("Every component needs a name.");
        return;
      }
      if (names.has(nm)) {
        setError(`Duplicate component name "${nm}".`);
        return;
      }
      names.add(nm);
      if (!d.template) {
        setError(`Component "${nm}" needs a template.`);
        return;
      }
    }
    setSaving(true);
    setError(null);
    try {
      await updateApp(project, data.name, {
        components: drafts.map(toComponentCreate),
      });
      toast.success("Components updated — re-publishing to GitOps.");
      await onSaved();
      setOpen(false);
    } catch (e) {
      setError(e instanceof Error ? e.message : "Failed to save components");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="rounded-xl border border-gray-200 bg-white">
      <div className="flex items-center justify-between border-b border-gray-100 px-5 py-3">
        <h2 className="text-xs font-medium uppercase tracking-wider text-gray-400">
          Edit components
        </h2>
        {!open && (
          <button
            type="button"
            onClick={begin}
            className="rounded-md border border-gray-300 px-3 py-1.5 text-xs font-medium text-gray-700 transition-colors hover:bg-gray-50"
          >
            {data.components.length > 1 ? "Edit components" : "Add components"}
          </button>
        )}
      </div>
      {open && (
        <div className="space-y-4 p-5">
          <p className="text-xs text-gray-400">
            Add, remove, or retemplate components. Two or more components make this
            a composed app (one chart source each); a single component renders as
            one chart. Saving re-publishes to GitOps.
          </p>
          <ComposeComponents
            templates={templates}
            components={drafts}
            onChange={setDrafts}
            configVars={configVars}
          />
          {error && <p className="text-sm text-red-600">{error}</p>}
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={save}
              disabled={saving}
              className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-gray-800 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {saving ? "Saving…" : "Save components"}
            </button>
            <button
              type="button"
              onClick={() => setOpen(false)}
              disabled={saving}
              className="rounded-md px-4 py-2 text-sm font-medium text-gray-600 transition-colors hover:bg-gray-100"
            >
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

// ComponentValues is a read-only disclosure of a component's Helm values overlay
// (its value-based config), shown under the component on the app detail page.
function ComponentValues({ values }: { values: Record<string, unknown> }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="mt-2">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="inline-flex items-center gap-1 text-xs font-medium text-gray-400 hover:text-gray-600"
      >
        <span className={`transition-transform ${open ? "rotate-90" : ""}`}>
          ▸
        </span>
        values
      </button>
      {open && (
        <pre className="mt-1 max-h-64 overflow-auto rounded-md bg-gray-50 p-2.5 font-mono text-xs leading-relaxed text-gray-600">
          {stringifyOverlay(values)}
        </pre>
      )}
    </div>
  );
}

// ComponentValuesEditor edits ONE composed component's Helm values overlay — its
// base (all-envs) overlay plus a per-environment override for each env. The base
// saves via componentValues; a per-env scope saves via envComponentValues, each
// deep-merged over the base for that env at publish.
const COMP_BASE_SCOPE = "__base__";

function ComponentValuesEditor({
  comp,
  project,
  appName,
  envNames,
  onSaved,
}: {
  comp: ComponentSummary;
  project: string;
  appName: string;
  envNames: string[];
  onSaved: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [scope, setScope] = useState(COMP_BASE_SCOPE);
  const savedText = (s: string): string =>
    stringifyOverlay(
      s === COMP_BASE_SCOPE ? comp.values : comp.envValues?.[s],
    );
  const [texts, setTexts] = useState<Record<string, string>>(() => {
    const m: Record<string, string> = { [COMP_BASE_SCOPE]: savedText(COMP_BASE_SCOPE) };
    for (const e of envNames) m[e] = savedText(e);
    return m;
  });
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const text = texts[scope] ?? "";
  const dirty = text !== savedText(scope);

  async function save() {
    const parsed = parseYamlOverlay(text);
    if (parsed.error) {
      setErr(parsed.error);
      return;
    }
    setErr(null);
    setSaving(true);
    try {
      const req: UpdateAppRequest =
        scope === COMP_BASE_SCOPE
          ? { componentValues: { [comp.name]: parsed.value ?? {} } }
          : { envComponentValues: { [scope]: { [comp.name]: parsed.value ?? {} } } };
      await updateApp(project, appName, req);
      toast.success("Component values saved — re-publishing to GitOps.");
      await onSaved();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save values");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="mt-2">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="inline-flex items-center gap-1 text-xs font-medium text-gray-400 hover:text-gray-600"
      >
        <span className={`transition-transform ${open ? "rotate-90" : ""}`}>▸</span>
        values
      </button>
      {open && (
        <div className="mt-1.5 space-y-2">
          {/* Scope: base (all envs) + one tab per environment. */}
          {envNames.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {[COMP_BASE_SCOPE, ...envNames].map((s) => {
                const active = scope === s;
                const overridden =
                  s !== COMP_BASE_SCOPE &&
                  comp.envValues?.[s] &&
                  Object.keys(comp.envValues[s]).length > 0;
                return (
                  <button
                    key={s}
                    type="button"
                    onClick={() => setScope(s)}
                    className={`rounded px-2 py-0.5 text-[11px] font-medium transition-colors ${
                      active
                        ? "bg-gray-900 text-white"
                        : "bg-gray-100 text-gray-600 hover:bg-gray-200"
                    }`}
                  >
                    {s === COMP_BASE_SCOPE ? "all envs" : s}
                    {overridden ? " ●" : ""}
                  </button>
                );
              })}
            </div>
          )}
          <textarea
            value={text}
            onChange={(e) =>
              setTexts((prev) => ({ ...prev, [scope]: e.target.value }))
            }
            spellCheck={false}
            rows={8}
            placeholder={
              scope === COMP_BASE_SCOPE
                ? "# values for this component (all envs)"
                : `# ${scope} overrides — deep-merged over the base for ${scope}`
            }
            className="block w-full rounded-md border border-gray-300 bg-gray-50 p-2.5 font-mono text-xs leading-relaxed text-gray-700 focus:border-gray-900 focus:outline-none focus:ring-1 focus:ring-gray-900"
          />
          {err && <p className="text-xs text-red-600">{err}</p>}
          <div className="flex items-center gap-3">
            <button
              type="button"
              onClick={save}
              disabled={saving || !dirty}
              className="rounded-md bg-gray-900 px-3 py-1.5 text-xs font-medium text-white transition-colors hover:bg-gray-800 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {saving ? "Saving…" : "Save"}
            </button>
            {scope !== COMP_BASE_SCOPE && (
              <span className="text-[11px] text-gray-400">
                overrides {scope} only · deep-merged over the base
              </span>
            )}
          </div>
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
  "project-environment": { bg: "bg-amber-50 text-amber-800", label: "Project-Env" },
  stack: { bg: "bg-teal-50 text-teal-700", label: "Stack" },
  "stack-environment": { bg: "bg-teal-50 text-teal-800", label: "Stack-Env" },
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
  // Sort by promotion order so stableEnvs[0] is the first stable env (the
  // default preview base env), matching the server's base-env resolution.
  const stableEnvs = environments
    .filter((e) => e.envType !== "preview")
    .sort((a, b) => a.order - b.order || a.envName.localeCompare(b.envName));
  const activeEnv = selectedEnvName ?? stableEnvs[0]?.envName ?? null;
  // Base env for the preview band — its items live in this env's vault.
  const previewBandEnv = stableEnvs[0]?.envName ?? null;

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

      {/* Preview band — one config applied to every preview, layered on top of
          the base env. Stored as preview-scoped items inside the base env vault
          (no per-preview vault). */}
      {previewBandEnv && (
        <div className="space-y-4 rounded-lg border border-indigo-100 bg-indigo-50/30 p-4">
          <p className="text-xs font-medium uppercase tracking-wider text-indigo-500">
            Preview band — applies to every preview (base env: {previewBandEnv})
          </p>
          <EnvConfigEditor
            key="appenv-preview"
            title="Preview variables"
            description="Variables applied to every preview of this app, on top of its base env. Overridden by per-preview values."
            fetchFn={() => getAppEnvEnvConfig(project, appName, "preview")}
            saveFn={(cfg) => updateAppEnvEnvConfig(project, appName, "preview", cfg)}
          />
          <SecretEditor
            key="secrets-preview"
            title="Preview secrets"
            description={`Secrets applied to every preview, on top of the base env. Stored as the "${appName}-env-preview" item in the "${previewBandEnv}" env vault.`}
            fetchFn={() =>
              listAppPreviewSecretKeys(project, appName, previewBandEnv)
            }
            upsertFn={(entries) =>
              upsertAppPreviewSecrets(project, appName, previewBandEnv, entries)
            }
            deleteFn={(key) =>
              deleteAppPreviewSecretKey(project, appName, previewBandEnv, key)
            }
          />
        </div>
      )}

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
