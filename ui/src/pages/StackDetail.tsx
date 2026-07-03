import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";

import { ApiError } from "../lib/api";
import { listApps } from "../lib/apps";
import { listPreviewGroups } from "../lib/previews";
import { AppTable } from "../components/AppTable";
import { PreviewGroupCard } from "../components/PreviewGroupCard";
import type { AppSummary, PreviewGroup } from "../types";
import { listProjectEnvironments } from "../lib/projects";
import type { ProjectEnvironment } from "../lib/projects";
import {
  cloneStack,
  createStackPreview,
  deleteStack,
  getStack,
  pinStack,
  promoteStack,
  resumeStack,
  setAppStack,
  setStackTargetClusters,
  suspendStack,
  syncStack,
  unpinStack,
  updateStack,
} from "../lib/stacks";
import type { Stack, StackBatchResponse, StackOpResult } from "../lib/stacks";
import { listOrgEnvironments } from "../lib/settings";
import type { OrgEnvironment } from "../lib/settings";
import type { EnvConfig } from "../lib/envconfig";
import {
  listStackGlobalSecretKeys,
  upsertStackGlobalSecrets,
  deleteStackGlobalSecretKey,
  listStackEnvSecretKeys,
  upsertStackEnvSecrets,
  deleteStackEnvSecretKey,
} from "../lib/secrets";
import { EnvConfigEditor } from "../components/EnvConfigEditor";
import { SecretEditor } from "../components/SecretEditor";

const TABS = [
  { id: "overview", label: "Overview" },
  { id: "previews", label: "Previews" },
  { id: "variables", label: "Variables" },
  { id: "secrets", label: "Secrets" },
  { id: "settings", label: "Settings" },
] as const;
type TabId = (typeof TABS)[number]["id"];

type ModalKind = "promote" | "preview" | "clone" | "delete" | "suspend";

const btnPrimary =
  "rounded-md bg-gray-900 px-3.5 py-2 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50";
const btnSecondary =
  "rounded-md border border-gray-300 bg-white px-3.5 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50";

// Modal is the shared backdrop + panel used for the input-driven batch actions,
// mirroring the hand-rolled modals on AppDetail.
function Modal({
  title,
  description,
  onClose,
  closable,
  children,
}: {
  title: string;
  description?: string;
  onClose: () => void;
  closable: boolean;
  children: ReactNode;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="w-full max-w-md rounded-2xl bg-white p-6 shadow-xl">
        <div className="flex items-start justify-between gap-4">
          <h3 className="text-lg font-semibold text-gray-900">{title}</h3>
          <button
            onClick={onClose}
            disabled={!closable}
            className="text-gray-400 hover:text-gray-600 disabled:opacity-40"
            aria-label="Close"
          >
            ✕
          </button>
        </div>
        {description && <p className="mt-1 text-sm text-gray-500">{description}</p>}
        {children}
      </div>
    </div>
  );
}

// ResultRows renders the per-app outcome of a batch op inside a modal.
function ResultRows({ results }: { results: StackOpResult[] }) {
  if (results.length === 0) {
    return <p className="mt-4 text-sm text-gray-400">No member apps were affected.</p>;
  }
  return (
    <ul className="mt-4 max-h-52 space-y-1 overflow-auto rounded-md border border-gray-100 bg-gray-50 p-3 text-xs">
      {results.map((r) => (
        <li
          key={r.app}
          className={
            !r.ok ? "text-red-600" : r.skipped ? "text-gray-400" : "text-gray-600"
          }
        >
          <span className="font-mono font-medium">{r.app}</span>
          {" — "}
          {r.skipped ? `skipped (${r.message})` : r.ok ? r.message || "ok" : r.error}
        </li>
      ))}
    </ul>
  );
}

// effectiveClusters resolves which cluster(s) an environment deploys to by
// default: every clusterRef in "all" (fan-out) mode, else the active one
// (falling back to the first). Mirrors NewService's helper of the same name.
function effectiveClusters(env: OrgEnvironment): string[] {
  if ((env.deployMode ?? "active") === "all") {
    return env.clusterRefs ?? [];
  }
  const active = env.activeClusterRef || env.clusterRefs?.[0] || "";
  return active ? [active] : [];
}

// sameSet reports whether two string slices contain the same elements.
function sameSet(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  const s = new Set(a);
  return b.every((x) => s.has(x));
}

