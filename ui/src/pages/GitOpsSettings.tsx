import { useEffect, useState } from "react";
import { toast } from "sonner";

import {
  fetchGitOpsConfig,
  updateGitOpsConfig,
  testGitOpsConnection,
  type GitOpsConfig,
  type GitOpsCredentials,
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
  optional,
}: {
  label: string;
  children: React.ReactNode;
  help?: string;
  optional?: boolean;
}) {
  return (
    <label className="block">
      <span className="flex items-center gap-2 text-sm font-medium text-gray-700">
        {label}
        {optional && (
          <span className="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-normal text-gray-400">
            optional
          </span>
        )}
      </span>
      <div className="mt-1">{children}</div>
      {help && <p className="mt-1 text-xs text-gray-400">{help}</p>}
    </label>
  );
}

const inputClass =
  "block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-blue-500 focus:ring-1 focus:ring-blue-500";
const selectClass =
  "block w-full rounded-md border border-gray-300 px-3 py-2 text-sm shadow-sm focus:border-blue-500 focus:ring-1 focus:ring-blue-500 bg-white";

function CredentialSavedBadge() {
  return (
    <div className="mb-2 flex items-center gap-1.5 rounded-md border border-green-200 bg-green-50 px-3 py-1.5 text-xs text-green-700">
      <svg className="h-3.5 w-3.5" fill="currentColor" viewBox="0 0 20 20">
        <path
          fillRule="evenodd"
          d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.707-9.293a1 1 0 00-1.414-1.414L9 10.586 7.707 9.293a1 1 0 00-1.414 1.414l2 2a1 1 0 001.414 0l4-4z"
          clipRule="evenodd"
        />
      </svg>
      Credentials saved — leave blank to keep existing
    </div>
  );
}

function CredentialFields({
  provider,
  credentialsSet,
  credentials,
  onUpdate,
}: {
  provider: string;
  credentialsSet: boolean;
  credentials: GitOpsCredentials;
  onUpdate: (c: GitOpsCredentials) => void;
}) {
  const keepPlaceholder = credentialsSet ? "Leave blank to keep existing" : "";

  if (provider === "github" || provider === "gitlab" || provider === "gitea") {
    const tokenLabel =
      provider === "github"
        ? "Personal Access Token"
        : provider === "gitlab"
          ? "Access Token"
          : "Access Token";
    const tokenHelp =
      provider === "github"
        ? "A GitHub PAT with repo read/write permissions"
        : provider === "gitlab"
          ? "A GitLab personal or project access token with write access"
          : "A Gitea personal access token with repo write permissions";

    return (
      <div className="space-y-4 rounded-md border border-gray-100 bg-gray-50 p-4">
        <p className="text-xs font-semibold uppercase tracking-wider text-gray-500">
          Authentication
        </p>
        {credentialsSet && !credentials.token && <CredentialSavedBadge />}
        <Field label={tokenLabel} help={tokenHelp}>
          <input
            type="password"
            className={inputClass}
            value={credentials.token ?? ""}
            onChange={(e) =>
              onUpdate({ ...credentials, token: e.target.value || undefined })
            }
            placeholder={keepPlaceholder || "ghp_xxxxxxxxxxxxxxxxxxxx"}
            autoComplete="new-password"
          />
        </Field>

        {provider === "github" && (
          <div className="space-y-3 rounded border border-blue-100 bg-blue-50 p-3">
            <p className="text-xs text-blue-600">
              Using a GitHub App instead? Provide the App ID and Installation ID
              below. The token field above should contain the private key in PEM
              format.
            </p>
            <Field label="GitHub App ID" optional>
              <input
                className={inputClass}
                value={""}
                onChange={() => {}}
                placeholder="123456"
                disabled
              />
            </Field>
            <p className="text-xs text-gray-400">
              GitHub App support coming soon — use a PAT for now.
            </p>
          </div>
        )}
      </div>
    );
  }

  if (provider === "bitbucket") {
    const hasExisting =
      credentialsSet && !credentials.username && !credentials.password;
    return (
      <div className="space-y-4 rounded-md border border-gray-100 bg-gray-50 p-4">
        <p className="text-xs font-semibold uppercase tracking-wider text-gray-500">
          Authentication
        </p>
        {hasExisting && <CredentialSavedBadge />}
        <Field label="Username" help="Your Bitbucket username or email">
          <input
            className={inputClass}
            value={credentials.username ?? ""}
            onChange={(e) =>
              onUpdate({
                ...credentials,
                username: e.target.value || undefined,
              })
            }
            placeholder={keepPlaceholder || "your-username"}
            autoComplete="username"
          />
        </Field>
        <Field
          label="App Password"
          help="A Bitbucket app password with repository read/write permissions"
        >
          <input
            type="password"
            className={inputClass}
            value={credentials.password ?? ""}
            onChange={(e) =>
              onUpdate({
                ...credentials,
                password: e.target.value || undefined,
              })
            }
            placeholder={keepPlaceholder || "ATBBxxxxxxxxxxxxxxxx"}
            autoComplete="new-password"
          />
        </Field>
      </div>
    );
  }

  // generic
  const hasExisting =
    credentialsSet && !credentials.username && !credentials.password;
  return (
    <div className="space-y-4 rounded-md border border-gray-100 bg-gray-50 p-4">
      <p className="text-xs font-semibold uppercase tracking-wider text-gray-500">
        Authentication
      </p>
      {hasExisting && <CredentialSavedBadge />}
      <Field
        label="Username"
        optional
        help="Leave blank for public repos or SSH key auth"
      >
        <input
          className={inputClass}
          value={credentials.username ?? ""}
          onChange={(e) =>
            onUpdate({ ...credentials, username: e.target.value || undefined })
          }
          placeholder={keepPlaceholder || "git-user"}
          autoComplete="username"
        />
      </Field>
      <Field label="Password" optional help="Password or personal access token">
        <input
          type="password"
          className={inputClass}
          value={credentials.password ?? ""}
          onChange={(e) =>
            onUpdate({ ...credentials, password: e.target.value || undefined })
          }
          placeholder={keepPlaceholder || "••••••••"}
          autoComplete="new-password"
        />
      </Field>
    </div>
  );
}

