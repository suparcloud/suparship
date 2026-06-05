import { useCallback, useEffect, useRef, useState } from "react";

import {
  listClusters,
  registerCluster,
  refreshSealingCert,
  removeCluster,
} from "../lib/clusters";
import type { Cluster } from "../lib/clusters";
import {
  getClusterEnvConfig,
  updateClusterEnvConfig,
} from "../lib/envconfig";
import {
  deleteSharedClusterSecretKey,
  listSharedClusterSecretKeys,
  upsertSharedClusterSecrets,
} from "../lib/secrets";
import { listOrgEnvironments } from "../lib/settings";
import type { OrgEnvironment } from "../lib/settings";
import { EnvConfigEditor } from "../components/EnvConfigEditor";
import { SecretEditor } from "../components/SecretEditor";

// ── Status badge ──────────────────────────────────────────────────────────────

function StatusBadge({ status }: { status?: string }) {
  const colors =
    status === "ready"
      ? "bg-green-50 text-green-700"
      : "bg-gray-100 text-gray-500";
  return (
    <span
      className={`inline-flex items-center gap-1 rounded-full px-2.5 py-0.5 text-xs font-medium ${colors}`}
    >
      <span
        className={`h-1.5 w-1.5 rounded-full ${status === "ready" ? "bg-green-500" : "bg-gray-400"}`}
      />
      {status ?? "unknown"}
    </span>
  );
}

// ── Register modal ────────────────────────────────────────────────────────────

interface RegisterModalProps {
  onClose: () => void;
  onRegistered: () => void;
}

