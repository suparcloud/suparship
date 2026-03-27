import { useEffect, useState } from "react";

import { fetchTeams } from "../lib/settings";
import type { TeamInfo } from "../types";

export function TeamSettings() {
  const [teams, setTeams] = useState<TeamInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;

    async function load() {
      try {
        const data = await fetchTeams();
        if (cancelled) return;
        setTeams(data.teams);
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
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((n) => (
            <div
              key={n}
              className="h-40 animate-pulse rounded-lg bg-gray-50"
            />
          ))}
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="rounded-lg border border-red-200 bg-red-50 p-4">
        <p className="text-sm text-red-700">
          Failed to load teams: {error}
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">Teams</h1>
        <p className="mt-1 text-sm text-gray-500">
          Teams and their members in your organization.
        </p>
      </div>

      {teams.length === 0 ? (
        <div className="rounded-lg border border-gray-200 bg-white px-6 py-12 text-center">
          <p className="text-sm text-gray-400">No teams configured yet.</p>
        </div>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {teams.map((team) => (
            <div
              key={team.name}
              className="rounded-lg border border-gray-200 bg-white"
            >
              <div className="border-b border-gray-100 px-5 py-4">
                <h3 className="text-sm font-semibold text-gray-900">
                  {team.displayName || team.name}
                </h3>
                {team.displayName && team.displayName !== team.name && (
                  <p className="mt-0.5 text-xs text-gray-400">{team.name}</p>
                )}
              </div>
              <div className="px-5 py-4">
                <p className="mb-2 text-xs font-medium uppercase tracking-wider text-gray-500">
                  Members ({team.members.length})
                </p>
                {team.members.length === 0 ? (
                  <p className="text-sm text-gray-400 italic">No members</p>
                ) : (
                  <div className="flex flex-wrap gap-1.5">
                    {team.members.map((member) => (
                      <span
                        key={member}
                        className="inline-block rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-700"
                      >
                        {member}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
