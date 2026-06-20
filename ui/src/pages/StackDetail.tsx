import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";

import { ApiError } from "../lib/api";
import { listApps } from "../lib/apps";
import { listProjectEnvironments } from "../lib/projects";
import type { ProjectEnvironment } from "../lib/projects";
import {
  cloneStack,
  createStackPreview,
  deleteStack,
  getStack,
  promoteStack,
  setAppStack,
  syncStack,
  updateStack,
} from "../lib/stacks";
import type { Stack, StackBatchResponse } from "../lib/stacks";
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

export function StackDetail() {
  const { project, stack: stackName } = useParams<{ project: string; stack: string }>();
  const navigate = useNavigate();
  const [stack, setStack] = useState<Stack | null>(null);
  const [allApps, setAllApps] = useState<{ name: string; stack?: string }[]>([]);
  const [envs, setEnvs] = useState<ProjectEnvironment[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [addApp, setAddApp] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [promoteEnv, setPromoteEnv] = useState("");
  const [previewName, setPreviewName] = useState("");
  const [cloneName, setCloneName] = useState("");

  const reload = useCallback(async () => {
    if (!project || !stackName) return;
    try {
      const [s, apps, envList] = await Promise.all([
        getStack(project, stackName),
        listApps(project),
        listProjectEnvironments(project),
      ]);
      setStack(s);
      setAllApps(apps.apps.map((a) => ({ name: a.name })));
      setEnvs(envList);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load stack");
    }
  }, [project, stackName]);

  useEffect(() => {
    reload();
  }, [reload]);

  if (!project || !stackName) return null;
  if (error) return <div className="p-6 text-sm text-red-600">{error}</div>;
  if (!stack) return <div className="p-6 text-sm text-gray-400">Loading…</div>;

  const memberApps = stack.apps ?? [];
  const members = new Set(memberApps);
  const addable = allApps.filter((a) => !members.has(a.name)).map((a) => a.name);

  async function move(app: string, toStack: string) {
    if (!project) return;
    try {
      await setAppStack(project, app, toStack);
      toast.success(toStack ? `Added ${app} to ${stackName}` : `Removed ${app} from ${stackName}`);
      setAddApp("");
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to update membership");
    }
  }

  async function onDelete(deleteApps: boolean) {
    if (!project) return;
    const msg = deleteApps
      ? `Delete stack "${stackName}" AND all its member apps? This tears down every app in the collection — this cannot be undone.`
      : `Delete stack "${stackName}"? Member apps stay in the project (detached from the stack).`;
    if (!confirm(msg)) return;
    try {
      await deleteStack(project, stackName!, deleteApps);
      toast.success(deleteApps ? `Stack ${stackName} and its apps deleted` : `Stack ${stackName} deleted`);
      navigate(`/projects/${encodeURIComponent(project)}`);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to delete stack");
    }
  }

  // summarize toasts the per-app outcome of a batch op.
  function summarize(action: string, res: StackBatchResponse) {
    const failed = res.results.filter((r) => !r.ok);
    if (failed.length === 0) {
      toast.success(`${action}: ${res.results.length} app(s) succeeded`);
    } else {
      toast.error(
        `${action}: ${failed.length}/${res.results.length} failed — ${failed
          .map((r) => `${r.app}: ${r.error}`)
          .join("; ")}`,
      );
    }
  }

  async function runBatch(action: string, fn: () => Promise<StackBatchResponse>) {
    setBusy(action);
    try {
      summarize(action, await fn());
      await reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : `Failed to ${action}`);
    } finally {
      setBusy(null);
    }
  }

  async function onClone() {
    if (!project || !cloneName) return;
    setBusy("clone");
    try {
      const res = await cloneStack(project, stackName!, { newName: cloneName });
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
      navigate(`/projects/${encodeURIComponent(project)}/stacks/${encodeURIComponent(cloneName)}`);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to clone stack");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <div className="text-sm text-gray-500">
          <Link to={`/projects/${encodeURIComponent(project)}`} className="hover:text-gray-700">
            {project}
          </Link>{" "}
          / Stacks
        </div>
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-semibold text-gray-900">
              {stack.displayName || stack.name}
            </h1>
            {stack.description && <p className="mt-1 text-sm text-gray-500">{stack.description}</p>}
          </div>
          <button
            onClick={() => onDelete(false)}
            className="rounded-md border border-red-200 bg-white px-3 py-2 text-sm font-medium text-red-600 hover:bg-red-50"
          >
            Delete stack
          </button>
        </div>
      </div>

      {/* Shared namespace toggle */}
      <div className="rounded-xl border border-gray-200 bg-white px-6 py-4">
        <label className="flex items-start gap-3">
          <input
            type="checkbox"
            checked={stack.sharedNamespace ?? false}
            onChange={async (e) => {
              if (!project) return;
              try {
                await updateStack(project, stackName!, { sharedNamespace: e.target.checked });
                toast.success(
                  e.target.checked
                    ? "Members will co-locate in one namespace"
                    : "Members will use their own namespaces",
                );
                await reload();
              } catch (err) {
                toast.error(err instanceof ApiError ? err.message : "Failed to update stack");
              }
            }}
            className="mt-0.5"
          />
          <span className="text-sm">
            <span className="font-medium text-gray-900">Shared namespace</span>
            <span className="block text-xs text-gray-500">
              Co-locate member apps in one{" "}
              <code className="font-mono">{project}-{stack.name}-&lt;env&gt;</code> namespace
              so they reach each other by in-cluster DNS (e.g.{" "}
              <code className="font-mono">web → http://agent-server-web:8080</code>). Toggling
              relocates the apps on the next sync.
            </span>
          </span>
        </label>
      </div>

      {/* Batch lifecycle */}
      <div className="rounded-xl border border-gray-200 bg-white">
        <div className="border-b border-gray-100 px-6 py-4">
          <h2 className="text-base font-medium text-gray-900">Batch actions</h2>
          <p className="mt-0.5 text-sm text-gray-500">
            Act on every app in this stack at once. Each app keeps its own ArgoCD/Kargo pipeline — these
            just fan out over the members.
          </p>
        </div>
        <div className="space-y-4 p-6">
          {/* Sync all */}
          <div className="flex items-center justify-between gap-3">
            <div className="text-sm">
              <span className="font-medium text-gray-900">Sync all</span>
              <span className="block text-xs text-gray-500">Republish every member app's GitOps manifests.</span>
            </div>
            <button
              onClick={() => runBatch("sync", () => syncStack(project, stackName))}
              disabled={busy !== null || memberApps.length === 0}
              className="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
            >
              {busy === "sync" ? "Syncing…" : "Sync all"}
            </button>
          </div>

          {/* Promote all */}
          <div className="flex items-center justify-between gap-3">
            <div className="text-sm">
              <span className="font-medium text-gray-900">Promote all</span>
              <span className="block text-xs text-gray-500">Promote every member to the chosen environment.</span>
            </div>
            <div className="flex items-center gap-2">
              <select
                value={promoteEnv}
                onChange={(e) => setPromoteEnv(e.target.value)}
                className="rounded-md border border-gray-300 px-3 py-1.5 text-sm"
              >
                <option value="">Target env…</option>
                {envs.map((env) => (
                  <option key={env.name} value={env.name}>{env.displayName || env.name}</option>
                ))}
              </select>
              <button
                onClick={() => promoteEnv && runBatch("promote", () => promoteStack(project, stackName, promoteEnv))}
                disabled={busy !== null || !promoteEnv || memberApps.length === 0}
                className="rounded-md bg-gray-900 px-3 py-1.5 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
              >
                {busy === "promote" ? "Promoting…" : "Promote all"}
              </button>
            </div>
          </div>

          {/* Preview the whole stack */}
          <div className="flex items-center justify-between gap-3">
            <div className="text-sm">
              <span className="font-medium text-gray-900">Preview the stack</span>
              <span className="block text-xs text-gray-500">
                Bring up a preview of every member co-located in one{" "}
                <code className="font-mono">{project}-{stack.name}-preview-&lt;name&gt;</code> namespace.
              </span>
            </div>
            <div className="flex items-center gap-2">
              <input
                value={previewName}
                onChange={(e) => setPreviewName(e.target.value)}
                placeholder="preview name"
                className="rounded-md border border-gray-300 px-3 py-1.5 text-sm"
              />
              <button
                onClick={() =>
                  previewName &&
                  runBatch("preview", () => createStackPreview(project, stackName, previewName)).then(() => setPreviewName(""))
                }
                disabled={busy !== null || !previewName || memberApps.length === 0}
                className="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
              >
                {busy === "preview" ? "Creating…" : "Preview"}
              </button>
            </div>
          </div>

          {/* Clone the stack */}
          <div className="flex items-center justify-between gap-3">
            <div className="text-sm">
              <span className="font-medium text-gray-900">Clone this stack</span>
              <span className="block text-xs text-gray-500">
                Duplicate the collection with variations (e.g. livekit-cloud vs self-hosted). Members are
                copied as <code className="font-mono">&lt;new-stack&gt;-&lt;app&gt;</code>; the source stays
                intact. App-level secret values aren't migrated — re-enter them under the new apps.
              </span>
            </div>
            <div className="flex items-center gap-2">
              <input
                value={cloneName}
                onChange={(e) => setCloneName(e.target.value)}
                placeholder="new stack name"
                className="rounded-md border border-gray-300 px-3 py-1.5 text-sm"
              />
              <button
                onClick={onClone}
                disabled={busy !== null || !cloneName}
                className="rounded-md border border-gray-300 bg-white px-3 py-1.5 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
              >
                {busy === "clone" ? "Cloning…" : "Clone"}
              </button>
            </div>
          </div>

          {/* Destroy everything */}
          <div className="flex items-center justify-between gap-3 border-t border-gray-100 pt-4">
            <div className="text-sm">
              <span className="font-medium text-red-700">Delete stack + all apps</span>
              <span className="block text-xs text-gray-500">Tear down every member app and reclaim the stack namespaces.</span>
            </div>
            <button
              onClick={() => onDelete(true)}
              disabled={busy !== null}
              className="rounded-md border border-red-300 bg-white px-3 py-1.5 text-sm font-medium text-red-700 hover:bg-red-50 disabled:opacity-50"
            >
              Delete everything
            </button>
          </div>
        </div>
      </div>

      {/* Members */}
      <div className="rounded-xl border border-gray-200 bg-white">
        <div className="border-b border-gray-100 px-6 py-4">
          <h2 className="text-base font-medium text-gray-900">Apps in this stack</h2>
          <p className="mt-0.5 text-sm text-gray-500">
            Tightly-coupled apps grouped here share the stack's overrides{stack.sharedNamespace ? " and namespace" : ""}.
          </p>
        </div>
        <div className="divide-y divide-gray-50">
          {memberApps.length === 0 && (
            <p className="px-6 py-4 text-sm text-gray-400">No apps yet. Add one below.</p>
          )}
          {memberApps.map((a) => (
            <div key={a} className="flex items-center justify-between px-6 py-3">
              <Link
                to={`/projects/${encodeURIComponent(project)}/apps/${encodeURIComponent(a)}`}
                className="font-mono text-sm text-gray-900 hover:text-indigo-600"
              >
                {a}
              </Link>
              <button onClick={() => move(a, "")} className="text-xs font-medium text-gray-500 hover:text-red-600">
                Remove
              </button>
            </div>
          ))}
        </div>
        {addable.length > 0 && (
          <div className="flex items-center gap-2 border-t border-gray-100 px-6 py-3">
            <select
              value={addApp}
              onChange={(e) => setAddApp(e.target.value)}
              className="rounded-md border border-gray-300 px-3 py-1.5 text-sm"
            >
              <option value="">Add an app…</option>
              {addable.map((a) => (
                <option key={a} value={a}>{a}</option>
              ))}
            </select>
            <button
              onClick={() => addApp && move(addApp, stackName!)}
              disabled={!addApp}
              className="rounded-md bg-gray-900 px-3 py-1.5 text-sm font-medium text-white hover:bg-gray-700 disabled:opacity-50"
            >
              Add
            </button>
          </div>
        )}
      </div>

      {/* Shared env vars */}
      <EnvConfigEditor
        title="Stack variables"
        description="Applied to every app in this stack. Overrides project defaults; overridden by app-level values."
        fetchFn={async (): Promise<EnvConfig> => (await getStack(project, stackName!)).envConfig ?? {}}
        saveFn={(cfg: EnvConfig) => updateStack(project, stackName!, { envConfig: cfg })}
      />

      {/* Shared secrets */}
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
            fetchFn={() => listStackGlobalSecretKeys(project, stackName!)}
            upsertFn={(e) => upsertStackGlobalSecrets(project, stackName!, e)}
            deleteFn={(k) => deleteStackGlobalSecretKey(project, stackName!, k)}
          />
          {envs.map((env) => (
            <SecretEditor
              key={env.name}
              title={`${env.displayName || env.name} secrets`}
              description={`Stack secrets for the ${env.name} environment.`}
              fetchFn={() => listStackEnvSecretKeys(project, stackName!, env.name)}
              upsertFn={(e) => upsertStackEnvSecrets(project, stackName!, env.name, e)}
              deleteFn={(k) => deleteStackEnvSecretKey(project, stackName!, env.name, k)}
            />
          ))}
        </div>
      </div>
    </div>
  );
}
