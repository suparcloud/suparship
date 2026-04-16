import { useEffect, useState } from "react";

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
    </div>
  );
}
