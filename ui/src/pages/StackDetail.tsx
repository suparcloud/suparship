import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { toast } from "sonner";

import { ApiError } from "../lib/api";
import { listApps } from "../lib/apps";
import { listProjectEnvironments } from "../lib/projects";
import type { ProjectEnvironment } from "../lib/projects";
import {
  deleteStack,
  getStack,
  setAppStack,
  updateStack,
} from "../lib/stacks";
import type { Stack } from "../lib/stacks";
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

  async function onDelete() {
    if (!project) return;
    if (!confirm(`Delete stack "${stackName}"? Member apps stay in the project (detached from the stack).`)) return;
    try {
      await deleteStack(project, stackName!);
      toast.success(`Stack ${stackName} deleted`);
      navigate(`/projects/${encodeURIComponent(project)}`);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Failed to delete stack");
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
            onClick={onDelete}
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
