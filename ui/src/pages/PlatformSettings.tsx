import { useEffect, useState } from "react";
import { toast } from "sonner";

import {
  fetchCredentialHealth,
  type CredentialHealthResponse,
  type CredentialStatus as CredStatus,
} from "../lib/credentials";
import {
  fetchPrerequisites,
  type PrerequisitesResponse,
  type ComponentStatus,
} from "../lib/prerequisites";

function StatusBadge({ installed, healthy }: ComponentStatus) {
  if (!installed) {
    return (
      <span className="inline-flex items-center rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-600">
        Not installed
      </span>
    );
  }
  if (!healthy) {
    return (
      <span className="inline-flex items-center rounded-full bg-yellow-100 px-2.5 py-0.5 text-xs font-medium text-yellow-800">
        Unhealthy
      </span>
    );
  }
  return (
    <span className="inline-flex items-center rounded-full bg-green-100 px-2.5 py-0.5 text-xs font-medium text-green-800">
      Healthy
    </span>
  );
}

function ComponentRow({
  label,
  status,
}: {
  label: string;
  status: ComponentStatus;
}) {
  return (
    <div className="flex items-center justify-between rounded-lg border border-gray-200 bg-white px-5 py-4">
      <div>
        <p className="text-sm font-medium text-gray-900">{label}</p>
        {status.installed && (
          <p className="mt-0.5 text-xs text-gray-500">
            {status.namespace && <span>{status.namespace}</span>}
            {status.version && <span className="ml-2">{status.version}</span>}
          </p>
        )}
      </div>
      <StatusBadge {...status} />
    </div>
  );
}

export function PlatformSettings() {
  const [data, setData] = useState<PrerequisitesResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const res = await fetchPrerequisites();
        if (!cancelled) setData(res);
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

  if (loading) {
    return (
      <div className="space-y-4">
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        {[1, 2, 3].map((n) => (
          <div key={n} className="h-16 animate-pulse rounded-lg bg-gray-50" />
        ))}
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">Failed to load prerequisites: {error}</p>
      </div>
    );
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Platform</h1>
        <p className="mt-1 text-sm text-gray-500">
          Cluster prerequisites and platform component status.
        </p>
      </div>

      {data && (
        <>
          <section className="space-y-3">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-gray-500">
              Components
            </h2>
            <ComponentRow label="ArgoCD" status={data.argocd} />
            <ComponentRow label="Ingress Controller" status={data.ingressController} />
            <ComponentRow label="External Secrets Operator" status={data.eso} />
          </section>

          <section className="space-y-3">
            <h2 className="text-sm font-semibold uppercase tracking-wider text-gray-500">
              Cluster
            </h2>
            <div className="rounded-lg border border-gray-200 bg-white px-5 py-4">
              <p className="text-sm text-gray-700">
                <span className="font-medium">API Server:</span>{" "}
                <code className="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-mono">
                  {data.inCluster.apiServer}
                </code>
              </p>
              {data.inCluster.clusterName && (
                <p className="mt-1 text-sm text-gray-500">
                  Cluster: {data.inCluster.clusterName}
                </p>
              )}
            </div>
          </section>
        </>
      )}

      <CredentialHealth />

      <ExportConfig />
    </div>
  );
}

const credStatusColors: Record<string, { bg: string; text: string; label: string }> = {
  healthy: { bg: "bg-green-100", text: "text-green-800", label: "Healthy" },
  warning: { bg: "bg-yellow-100", text: "text-yellow-800", label: "Warning" },
  expired: { bg: "bg-red-100", text: "text-red-800", label: "Expired" },
  missing: { bg: "bg-red-100", text: "text-red-800", label: "Missing" },
  not_configured: { bg: "bg-gray-100", text: "text-gray-600", label: "Not configured" },
};

const credNameLabels: Record<string, string> = {
  gitops: "GitOps Repository",
  registry: "Container Registry",
  "1password": "1Password",
};

function CredentialBadge({ status }: { status: string }) {
  const s = credStatusColors[status] ?? credStatusColors.missing;
  return (
    <span
      className={`inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium ${s.bg} ${s.text}`}
    >
      {s.label}
    </span>
  );
}

