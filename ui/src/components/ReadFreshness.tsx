import { useEffect, useState } from "react";
import { useLocation } from "react-router-dom";
import { readStates, watchedReadPaths } from "../lib/readViews";

export function ReadFreshness() {
  const [, render] = useState(0);
  const location = useLocation();
  useEffect(() => { readStates.clear(); render(n => n + 1); }, [location.pathname]);
  useEffect(() => {
    const update = () => render(n => n + 1);
    window.addEventListener("read-freshness", update);
    const timer = setInterval(update, 1000);
    return () => { window.removeEventListener("read-freshness", update); clearInterval(timer); };
  }, []);
  // Incidental editor/catalog reads must not mark the active runtime view stale.
  const states = [...readStates.entries()]
    .filter(([path]) => watchedReadPaths.size === 0 || watchedReadPaths.has(path))
    .map(([, state]) => state);
  if (!states.length) return null;
  const syncing = states.some(s => s.state === "synchronizing");
  const stale = states.some(s => (s.state === "stale" || s.state === "unavailable") || (s.observedAt && Date.now() - Date.parse(s.observedAt) > 15000));
  const times = states.map(s => Date.parse(s.observedAt)).filter(Number.isFinite);
  const oldest = times.length ? Math.min(...times) : undefined;
  return <div role="status" className={`mb-3 rounded border px-3 py-2 text-xs ${stale || syncing ? "border-amber-200 bg-amber-50 text-amber-900" : "border-gray-200 text-gray-500"}`}>
    {syncing ? "Synchronizing view…" : stale ? "Updates delayed — showing last observed state." : "View up to date."}
    {oldest && <> Last observed {new Date(oldest).toLocaleTimeString()}.</>}
  </div>;
}