export function GitOpsSettings() {
  const [config, setConfig] = useState<GitOpsConfig | null>(null);
  const [credentialsSet, setCredentialsSet] = useState(false);
  const [credentials, setCredentials] = useState<GitOpsCredentials>({});
  const [configured, setConfigured] = useState(false);
  const [showAdvanced, setShowAdvanced] = useState(false);
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
        setCredentialsSet(res.credentialsSet);
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
    return () => {
      cancelled = true;
    };
  }, []);

  const handleSave = async () => {
    if (!config) return;
    setSaving(true);
    try {
      const res = await updateGitOpsConfig({ ...config, credentials });
      setConfigured(res.configured);
      setCredentialsSet(res.credentialsSet);
      // Clear plaintext credentials from local state after saving.
      setCredentials({});
      if (res.activationWarning) {
        toast.success("Configuration saved", { description: "GitOps config stored in cluster." });
        toast.warning(`ArgoCD registration: ${res.activationWarning}`, {
          description: "Config is saved. ArgoCD may not be installed yet — it will pick up the repo Secret once available.",
          duration: 8000,
        });
      } else {
        toast.success("GitOps configuration saved and applied");
      }
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
      // Pass current unsaved credentials if the user typed them; otherwise
      // the backend will fall back to stored credentials automatically.
      const res = await testGitOpsConnection({
        repoURL: config.repoURL,
        username: credentials.username,
        password: credentials.token ?? credentials.password,
      });
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
        <h1 className="text-2xl font-semibold text-gray-900">
          GitOps Repository
        </h1>
        <p className="mt-1 text-sm text-gray-500">
          Connect the Git repository where suparShip will store deployment
          manifests.
          {configured && (
            <span className="ml-2 inline-flex items-center rounded-full bg-green-100 px-2 py-0.5 text-xs font-medium text-green-800">
              Connected
            </span>
          )}
        </p>
      </div>

      {config && (
        <div className="max-w-xl space-y-6 rounded-lg border border-gray-200 bg-white p-6">
          {/* Provider */}
          <Field label="Git Provider">
            <select
              className={selectClass}
              value={config.provider}
              onChange={(e) => {
                update({ provider: e.target.value });
                setCredentials({});
              }}
            >
              <option value="">Select a provider...</option>
              {PROVIDERS.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </select>
          </Field>

          {/* Repository URL */}
          <Field label="Repository URL" help="HTTPS clone URL of your GitOps repository">
            <input
              className={inputClass}
              value={config.repoURL}
              onChange={(e) => update({ repoURL: e.target.value })}
              placeholder="https://github.com/my-org/gitops-manifests.git"
            />
          </Field>

          {/* Branch */}
          <Field label="Branch" help="Branch where manifests will be committed">
            <input
              className={inputClass}
              value={config.branch}
              onChange={(e) => update({ branch: e.target.value })}
              placeholder="main"
            />
          </Field>

          {/* Sub-path */}
          <Field
            label="Folder"
            optional
            help="Sub-directory within the repo to use for manifests (e.g. gitops/)"
          >
            <input
              className={inputClass}
              value={config.subPath ?? ""}
              onChange={(e) =>
                update({ subPath: e.target.value || undefined })
              }
              placeholder="gitops/"
            />
          </Field>

          {/* Credentials — shown only when a provider is selected */}
          {config.provider && (
            <CredentialFields
              provider={config.provider}
              credentialsSet={credentialsSet}
              credentials={credentials}
              onUpdate={setCredentials}
            />
          )}

          {/* Token expiry — optional quality-of-life warning field */}
          <Field
            label="Token Expiry"
            optional
            help="When your access token expires — suparShip will warn you before it does"
          >
            <input
              type="date"
              className={inputClass}
              value={
                config.credentialExpiresAt
                  ? config.credentialExpiresAt.split("T")[0]
                  : ""
              }
              onChange={(e) => {
                const val = e.target.value;
                update({
                  credentialExpiresAt: val
                    ? new Date(val + "T23:59:59Z").toISOString()
                    : undefined,
                });
              }}
            />
          </Field>

          {/* Advanced settings */}
          <div>
            <button
              type="button"
              className="flex items-center gap-1 text-xs text-gray-400 hover:text-gray-600"
              onClick={() => setShowAdvanced((v) => !v)}
            >
              <svg
                className={`h-3.5 w-3.5 transition-transform ${showAdvanced ? "rotate-90" : ""}`}
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M9 5l7 7-7 7"
                />
              </svg>
              Advanced settings
            </button>

            {showAdvanced && (
              <div className="mt-4 space-y-4 rounded-md border border-gray-100 bg-gray-50 p-4">
                <Field
                  label="ArgoCD Repo URL"
                  optional
                  help="Only needed if ArgoCD uses a different in-cluster URL to reach this repo"
                >
                  <input
                    className={inputClass}
                    value={config.argoCDRepoURL ?? ""}
                    onChange={(e) =>
                      update({ argoCDRepoURL: e.target.value || undefined })
                    }
                    placeholder="http://gitea-http.gitea.svc:3000/org/repo"
                  />
                </Field>
              </div>
            )}
          </div>

          {/* Actions */}
          <div className="flex items-center gap-3 border-t border-gray-100 pt-4">
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