// stackClustersForEnv maps a per-env UI selection to the wire `clusters` value
// for setStackTargetClusters: empty or == the env's DeployMode default → [] (clear
// the per-member override, inherit the default); all of the env's clusters →
// ["*"] (track the env dynamically); any other set → that explicit subset.
function stackClustersForEnv(env: OrgEnvironment, chosen: string[]): string[] {
  const refs = env.clusterRefs ?? [];
  if (chosen.length === 0) return []; // clear → inherit default
  if (sameSet(chosen, refs)) return ["*"]; // all → dynamic
  if (sameSet(chosen, effectiveClusters(env))) return []; // == default → inherit
  return [...chosen].sort();
}

export function StackDetail() {
  const { project, stack: stackName } = useParams<{ project: string; stack: string }>();
  const navigate = useNavigate();
  const [stack, setStack] = useState<Stack | null>(null);
  const [allApps, setAllApps] = useState<AppSummary[]>([]);
  const [envs, setEnvs] = useState<ProjectEnvironment[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [addApp, setAddApp] = useState("");
  const [activeTab, setActiveTab] = useState<TabId>("overview");
  const [busy, setBusy] = useState<string | null>(null);
  const [modal, setModal] = useState<ModalKind | null>(null);
  const [results, setResults] = useState<StackOpResult[] | null>(null);
  const [promoteEnv, setPromoteEnv] = useState("");
  const [suspendEnv, setSuspendEnv] = useState("");
  const [previewName, setPreviewName] = useState("");
  const [cloneName, setCloneName] = useState("");
  const [previewGroups, setPreviewGroups] = useState<PreviewGroup[]>([]);
  // Per-preview-group selected pin target env (keyed by preview name).
  const [pinTargets, setPinTargets] = useState<Record<string, string>>({});
  // Org environments (excluding "preview") — drives the Target clusters control.
  const [orgEnvs, setOrgEnvs] = useState<OrgEnvironment[]>([]);
  // Per-env chosen cluster names for the Target clusters control (UI state). An
  // env absent here defaults to its DeployMode default; see stackClustersForEnv
  // for the wire mapping. Only meaningful for envs with more than one cluster.
  const [targetSel, setTargetSel] = useState<Record<string, string[]>>({});

  const loadPreviews = useCallback(() => {
    if (!project) return;
    listPreviewGroups(project)
      .then((r) => setPreviewGroups(r.previews ?? []))
      .catch(() => setPreviewGroups([]));
  }, [project]);

  const reload = useCallback(async () => {
    if (!project || !stackName) return;
    try {
      const [s, apps, envList] = await Promise.all([
        getStack(project, stackName),
        listApps(project),
        listProjectEnvironments(project),
      ]);
      setStack(s);
      setAllApps(apps.apps);
      setEnvs(envList);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load stack");
    }
  }, [project, stackName]);

  useEffect(() => {
    reload();
  }, [reload]);

  useEffect(() => {
    loadPreviews();
  }, [loadPreviews]);

  useEffect(() => {
    listOrgEnvironments()
      .then((res) =>
        setOrgEnvs((res.environments ?? []).filter((e) => e.name !== "preview")),
      )
      .catch(() => setOrgEnvs([]));
  }, []);

  if (!project || !stackName) return null;
  if (error) return <div className="p-6 text-sm text-red-600">{error}</div>;
  if (!stack) return <div className="p-6 text-sm text-gray-400">Loading…</div>;

  const memberApps = stack.apps ?? [];
  const members = new Set(memberApps);
  const addable = allApps.filter((a) => !members.has(a.name)).map((a) => a.name);
  const noMembers = memberApps.length === 0;
  // Member apps as full summaries (with per-env status), in stack order.
  const byName = new Map(allApps.map((a) => [a.name, a]));
  const memberSummaries: AppSummary[] = memberApps
    .map((n) => byName.get(n))
    .filter((a): a is AppSummary => a !== undefined);
  // PR previews scoped to this stack: within each PR group keep only member-app
  // previews, and drop PRs that touch none of the stack's apps.
  const stackPreviews: PreviewGroup[] = previewGroups
    .map((g) => ({ ...g, apps: g.apps.filter((a) => members.has(a.appName)) }))
    .filter((g) => g.apps.length > 0);

  function openModal(kind: ModalKind) {
    setResults(null);
    setModal(kind);
  }
  function closeModal() {
    if (busy) return; // don't close mid-flight
    setModal(null);
    setResults(null);
    setPromoteEnv("");
    setSuspendEnv("");
    setPreviewName("");
    setCloneName("");
  }

  async function move(app: string, toStack: string) {
    try {
      await setAppStack(project!, app, toStack);
      toast.success(toStack ? `Added ${app} to ${stackName}` : `Removed ${app} from ${stackName}`);
      setAddApp("");
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to update membership");
    }
  }

  // summarize toasts the per-app outcome of a batch op (used by Sync, which has
  // no modal of its own).
  function summarize(action: string, res: StackBatchResponse) {
    const failed = res.results.filter((r) => !r.ok);
    const skipped = res.results.filter((r) => r.ok && r.skipped);
    const done = res.results.length - failed.length - skipped.length;
    if (failed.length === 0) {
      toast.success(
        `${action}: ${done} app(s) succeeded${skipped.length ? `, ${skipped.length} skipped` : ""}`,
      );
    } else {
      toast.error(
        `${action}: ${failed.length}/${res.results.length} failed — ${failed
          .map((r) => `${r.app}: ${r.error}`)
          .join("; ")}`,
      );
    }
  }

  // doPin pins a whole PR preview group to a stable env across the stack's
  // pipeline members (direct members / members without the preview are skipped).
  async function doPin(previewName: string, targetEnv: string) {
    if (!targetEnv) return;
    setBusy(`pin:${previewName}`);
    try {
      summarize(
        `Pin ${previewName} → ${targetEnv}`,
        await pinStack(project!, stackName!, { fromPreview: previewName, targetEnv }),
      );
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to pin preview");
    } finally {
      setBusy(null);
    }
  }

  // doSuspend/doResume scale the stack's workloads down/up for a chosen env,
  // showing the per-member result rows in the suspend modal.
  async function doSuspendResume(resume: boolean) {
    if (!suspendEnv) return;
    const kind = resume ? "resume" : "suspend";
    setBusy(kind);
    try {
      const res = resume
        ? await resumeStack(project!, stackName!, suspendEnv)
        : await suspendStack(project!, stackName!, suspendEnv);
      setResults(res.results);
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : `Failed to ${kind}`);
    } finally {
      setBusy(null);
    }
  }

  // doUnpin clears the stack's pin on a stable env (restores each member's
  // pre-pin image).
  async function doUnpin(previewName: string, targetEnv: string) {
    setBusy(`unpin:${previewName}`);
    try {
      summarize(
        `Unpin ${targetEnv}`,
        await unpinStack(project!, stackName!, targetEnv),
      );
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to unpin");
    } finally {
      setBusy(null);
    }
  }

  // doSetTargetClusters applies the chosen cluster selection for one stable env
  // across the stack's members. Members not deployed to that env are skipped; a
  // default/empty selection clears each member's override back to the env default.
  async function doSetTargetClusters(env: OrgEnvironment) {
    const chosen = targetSel[env.name] ?? effectiveClusters(env);
    const clusters = stackClustersForEnv(env, chosen);
    setBusy(`target:${env.name}`);
    try {
      summarize(
        `Target clusters ${env.displayName || env.name}`,
        await setStackTargetClusters(project!, stackName!, {
          targetEnv: env.name,
          clusters,
        }),
      );
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to set target clusters");
    } finally {
      setBusy(null);
    }
  }

  async function doSync() {
    setBusy("sync");
    try {
      summarize("Sync", await syncStack(project!, stackName!));
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to sync stack");
    } finally {
      setBusy(null);
    }
  }

  // runModalBatch runs an action that shows its per-app results inside the open
  // modal (Promote, Preview) rather than only a toast.
  async function runModalBatch(kind: string, fn: () => Promise<StackBatchResponse>) {
    setBusy(kind);
    try {
      const res = await fn();
      setResults(res.results);
      await reload();
      loadPreviews(); // Preview creates previews; refresh the Previews tab.
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : `Failed to ${kind}`);
    } finally {
      setBusy(null);
    }
  }

  async function doClone() {
    if (!cloneName) return;
    setBusy("clone");
    try {
      const res = await cloneStack(project!, stackName!, { newName: cloneName });
      const failed = res.results.filter((r) => !r.ok);
      if (failed.length) {
        toast.error(
          `Cloned, but ${failed.length} app(s) failed — ${failed.map((r) => `${r.app}: ${r.error}`).join("; ")}`,
        );
      } else {
        toast.success(
          `Cloned to ${cloneName} (${res.results.length} app(s)). Re-enter app-level secrets under the new apps.`,
        );
      }
      navigate(`/projects/${encodeURIComponent(project!)}/stacks/${encodeURIComponent(cloneName)}`);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to clone stack");
      setBusy(null);
    }
  }

  async function doDelete(deleteApps: boolean) {
    if (deleteApps && !confirm(`Delete stack "${stackName}" AND all ${memberApps.length} member app(s)? This cannot be undone.`)) {
      return;
    }
    setBusy("delete");
    try {
      await deleteStack(project!, stackName!, deleteApps);
      toast.success(deleteApps ? `Stack ${stackName} and its apps deleted` : `Stack ${stackName} deleted`);
      navigate(`/projects/${encodeURIComponent(project!)}`);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to delete stack");
      setBusy(null);
    }
  }

  async function toggleSharedNamespace(checked: boolean) {
    try {
      await updateStack(project!, stackName!, { sharedNamespace: checked });
      toast.success(checked ? "Members will co-locate in one namespace" : "Members will use their own namespaces");
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to update stack");
    }
  }

  async function toggleAutoPromote(checked: boolean) {
    try {
      await updateStack(project!, stackName!, { autoPromote: checked });
      toast.success(checked ? "Member apps will auto-promote to prod" : "Member apps revert to manual promotion");
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to update stack");
    }
  }

  return (
    <div className="space-y-6">
      {/* Header */}
      <div>
        <div className="text-sm text-gray-500">
          <Link to={`/projects/${encodeURIComponent(project)}`} className="hover:text-gray-700">
            {project}
          </Link>{" "}
          / Stacks
        </div>
        <div className="mt-1 flex items-start justify-between gap-4">
          <div>
            <div className="flex items-center gap-2">
              <h1 className="text-2xl font-semibold text-gray-900">{stack.displayName || stack.name}</h1>
              <span className="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-700">
                Beta
              </span>
            </div>
            {stack.description && <p className="mt-1 text-sm text-gray-500">{stack.description}</p>}
            <div className="mt-2 flex items-center gap-2 text-xs">
              <span className="rounded-full bg-gray-100 px-2.5 py-0.5 font-medium text-gray-600">
                {memberApps.length} app{memberApps.length === 1 ? "" : "s"}
              </span>
              {stack.sharedNamespace && (
                <span className="rounded-full bg-indigo-50 px-2.5 py-0.5 font-medium text-indigo-700">
                  Shared namespace
                </span>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Quick actions */}
      <div className="flex flex-wrap gap-2">
        <button onClick={doSync} disabled={busy !== null || noMembers} className={btnSecondary}>
          {busy === "sync" ? "Syncing…" : "Sync"}
        </button>
        <button onClick={() => openModal("promote")} disabled={busy !== null || noMembers} className={btnPrimary}>
          Promote
        </button>
        <button onClick={() => openModal("preview")} disabled={busy !== null || noMembers} className={btnSecondary}>
          Preview
        </button>
        <button onClick={() => openModal("suspend")} disabled={busy !== null || noMembers} className={btnSecondary}>
          Suspend / Resume
        </button>
        <button onClick={() => openModal("clone")} disabled={busy !== null} className={btnSecondary}>
          Clone
        </button>
      </div>

      {/* Tabs */}
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

      {/* Overview — members */}
      {activeTab === "overview" && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-6 py-4">
            <h2 className="text-base font-medium text-gray-900">Apps in this stack</h2>
            <p className="mt-0.5 text-sm text-gray-500">
              Tightly-coupled apps grouped here share the stack's overrides
              {stack.sharedNamespace ? " and namespace" : ""}. Each keeps its own ArgoCD/Kargo pipeline.
            </p>
          </div>
          <div className="px-6 py-4">
            {noMembers ? (
              <p className="text-sm text-gray-400">No apps yet. Add one below.</p>
            ) : (
              <AppTable
                project={project}
                apps={memberSummaries}
                rowAction={(app) => (
                  <button
                    onClick={() => move(app.name, "")}
                    className="text-xs font-medium text-gray-500 hover:text-red-600"
                  >
                    Remove
                  </button>
                )}
              />
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2 border-t border-gray-100 px-6 py-3">
            <Link
              to={`/projects/${encodeURIComponent(project)}/apps/new?stack=${encodeURIComponent(stackName)}`}
              className="rounded-md bg-gray-900 px-3 py-1.5 text-sm font-medium text-white hover:bg-gray-700"
            >
              + New app
            </Link>
            {addable.length > 0 && (
              <>
                <span className="text-xs text-gray-400">or attach existing:</span>
                <select
                  value={addApp}
                  onChange={(e) => setAddApp(e.target.value)}
                  className="rounded-md border border-gray-300 px-3 py-1.5 text-sm"
                >
                  <option value="">Add an app…</option>
                  {addable.map((a) => (
                    <option key={a} value={a}>
                      {a}
                    </option>
                  ))}
                </select>
                <button
                  onClick={() => addApp && move(addApp, stackName)}
                  disabled={!addApp}
                  className="rounded-md border border-gray-300 px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
                >
                  Attach
                </button>
              </>
            )}
          </div>
        </div>
      )}

      {/* Previews — PR previews scoped to this stack's member apps */}
      {activeTab === "previews" && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-6 py-4">
            <h2 className="text-base font-medium text-gray-900">Previews</h2>
            <p className="mt-0.5 text-sm text-gray-500">
              PR previews of this stack's apps — one item per PR, showing the
              member apps deployed for it.
            </p>
          </div>
          <div className="space-y-3 p-6">
            {stackPreviews.length === 0 ? (
              <p className="text-sm text-gray-400">
                No previews for this stack's apps yet. Use{" "}
                <span className="font-medium">Preview</span> above to create one
                across the stack, or open a PR with the preview workflow.
              </p>
            ) : (
              stackPreviews.map((g) => {
                const target = pinTargets[g.name] ?? envs[0]?.name ?? "";
                const pinning = busy === `pin:${g.name}`;
                const unpinning = busy === `unpin:${g.name}`;
                return (
                  <div key={`${g.project}/${g.name}`} className="space-y-2">
                    <PreviewGroupCard group={g} onAppDeleted={loadPreviews} />
                    {/* Pin this PR's build to a stable env across the stack.
                        Each pipeline member pins its own image; direct members
                        and members without this preview are skipped. */}
                    <div className="flex flex-wrap items-center gap-2 pl-1 text-xs text-gray-500">
                      <span>Pin {g.name} to</span>
                      <select
                        value={target}
                        onChange={(e) =>
                          setPinTargets((m) => ({ ...m, [g.name]: e.target.value }))
                        }
                        disabled={busy !== null}
                        className="rounded border border-gray-200 px-2 py-1 text-xs"
                      >
                        {envs.map((env) => (
                          <option key={env.name} value={env.name}>
                            {env.displayName || env.name}
                          </option>
                        ))}
                      </select>
                      <button
                        onClick={() => doPin(g.name, target)}
                        disabled={busy !== null || !target}
                        className={btnSecondary}
                      >
                        {pinning ? "Pinning…" : "Pin"}
                      </button>
                      <button
                        onClick={() => doUnpin(g.name, target)}
                        disabled={busy !== null || !target}
                        className="text-xs text-gray-400 underline hover:text-gray-600"
                      >
                        {unpinning ? "Unpinning…" : "Unpin env"}
                      </button>
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </div>
      )}

      {/* Variables */}
      {activeTab === "variables" && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-6 py-4">
            <h2 className="text-base font-medium text-gray-900">Stack variables</h2>
            <p className="mt-0.5 text-sm text-gray-500">
              Applied to every app in this stack. Override project defaults; overridden by app-level values.
            </p>
          </div>
          <div className="space-y-6 p-6">
            <EnvConfigEditor
              title="Global (all environments)"
              description="Stack variables identical in every environment."
              fetchFn={async (): Promise<EnvConfig> => (await getStack(project, stackName)).envConfig ?? {}}
              saveFn={(cfg: EnvConfig) => updateStack(project, stackName, { envConfig: cfg })}
            />
            {envs.map((env) => (
              <EnvConfigEditor
                key={env.name}
                title={`${env.displayName || env.name} variables`}
                description={`Stack variables for the ${env.name} environment. Override the global stack variables above.`}
                fetchFn={async (): Promise<EnvConfig> =>
                  (await getStack(project, stackName)).envConfigByEnv?.[env.name] ?? {}
                }
                saveFn={async (cfg: EnvConfig) => {
                  const fresh = await getStack(project, stackName);
                  await updateStack(project, stackName, {
                    envConfigByEnv: { ...(fresh.envConfigByEnv ?? {}), [env.name]: cfg },
                  });
                }}
              />
            ))}
          </div>
        </div>
      )}

      {/* Secrets */}
      {activeTab === "secrets" && (
        <div className="rounded-xl border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-6 py-4">
            <h2 className="text-base font-medium text-gray-900">Stack secrets</h2>
            <p className="mt-0.5 text-sm text-gray-500">
              Shared by every app in this stack. Override project secrets; overridden by app-level secrets.
            </p>
          </div>
          <div className="space-y-6 p-6">
            <SecretEditor
              title="Global (all environments)"
              description="Stack secrets identical in every environment."
              fetchFn={() => listStackGlobalSecretKeys(project, stackName)}
              upsertFn={(e) => upsertStackGlobalSecrets(project, stackName, e)}
              deleteFn={(k) => deleteStackGlobalSecretKey(project, stackName, k)}
            />
            {envs.map((env) => (
              <SecretEditor
                key={env.name}
                title={`${env.displayName || env.name} secrets`}
                description={`Stack secrets for the ${env.name} environment.`}
                fetchFn={() => listStackEnvSecretKeys(project, stackName, env.name)}
                upsertFn={(e) => upsertStackEnvSecrets(project, stackName, env.name, e)}
                deleteFn={(k) => deleteStackEnvSecretKey(project, stackName, env.name, k)}
              />
            ))}
          </div>
        </div>
      )}

      {/* Settings */}
      {activeTab === "settings" && (
        <div className="space-y-6">
          <div className="rounded-xl border border-gray-200 bg-white px-6 py-4">
            <label className="flex items-start gap-3">
              <input
                type="checkbox"
                checked={stack.sharedNamespace ?? false}
                onChange={(e) => toggleSharedNamespace(e.target.checked)}
                className="mt-0.5"
              />
              <span className="text-sm">
                <span className="font-medium text-gray-900">Shared namespace</span>
                <span className="block text-xs text-gray-500">
                  Co-locate member apps in one{" "}
                  <code className="font-mono">
                    {stack.namespacePattern || `${project}-${stack.name}-<env>`}
                  </code>{" "}
                  namespace so they reach each other by in-cluster DNS (e.g.{" "}
                  <code className="font-mono">web → http://agent-server-web:8080</code>). Toggling relocates
                  the apps on the next sync.
                </span>
              </span>
            </label>
          </div>

          <div className="rounded-xl border border-gray-200 bg-white px-6 py-4">
            <label className="flex items-start gap-3">
              <input
                type="checkbox"
                checked={stack.autoPromote ?? false}
                onChange={(e) => toggleAutoPromote(e.target.checked)}
                className="mt-0.5"
              />
              <span className="text-sm">
                <span className="font-medium text-gray-900">Auto-promote member apps to prod</span>
                <span className="block text-xs text-gray-500">
                  Each member's image promotes to prod automatically once it's deployed and
                  healthy in staging — no manual step. Applies to all pipeline member apps;
                  manual “Promote” still works. Takes effect on the next sync.
                </span>
              </span>
            </label>
          </div>

          {/* Target clusters — per stable env, choose which of the env's
              clusters the stack's members deploy to. Only multi-cluster envs
              offer a choice; single-cluster envs are shown read-only. */}
          <div className="rounded-xl border border-gray-200 bg-white">
            <div className="border-b border-gray-100 px-6 py-4">
              <h2 className="text-base font-medium text-gray-900">Target clusters</h2>
              <p className="mt-0.5 text-sm text-gray-500">
                For a multi-cluster environment, pick which clusters this stack's
                member apps deploy to. Members not deployed to an environment are
                skipped. Applying the environment's default resets each member's
                override to inherit the environment default.
              </p>
            </div>
            <div className="px-6 py-2">
              {orgEnvs.length === 0 ? (
                <p className="py-2 text-sm text-gray-400">No environments.</p>
              ) : (
                <ul className="divide-y divide-gray-50">
                  {[...orgEnvs]
                    .sort((a, b) => a.order - b.order)
                    .map((env) => {
                      const refs = env.clusterRefs ?? [];
                      const multi = refs.length > 1;
                      const current = targetSel[env.name] ?? effectiveClusters(env);
                      const allSelected = multi && sameSet(current, refs);
                      const applying = busy === `target:${env.name}`;
                      function toggle(cluster: string) {
                        const next = current.includes(cluster)
                          ? current.filter((c) => c !== cluster)
                          : [...current, cluster];
                        setTargetSel((prev) => ({ ...prev, [env.name]: next }));
                      }
                      return (
                        <li key={env.name} className="py-3">
                          <div className="flex items-center justify-between gap-3">
                            <span className="text-sm font-medium text-gray-800">
                              {env.displayName || env.name}
                            </span>
                            {!multi && (
                              <span className="font-mono text-xs text-gray-500">
                                {refs[0] || env.activeClusterRef ? (
                                  refs[0] || env.activeClusterRef
                                ) : (
                                  <span className="text-amber-600">no cluster bound</span>
                                )}
                              </span>
                            )}
                          </div>
                          {multi && (
                            <div className="mt-2 flex flex-wrap items-center gap-1.5">
                              <button
                                type="button"
                                onClick={() =>
                                  setTargetSel((prev) => ({
                                    ...prev,
                                    [env.name]: allSelected
                                      ? effectiveClusters(env)
                                      : refs,
                                  }))
                                }
                                disabled={busy !== null}
                                className={`rounded border px-2 py-0.5 text-[11px] font-medium transition-colors disabled:opacity-50 ${
                                  allSelected
                                    ? "border-indigo-500 bg-indigo-50 text-indigo-700"
                                    : "border-gray-200 text-gray-500 hover:bg-gray-50"
                                }`}
                              >
                                All clusters
                              </button>
                              {refs.map((c) => {
                                const on = current.includes(c);
                                return (
                                  <button
                                    key={c}
                                    type="button"
                                    onClick={() => toggle(c)}
                                    disabled={busy !== null}
                                    className={`rounded border px-2 py-0.5 font-mono text-[11px] transition-colors disabled:opacity-50 ${
                                      on
                                        ? "border-indigo-500 bg-indigo-50 text-indigo-700"
                                        : "border-gray-200 text-gray-500 hover:bg-gray-50"
                                    }`}
                                  >
                                    {c}
                                    {c === env.activeClusterRef && (
                                      <span className="ml-1 text-[9px] uppercase text-gray-400">
                                        active
                                      </span>
                                    )}
                                  </button>
                                );
                              })}
                              <button
                                onClick={() => doSetTargetClusters(env)}
                                disabled={busy !== null}
                                className="ml-1 rounded-md border border-gray-300 bg-white px-3 py-1 text-xs font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
                              >
                                {applying ? "Applying…" : "Apply"}
                              </button>
                              {current.length === 0 && (
                                <span className="text-[11px] text-amber-600">
                                  applying with none clears the override (inherit default)
                                </span>
                              )}
                            </div>
                          )}
                        </li>
                      );
                    })}
                </ul>
              )}
            </div>
          </div>

          {/* Danger zone */}
          <div className="rounded-xl border border-red-200 bg-white">
            <div className="border-b border-red-100 px-6 py-4">
              <h2 className="text-base font-medium text-red-700">Danger zone</h2>
            </div>
            <div className="flex items-center justify-between gap-3 px-6 py-4">
              <p className="text-sm text-gray-600">
                Delete this stack — detach its apps and keep them, or tear the whole collection down.
              </p>
              <button
                onClick={() => openModal("delete")}
                disabled={busy !== null}
                className="rounded-md border border-red-300 bg-white px-3.5 py-2 text-sm font-medium text-red-700 hover:bg-red-50 disabled:opacity-50"
              >
                Delete stack…
              </button>
            </div>
          </div>
        </div>
      )}

      {/* Promote modal */}
      {modal === "promote" && (
        <Modal
          title="Promote stack"
          description="Promote every member app to the chosen environment. Each app keeps its own pipeline."
          onClose={closeModal}
          closable={busy === null}
        >
          {!results && (
            <select
              value={promoteEnv}
              onChange={(e) => setPromoteEnv(e.target.value)}
              className="mt-4 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            >
              <option value="">Target environment…</option>
              {envs.map((env) => (
                <option key={env.name} value={env.name}>
                  {env.displayName || env.name}
                </option>
              ))}
            </select>
          )}
          {results && <ResultRows results={results} />}
          <div className="mt-5 flex justify-end gap-2">
            <button onClick={closeModal} disabled={busy !== null} className={btnSecondary}>
              {results ? "Done" : "Cancel"}
            </button>
            {!results && (
              <button
                onClick={() => runModalBatch("promote", () => promoteStack(project, stackName, promoteEnv))}
                disabled={!promoteEnv || busy !== null}
                className={btnPrimary}
              >
                {busy === "promote" ? "Promoting…" : "Promote all"}
              </button>
            )}
          </div>
        </Modal>
      )}

      {/* Suspend / resume modal */}
      {modal === "suspend" && (
        <Modal
          title="Suspend / resume stack"
          description="Scale the stack's workloads down (or back up) for an environment. The env stays published — no data loss — and each member's chart honors the platform's suspend key."
          onClose={closeModal}
          closable={busy === null}
        >
          {!results && (
            <select
              value={suspendEnv}
              onChange={(e) => setSuspendEnv(e.target.value)}
              className="mt-4 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
            >
              <option value="">Environment…</option>
              {envs.map((env) => (
                <option key={env.name} value={env.name}>
                  {env.displayName || env.name}
                </option>
              ))}
            </select>
          )}
          {results && <ResultRows results={results} />}
          <div className="mt-5 flex justify-end gap-2">
            <button onClick={closeModal} disabled={busy !== null} className={btnSecondary}>
              {results ? "Done" : "Cancel"}
            </button>
            {!results && (
              <>
                <button
                  onClick={() => doSuspendResume(true)}
                  disabled={!suspendEnv || busy !== null}
                  className={btnSecondary}
                >
                  {busy === "resume" ? "Resuming…" : "Resume"}
                </button>
                <button
                  onClick={() => doSuspendResume(false)}
                  disabled={!suspendEnv || busy !== null}
                  className={btnPrimary}
                >
                  {busy === "suspend" ? "Suspending…" : "Suspend"}
                </button>
              </>
            )}
          </div>
        </Modal>
      )}

      {/* Preview modal */}
      {modal === "preview" && (
        <Modal
          title="Preview the stack"
          description="Bring up a preview of every member co-located in one shared namespace."
          onClose={closeModal}
          closable={busy === null}
        >
          {!results && (
            <>
              <input
                value={previewName}
                onChange={(e) => setPreviewName(e.target.value)}
                placeholder="preview name"
                className="mt-4 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
              />
              <p className="mt-2 text-xs text-gray-500">
                Namespace:{" "}
                <code className="font-mono">
                  {project}-{stack.name}-preview-{previewName || "<name>"}
                </code>
              </p>
            </>
          )}
          {results && <ResultRows results={results} />}
          <div className="mt-5 flex justify-end gap-2">
            <button onClick={closeModal} disabled={busy !== null} className={btnSecondary}>
              {results ? "Done" : "Cancel"}
            </button>
            {!results && (
              <button
                onClick={() => runModalBatch("preview", () => createStackPreview(project, stackName, { name: previewName }))}
                disabled={!previewName || busy !== null}
                className={btnPrimary}
              >
                {busy === "preview" ? "Creating…" : "Create preview"}
              </button>
            )}
          </div>
        </Modal>
      )}

      {/* Clone modal */}
      {modal === "clone" && (
        <Modal
          title="Clone this stack"
          description="Duplicate the collection with variations (e.g. livekit-cloud vs self-hosted). The source stays intact."
          onClose={closeModal}
          closable={busy === null}
        >
          <input
            value={cloneName}
            onChange={(e) => setCloneName(e.target.value)}
            placeholder="new stack name"
            className="mt-4 w-full rounded-md border border-gray-300 px-3 py-2 text-sm"
          />
          <p className="mt-2 text-xs text-gray-500">
            Members are copied as <code className="font-mono">{cloneName || "<new-stack>"}-&lt;app&gt;</code>.
            App-level secret values are not migrated — re-enter them under the new apps.
          </p>
          <div className="mt-5 flex justify-end gap-2">
            <button onClick={closeModal} disabled={busy !== null} className={btnSecondary}>
              Cancel
            </button>
            <button onClick={doClone} disabled={!cloneName || busy !== null} className={btnPrimary}>
              {busy === "clone" ? "Cloning…" : "Clone"}
            </button>
          </div>
        </Modal>
      )}

      {/* Delete modal */}
      {modal === "delete" && (
        <Modal title={`Delete stack "${stackName}"`} onClose={closeModal} closable={busy === null}>
          <div className="mt-4 space-y-2">
            <button
              onClick={() => doDelete(false)}
              disabled={busy !== null}
              className="w-full rounded-md border border-gray-300 bg-white px-4 py-3 text-left text-sm hover:bg-gray-50 disabled:opacity-50"
            >
              <span className="font-medium text-gray-900">Detach apps & delete stack</span>
              <span className="block text-xs text-gray-500">
                Member apps stay in the project, detached from the stack.
              </span>
            </button>
            <button
              onClick={() => doDelete(true)}
              disabled={busy !== null}
              className="w-full rounded-md border border-red-300 bg-white px-4 py-3 text-left text-sm hover:bg-red-50 disabled:opacity-50"
            >
              <span className="font-medium text-red-700">
                Delete stack AND all {memberApps.length} app{memberApps.length === 1 ? "" : "s"}
              </span>
              <span className="block text-xs text-gray-500">
                Tears down every member app and reclaims the stack namespaces. Cannot be undone.
              </span>
            </button>
          </div>
          <div className="mt-5 flex justify-end">
            <button onClick={closeModal} disabled={busy !== null} className={btnSecondary}>
              Cancel
            </button>
          </div>
        </Modal>
      )}
    </div>
  );
}