function CredentialRow({ cred }: { cred: CredStatus }) {
  return (
    <div className="flex items-center justify-between rounded-lg border border-gray-200 bg-white px-5 py-4">
      <div>
        <p className="text-sm font-medium text-gray-900">
          {credNameLabels[cred.name] ?? cred.name}
        </p>
        <div className="mt-0.5 space-x-3 text-xs text-gray-500">
          {cred.secretRef && <span>Secret: {cred.secretRef}</span>}
          {cred.daysUntilExpiry !== undefined && cred.daysUntilExpiry !== null && (
            <span>
              {cred.daysUntilExpiry < 0
                ? `Expired ${Math.abs(cred.daysUntilExpiry)} days ago`
                : `Expires in ${cred.daysUntilExpiry} days`}
            </span>
          )}
          {cred.message && !cred.secretRef && <span>{cred.message}</span>}
        </div>
      </div>
      <CredentialBadge status={cred.status} />
    </div>
  );
}

function CredentialHealth() {
  const [data, setData] = useState<CredentialHealthResponse | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    async function load() {
      try {
        const res = await fetchCredentialHealth();
        if (!cancelled) setData(res);
      } catch {
        // non-fatal
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    return () => { cancelled = true; };
  }, []);

  if (loading) {
    return (
      <section className="space-y-3">
        <div className="h-5 w-40 animate-pulse rounded bg-gray-100" />
        {[1, 2, 3].map((n) => (
          <div key={n} className="h-14 animate-pulse rounded-lg bg-gray-50" />
        ))}
      </section>
    );
  }

  if (!data) return null;

  const overallColor = credStatusColors[data.overallStatus] ?? credStatusColors.missing;

  return (
    <section className="space-y-3">
      <div className="flex items-center gap-3">
        <h2 className="text-sm font-semibold uppercase tracking-wider text-gray-500">
          Credential Health
        </h2>
        <span
          className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${overallColor.bg} ${overallColor.text}`}
        >
          {overallColor.label}
        </span>
      </div>
      {data.credentials.map((c) => (
        <CredentialRow key={c.name} cred={c} />
      ))}
    </section>
  );
}

function ExportConfig() {
  const [exporting, setExporting] = useState(false);

  async function handleExport(format: "json" | "yaml") {
    setExporting(true);
    try {
      const suffix = format === "yaml" ? "?format=yaml" : "";
      const res = await fetch(`/api/v1/org/export${suffix}`, {
        credentials: "include",
      });
      if (!res.ok) {
        throw new Error(`Export failed: ${res.status}`);
      }
      const blob = await res.blob();
      const filename = format === "yaml" ? "values.yaml" : "values.json";
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
      toast.success(`Configuration exported as ${filename}`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Export failed");
    } finally {
      setExporting(false);
    }
  }

  return (
    <section className="space-y-3">
      <h2 className="text-sm font-semibold uppercase tracking-wider text-gray-500">
        Export Configuration
      </h2>
      <div className="rounded-lg border border-gray-200 bg-white px-5 py-4">
        <p className="text-sm text-gray-600">
          Download the current platform configuration as a Helm{" "}
          <code className="rounded bg-gray-100 px-1 py-0.5 text-xs font-mono">
            values.yaml
          </code>{" "}
          file. Secret values are never included — only secret reference names.
        </p>
        <div className="mt-4 flex gap-3">
          <button
            onClick={() => handleExport("yaml")}
            disabled={exporting}
            className="inline-flex items-center rounded-md bg-indigo-600 px-3.5 py-2 text-sm font-semibold text-white shadow-sm hover:bg-indigo-500 disabled:opacity-50"
          >
            {exporting ? "Exporting..." : "Download values.yaml"}
          </button>
          <button
            onClick={() => handleExport("json")}
            disabled={exporting}
            className="inline-flex items-center rounded-md bg-white px-3.5 py-2 text-sm font-semibold text-gray-900 shadow-sm ring-1 ring-inset ring-gray-300 hover:bg-gray-50 disabled:opacity-50"
          >
            Download JSON
          </button>
        </div>
      </div>
    </section>
  );
}
