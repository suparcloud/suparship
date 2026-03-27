import { useEffect, useState } from "react";

import { fetchOrg, fetchAllRoleBindings } from "../lib/settings";
import type { OrgInfo, RoleBinding } from "../types";

const roleBadgeColor: Record<string, string> = {
  org_admin: "bg-red-50 text-red-700",
  project_admin: "bg-amber-50 text-amber-700",
  developer: "bg-blue-50 text-blue-700",
  viewer: "bg-gray-100 text-gray-600",
};

function RoleBadge({ role }: { role: string }) {
  const color = roleBadgeColor[role] ?? "bg-gray-100 text-gray-600";
  return (
    <span
      className={`inline-block rounded-full px-2.5 py-0.5 text-xs font-medium ${color}`}
    >
      {role}
    </span>
  );
}

export function OrgSettings() {
  const [org, setOrg] = useState<OrgInfo | null>(null);
  const [bindings, setBindings] = useState<RoleBinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      try {
        const [orgData, bindingsData] = await Promise.all([
          fetchOrg(),
          fetchAllRoleBindings(),
        ]);
        if (cancelled) return;
        setOrg(orgData);
        setBindings(bindingsData);
      } catch (err) {
        if (cancelled) return;
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

  if (loading) {
    return (
      <div className="space-y-4">
        <div className="h-8 w-48 animate-pulse rounded bg-gray-100" />
        <div className="h-32 animate-pulse rounded-lg bg-gray-50" />
        <div className="h-48 animate-pulse rounded-lg bg-gray-50" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">
          Failed to load organization settings: {error}
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Organization</h1>
        <p className="mt-1 text-sm text-gray-500">
          Organization details and project role bindings.
        </p>
      </div>

      {org && (
        <div className="rounded-lg border border-gray-200 bg-white">
          <div className="border-b border-gray-100 px-6 py-4">
            <h2 className="text-sm font-medium text-gray-500">
              Organization Details
            </h2>
          </div>
          <dl className="divide-y divide-gray-100">
            <div className="grid grid-cols-3 px-6 py-3">
              <dt className="text-sm font-medium text-gray-500">Name</dt>
              <dd className="col-span-2 text-sm text-gray-900">{org.name}</dd>
            </div>
            <div className="grid grid-cols-3 px-6 py-3">
              <dt className="text-sm font-medium text-gray-500">
                Display Name
              </dt>
              <dd className="col-span-2 text-sm text-gray-900">
                {org.displayName}
              </dd>
            </div>
            {org.createdAt && (
              <div className="grid grid-cols-3 px-6 py-3">
                <dt className="text-sm font-medium text-gray-500">Created</dt>
                <dd className="col-span-2 text-sm text-gray-900">
                  {new Date(org.createdAt).toLocaleDateString(undefined, {
                    year: "numeric",
                    month: "long",
                    day: "numeric",
                  })}
                </dd>
              </div>
            )}
          </dl>
        </div>
      )}

      <div className="rounded-lg border border-gray-200 bg-white">
        <div className="border-b border-gray-100 px-6 py-4">
          <h2 className="text-sm font-medium text-gray-500">
            Project Role Bindings
          </h2>
        </div>
        {bindings.length === 0 ? (
          <div className="px-6 py-12 text-center">
            <p className="text-sm text-gray-400">
              No role bindings configured yet.
            </p>
          </div>
        ) : (
          <table className="w-full">
            <thead>
              <tr className="border-b border-gray-100 text-left text-xs font-medium uppercase tracking-wider text-gray-500">
                <th className="px-6 py-3">Project</th>
                <th className="px-6 py-3">Team</th>
                <th className="px-6 py-3">Role</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-50">
              {bindings.map((rb, i) => (
                <tr key={i} className="hover:bg-gray-50">
                  <td className="px-6 py-3 text-sm text-gray-900">
                    {rb.project === "*" ? (
                      <span className="text-gray-400 italic">All projects</span>
                    ) : (
                      rb.project
                    )}
                  </td>
                  <td className="px-6 py-3 text-sm text-gray-900">
                    {rb.team}
                  </td>
                  <td className="px-6 py-3">
                    <RoleBadge role={rb.role} />
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
