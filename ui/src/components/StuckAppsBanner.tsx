import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";

import { listStuckApps, unstickApp, type StuckApp } from "../lib/platform";

// StuckAppsBanner proactively surfaces ArgoCD Applications wedged in
// Terminating (a finalizer that can't complete — e.g. its AppProject was
// removed) and offers a one-click unstick, so an operator never has to hand
// -patch finalizers. Renders nothing when there's nothing stuck, on error, or
// for non-admins (the endpoint 403s and we simply stay quiet).
export function StuckAppsBanner() {
  const [apps, setApps] = useState<StuckApp[]>([]);
  const [busy, setBusy] = useState<string | null>(null);

  const refresh = useCallback(() => {
    listStuckApps()
      .then((r) => setApps(r.stuckApps ?? []))
      .catch(() => setApps([])); // 403 for non-admins / no ArgoCD → stay quiet
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  async function handleUnstick(name: string) {
    setBusy(name);
    try {
      await unstickApp(name);
      toast.success(`Unstuck "${name}" — its deletion can now complete.`);
      setApps((cur) => cur.filter((a) => a.name !== name));
    } catch (err) {
      toast.error(err instanceof Error ? err.message : `Failed to unstick "${name}"`);
    } finally {
      setBusy(null);
    }
  }

  if (apps.length === 0) return null;

  return (
    <div className="rounded-xl border border-amber-200 bg-amber-50 p-4">
      <div className="flex items-center gap-2">
        <svg className="h-5 w-5 text-amber-600" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
        </svg>
        <h2 className="text-sm font-semibold text-amber-900">
          {apps.length} application{apps.length > 1 ? "s" : ""} stuck deleting
        </h2>
      </div>
      <p className="mt-1 text-xs text-amber-800">
        These ArgoCD Applications can&apos;t finish deleting because a finalizer
        is blocked. Unsticking removes ArgoCD&apos;s cascade-delete finalizer so
        the object is removed; any already-orphaned workloads stay until cleaned
        up directly.
      </p>
      <div className="mt-3 space-y-2">
        {apps.map((a) => (
          <div
            key={a.name}
            className="flex items-start justify-between gap-4 rounded-lg border border-amber-200 bg-white px-3 py-2"
          >
            <div className="min-w-0">
              <p className="font-mono text-sm text-gray-900">{a.name}</p>
              <p className="text-xs text-gray-600">{a.reason}</p>
            </div>
            <button
              onClick={() => handleUnstick(a.name)}
              disabled={busy === a.name}
              className="flex-shrink-0 rounded-full border border-amber-700 bg-white px-3 py-1 text-xs font-medium text-amber-800 transition-colors hover:bg-amber-700 hover:text-white disabled:opacity-50"
            >
              {busy === a.name ? "Unsticking…" : "Unstick"}
            </button>
          </div>
        ))}
      </div>
    </div>
  );
}
