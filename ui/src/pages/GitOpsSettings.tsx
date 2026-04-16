import { useEffect, useState } from "react";
import { toast } from "sonner";

import {
  fetchGitOpsConfig,
  updateGitOpsConfig,
  testGitOpsConnection,
  type GitOpsConfig,
} from "../lib/gitops";

const PROVIDERS = [
  { value: "github", label: "GitHub" },
  { value: "gitlab", label: "GitLab" },
  { value: "bitbucket", label: "Bitbucket" },
  { value: "gitea", label: "Gitea" },
  { value: "generic", label: "Generic Git" },
];

function Field({
  label,
  children,
  help,
}: {
  label: string;
  children: React.ReactNode;
  help?: string;
}) {
  return (
    <label className="block">
      <span className="text-sm font-medium text-gray-700">{label}</span>
      <div className="mt-1">{children}</div>
      {help && <p className="mt-1 text-xs text-gray-400">{help}</p>}
    </label>
  );
}

const inputClass =
  "block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-blue-500 focus:ring-1 focus:ring-blue-500";
const selectClass =
  "block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-blue-500 focus:ring-1 focus:ring-blue-500 bg-white";

export function GitOpsSettings() {
  const [config, setConfig] = useState<GitOpsConfig | null>(null);
  const [configured, setConfigured] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const res = await fetchGitOpsConfig();
        if (cancelled) return;
        setConfigured(res.configured);
        setConfig(
          res.config ?? {
            provider: "",
            repoURL: "",
            branch: "main",
            initializeRepo: true,
            initialized: false,
          },
        );
      } catch (err) {
        if (!cancelled)
          setError(err instanceof Error ? err.message : "Failed to load");
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    return () => { cancelled = true; };
  }, []);

  const handleSave = async () => {
    if (!config) return;
    setSaving(true);
    try {
      const res = await updateGitOpsConfig(config);
      setConfigured(res.configured);
      toast.success("GitOps configuration saved");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Save failed");
    } finally {
      setSaving(false);
    }
  };

  const handleTest = async () => {
    if (!config?.repoURL) return;
    setTesting(true);
    try {
      const res = await testGitOpsConnection({ repoURL: config.repoURL });
      if (res.success) {
        toast.success(`Connection successful (${res.durationMs}ms)`);
      } else {
        toast.error(`Connection failed: ${res.message}`);
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Test failed");
    } finally {
      setTesting(false);
    }
  };

  const update = (partial: Partial<GitOpsConfig>) => {
    setConfig((prev) => (prev ? { ...prev, ...partial } : prev));
  };

  if (loading) {
    return (
      <div className="space-y-4">
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        <div className="h-64 animate-pulse rounded-lg bg-gray-50" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">Failed to load config: {error}</p>
      </div>
    );
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">GitOps Repository</h1>
        <p className="mt-1 text-sm text-gray-500">
          Configure the Git repository used for GitOps manifests.
          {configured && (
            <span className="ml-2 inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800">
              Connected
            </span>
          )}
        </p>
      </div>

      {config && (
        <div className="max-w-xl space-y-6 rounded-lg border border-gray-200 bg-white p-6">
          <Field label="Provider">
            <select
              className={selectClass}
              value={config.provider}
              onChange={(e) => update({ provider: e.target.value })}
            >
              <option value="">Select provider...</option>
              {PROVIDERS.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </select>
          </Field>

          <Field label="Repository URL" help="HTTPS or SSH clone URL">
            <input
              className={inputClass}
              value={config.repoURL}
              onChange={(e) => update({ repoURL: e.target.value })}
              placeholder="https://github.com/org/gitops-repo.git"
            />
          </Field>

          <Field label="Branch">
            <input
              className={inputClass}
              value={config.branch}
              onChange={(e) => update({ branch: e.target.value })}
              placeholder="main"
            />
          </Field>

          <Field label="Sub-path" help="Optional directory within the repo for gitops content">
            <input
              className={inputClass}
              value={config.subPath ?? ""}
              onChange={(e) => update({ subPath: e.target.value || undefined })}
              placeholder="gitops/"
            />
          </Field>

          <Field
            label="Auth Secret Reference"
            help="Name of a K8s Secret in suparship-system containing credentials"
          >
            <input
              className={inputClass}
              value={config.authSecretRef ?? ""}
              onChange={(e) =>
                update({ authSecretRef: e.target.value || undefined })
              }
              placeholder="suparship-gitops-credentials"
            />
          </Field>

          {config.provider === "github" && (
            <div className="space-y-4 rounded-md border border-blue-100 bg-blue-50 p-4">
              <p className="text-xs font-semibold uppercase tracking-wider text-blue-600">
                GitHub App (optional)
              </p>
              <Field label="App ID">
                <input
                  className={inputClass}
                  value={config.github?.appId ?? ""}
                  onChange={(e) =>
                    update({
                      github: {
                        ...config.github,
                        appId: e.target.value || undefined,
                      },
                    })
                  }
                />
              </Field>
              <Field label="Installation ID">
                <input
                  className={inputClass}
                  value={config.github?.installationId ?? ""}
                  onChange={(e) =>
                    update({
                      github: {
                        ...config.github,
                        installationId: e.target.value || undefined,
                      },
                    })
                  }
                />
              </Field>
            </div>
          )}

          <Field label="ArgoCD Repo URL" help="If ArgoCD uses a different URL (e.g. in-cluster)">
            <input
              className={inputClass}
              value={config.argoCDRepoURL ?? ""}
              onChange={(e) =>
                update({ argoCDRepoURL: e.target.value || undefined })
              }
              placeholder="http://gitea-http.gitea.svc:3000/org/repo"
            />
          </Field>

          <div className="flex items-center gap-3 pt-2">
            <button
              onClick={handleSave}
              disabled={saving || !config.repoURL}
              className="rounded-md bg-blue-600 px-4 py-2 text-sm font-medium text-white shadow-sm hover:bg-blue-700 disabled:opacity-50"
            >
              {saving ? "Saving..." : "Save"}
            </button>
            <button
              onClick={handleTest}
              disabled={testing || !config.repoURL}
              className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 shadow-sm hover:bg-gray-50 disabled:opacity-50"
            >
              {testing ? "Testing..." : "Test Connection"}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