function RegisterModal({ onClose, onRegistered }: RegisterModalProps) {
  const fileRef = useRef<HTMLInputElement>(null);
  const [name, setName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [apiServer, setApiServer] = useState("");
  const [kubeconfigB64, setKubeconfigB64] = useState("");
  const [fileName, setFileName] = useState<string | null>(null);
  const [esoNamespace, setEsoNamespace] = useState("");
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function handleFileChange(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    setFileName(file.name);
    const reader = new FileReader();
    reader.onload = (ev) => {
      const bytes = ev.target?.result as ArrayBuffer;
      const arr = new Uint8Array(bytes);
      let binary = "";
      arr.forEach((b) => (binary += String.fromCharCode(b)));
      setKubeconfigB64(btoa(binary));
    };
    reader.readAsArrayBuffer(file);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (!name || !apiServer || !kubeconfigB64) {
      setError("Name, API server URL, and kubeconfig are required.");
      return;
    }
    setSubmitting(true);
    try {
      await registerCluster({
        name,
        displayName: displayName || undefined,
        apiServer,
        kubeconfig: kubeconfigB64,
        esoNamespace: esoNamespace.trim() || undefined,
      });
      onRegistered();
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Registration failed");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/30 backdrop-blur-sm">
      <div className="w-full max-w-lg rounded-xl bg-white p-8 shadow-xl">
        <h2 className="text-lg font-semibold text-gray-900">Register cluster</h2>
        <p className="mt-1 text-sm text-gray-500">
          Credentials are stored in Kubernetes Secrets and never written to Git.
        </p>

        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
          <div>
            <label className="block text-sm font-medium text-gray-700">
              Name <span className="text-red-500">*</span>
            </label>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="staging-cluster"
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            />
            <p className="mt-1 text-xs text-gray-400">
              DNS label: lowercase letters, digits, and hyphens.
            </p>
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700">
              Display name
            </label>
            <input
              type="text"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Staging"
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700">
              API server URL <span className="text-red-500">*</span>
            </label>
            <input
              type="url"
              value={apiServer}
              onChange={(e) => setApiServer(e.target.value)}
              placeholder="https://10.0.0.1:6443"
              className="mt-1 block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
            />
          </div>

          <div>
            <label className="block text-sm font-medium text-gray-700">
              Kubeconfig <span className="text-red-500">*</span>
            </label>
            <div
              className="mt-1 flex cursor-pointer items-center justify-center rounded-md border-2 border-dashed border-gray-300 px-4 py-6 text-center hover:border-indigo-400"
              onClick={() => fileRef.current?.click()}
            >
              <input
                ref={fileRef}
                type="file"
                accept=".yaml,.yml,.kubeconfig"
                className="hidden"
                onChange={handleFileChange}
              />
              {fileName ? (
                <p className="text-sm text-gray-700">{fileName}</p>
              ) : (
                <p className="text-sm text-gray-400">
                  Click to upload kubeconfig file
                </p>
              )}
            </div>
          </div>

          {/* Advanced options */}
          <div>
            <button
              type="button"
              onClick={() => setShowAdvanced((v) => !v)}
              className="flex items-center gap-1 text-sm text-gray-500 hover:text-gray-700"
            >
              <svg
                className={`h-4 w-4 transition-transform ${showAdvanced ? "rotate-90" : ""}`}
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                strokeWidth={2}
              >
                <path strokeLinecap="round" strokeLinejoin="round" d="M9 5l7 7-7 7" />
              </svg>
              Advanced options
            </button>

            {showAdvanced && (
              <div className="mt-3 space-y-4 rounded-md border border-gray-200 bg-gray-50 p-4">
                <div>
                  <label className="block text-sm font-medium text-gray-700">
                    External Secrets namespace
                  </label>
                  <input
                    type="text"
                    value={esoNamespace}
                    onChange={(e) => setEsoNamespace(e.target.value)}
                    placeholder="external-secrets"
                    className="mt-1 block w-full rounded-md border border-gray-300 bg-white px-3 py-2 text-sm shadow-sm focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500"
                  />
                  <p className="mt-1 text-xs text-gray-400">
                    Namespace where the External Secrets Operator is installed on
                    this cluster. Defaults to{" "}
                    <code className="font-mono">external-secrets</code> when left
                    blank. Set to{" "}
                    <code className="font-mono">external-secrets-system</code> if
                    that is where ESO runs on this cluster.
                  </p>
                </div>
              </div>
            )}
          </div>

          {error && (
            <p className="rounded-md bg-red-50 px-3 py-2 text-sm text-red-700">
              {error}
            </p>
          )}

          <div className="flex justify-end gap-3 pt-2">
            <button
              type="button"
              onClick={onClose}
              className="rounded-md px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={submitting}
              className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
            >
              {submitting ? "Registering…" : "Register cluster"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ── Cluster overrides section (env vars + secrets) ────────────────────────────
//
// Cluster-scope is the platform-engineering escape hatch — values written here
// override the global and env scopes (precedence: cluster > env > global) for
// apps deployed onto this specific cluster. Use sparingly: incident break-glass,
// regional tuning, per-cluster feature kill-switches.
//
// Cluster-override SECRETS are per-(env, cluster): the items live inside each
// env vault (e.g. shared-cluster-eu-1 in suparship-secrets-env-staging), so one
// editor is rendered per environment bound to this cluster. Env VARS remain a
// separate cluster-global ConfigMap axis.

function ClusterOverridesSection({ cluster }: { cluster: Cluster }) {
  const [boundEnvs, setBoundEnvs] = useState<OrgEnvironment[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    listOrgEnvironments()
      .then((resp) => {
        if (cancelled) return;
        const envs = (resp.environments || []).filter(
          (e) =>
            e.activeClusterRef === cluster.name ||
            (e.clusterRefs || []).includes(cluster.name),
        );
        setBoundEnvs(envs);
      })
      .catch(() => {
        if (!cancelled) setBoundEnvs([]);
      });
    return () => {
      cancelled = true;
    };
  }, [cluster.name]);

  const fetchEnv = useCallback(
    () => getClusterEnvConfig(cluster.name),
    [cluster.name],
  );
  const saveEnv = useCallback(
    (cfg: Parameters<typeof updateClusterEnvConfig>[1]) =>
      updateClusterEnvConfig(cluster.name, cfg),
    [cluster.name],
  );

  return (
    <div className="space-y-3 border-t border-gray-100 bg-gray-50 px-6 py-5">
      <div>
        <h3 className="text-sm font-semibold text-gray-900">
          Cluster overrides — escape hatch
        </h3>
        <p className="mt-0.5 text-xs text-gray-500">
          Variables and secrets here override the global and env scopes
          (precedence: cluster &gt; env &gt; global) for apps deployed onto{" "}
          <span className="font-mono">{cluster.name}</span>. Use for incident
          response, regional tuning, or per-cluster kill-switches. Override
          secrets are stored per environment, as items inside that env&apos;s
          vault — other clusters bound to the same environment can technically
          read them (vault-scoped tokens).
        </p>
      </div>
      <EnvConfigEditor
        key={`env-${cluster.name}`}
        title={`Variables for cluster "${cluster.name}"`}
        description="Plain-text env vars applied to every app deployed onto this cluster."
        fetchFn={fetchEnv}
        saveFn={saveEnv}
      />
      {boundEnvs === null ? (
        <div className="h-10 animate-pulse rounded bg-gray-100" />
      ) : boundEnvs.length === 0 ? (
        <p className="text-xs text-gray-400">
          No environment is bound to this cluster yet. Bind it to an
          environment (Settings &gt; Environments) to set per-env cluster
          override secrets.
        </p>
      ) : (
        boundEnvs.map((env) => (
          <SecretEditor
            key={`secrets-${env.name}-${cluster.name}`}
            title={`Shared cluster secrets for "${cluster.name}" in env "${env.name}"`}
            description={`Shared secrets applied to every app of env "${env.name}" deployed onto this cluster (cluster scope, stored in the "${env.name}" env vault). App-level cluster secrets override these.`}
            fetchFn={() => listSharedClusterSecretKeys(env.name, cluster.name)}
            upsertFn={(entries) =>
              upsertSharedClusterSecrets(env.name, cluster.name, entries)
            }
            deleteFn={(key) =>
              deleteSharedClusterSecretKey(env.name, cluster.name, key)
            }
          />
        ))
      )}
    </div>
  );
}

// ── Main page ─────────────────────────────────────────────────────────────────

export function ClusterSettings() {
  const [clusters, setClusters] = useState<Cluster[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [showRegister, setShowRegister] = useState(false);
  const [removingName, setRemovingName] = useState<string | null>(null);
  const [refreshingCert, setRefreshingCert] = useState<string | null>(null);
  const [certMessage, setCertMessage] = useState<Record<string, string>>({});
  const [expandedName, setExpandedName] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const data = await listClusters();
      setClusters(data);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load clusters");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function handleRemove(name: string) {
    if (!confirm(`Remove cluster "${name}"? This cannot be undone.`)) return;
    setRemovingName(name);
    try {
      await removeCluster(name);
      await load();
    } catch (err) {
      alert(err instanceof Error ? err.message : "Failed to remove cluster");
    } finally {
      setRemovingName(null);
    }
  }

  async function handleRefreshCert(name: string) {
    setRefreshingCert(name);
    setCertMessage((prev) => ({ ...prev, [name]: "" }));
    try {
      const res = await refreshSealingCert(name);
      setCertMessage((prev) => ({
        ...prev,
        [name]: res.message ?? "Certificate refreshed successfully.",
      }));
    } catch (err) {
      setCertMessage((prev) => ({
        ...prev,
        [name]: err instanceof Error ? err.message : "Refresh failed",
      }));
    } finally {
      setRefreshingCert(null);
    }
  }

  return (
    <div className="space-y-6">
      {showRegister && (
        <RegisterModal
          onClose={() => setShowRegister(false)}
          onRegistered={load}
        />
      )}

      <div className="flex items-start justify-between">
        <div>
          <h1 className="text-2xl font-semibold text-gray-900">Clusters</h1>
          <p className="mt-1 text-sm text-gray-500">
            Workload clusters that suparShip deploys apps to via ArgoCD.
          </p>
        </div>
        <button
          onClick={() => setShowRegister(true)}
          className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700"
        >
          Register cluster
        </button>
      </div>

      {loading && (
        <div className="space-y-3">
          {[1, 2].map((i) => (
            <div key={i} className="h-16 animate-pulse rounded-lg bg-gray-100" />
          ))}
        </div>
      )}

      {!loading && error && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">
            Failed to load clusters: {error}
          </p>
        </div>
      )}

      {!loading && !error && clusters.length === 0 && (
        <div className="rounded-lg border border-dashed border-gray-300 py-16 text-center">
          <p className="text-sm font-medium text-gray-500">
            No clusters registered yet.
          </p>
          <p className="mt-1 text-sm text-gray-400">
            Register a cluster to start deploying apps to it.
          </p>
          <button
            onClick={() => setShowRegister(true)}
            className="mt-4 rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700"
          >
            Register your first cluster
          </button>
        </div>
      )}

      {!loading && !error && clusters.length > 0 && (
        <div className="rounded-lg border border-gray-200 bg-white">
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
                <th className="px-6 py-3">Name</th>
                <th className="px-6 py-3">API Server</th>
                <th className="px-6 py-3">Status</th>
                <th className="px-6 py-3" />
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {clusters.map((c) => (
                <>
                  <tr key={c.name} className="hover:bg-gray-50">
                    <td className="px-6 py-4">
                      <p className="text-sm font-medium text-gray-900">
                        {c.displayName || c.name}
                      </p>
                      <p className="text-xs text-gray-400">{c.name}</p>
                    </td>
                    <td className="px-6 py-4">
                      <p className="font-mono text-xs text-gray-600">
                        {c.apiServer}
                      </p>
                      {c.esoNamespace && (
                        <p className="mt-0.5 text-xs text-gray-400">
                          ESO:{" "}
                          <span className="font-mono">{c.esoNamespace}</span>
                        </p>
                      )}
                    </td>
                    <td className="px-6 py-4">
                      <StatusBadge status={c.status} />
                    </td>
                    <td className="px-6 py-4 text-right">
                      <div className="flex flex-col items-end gap-1">
                        <div className="flex items-center gap-3">
                          <button
                            onClick={() =>
                              setExpandedName((cur) =>
                                cur === c.name ? null : c.name,
                              )
                            }
                            title="Edit cluster-scope env vars and secrets — these override every other layer for apps deployed on this cluster"
                            className="text-sm text-gray-600 hover:text-gray-900"
                          >
                            {expandedName === c.name
                              ? "Hide overrides"
                              : "Overrides"}
                          </button>
                          <button
                            onClick={() => handleRefreshCert(c.name)}
                            disabled={
                              refreshingCert === c.name ||
                              removingName === c.name
                            }
                            title="Re-fetch the sealed-secrets controller certificate from this cluster and update the cache"
                            className="text-sm text-indigo-600 hover:text-indigo-700 disabled:opacity-50"
                          >
                            {refreshingCert === c.name
                              ? "Refreshing…"
                              : "Refresh cert"}
                          </button>
                          <button
                            onClick={() => handleRemove(c.name)}
                            disabled={removingName === c.name}
                            className="text-sm text-red-600 hover:text-red-700 disabled:opacity-50"
                          >
                            {removingName === c.name ? "Removing…" : "Remove"}
                          </button>
                        </div>
                        {(() => {
                          const msg = certMessage[c.name];
                          if (!msg) return null;
                          const lower = msg.toLowerCase();
                          const isError =
                            lower.includes("fail") || lower.includes("error");
                          return (
                            <p
                              className={`text-xs ${
                                isError ? "text-red-600" : "text-green-600"
                              }`}
                            >
                              {msg}
                            </p>
                          );
                        })()}
                      </div>
                    </td>
                  </tr>
                  {expandedName === c.name && (
                    <tr key={`${c.name}-overrides`}>
                      <td colSpan={4} className="p-0">
                        <ClusterOverridesSection cluster={c} />
                      </td>
                    </tr>
                  )}
                </>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="rounded-lg bg-blue-50 p-4">
        <h3 className="text-sm font-medium text-blue-800">How it works</h3>
        <ul className="mt-2 space-y-1 text-sm text-blue-700">
          <li>
            • Each registered cluster gets a kubeconfig stored securely in
            Kubernetes Secrets (never in Git).
          </li>
          <li>
            • ArgoCD is notified automatically so it can deploy
            ApplicationSets to the cluster.
          </li>
          <li>
            • Assign a cluster to an environment in your project settings.
          </li>
        </ul>
      </div>
    </div>
  );
}
