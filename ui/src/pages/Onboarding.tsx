import { useEffect, useState } from "react";
import { Link } from "react-router-dom";

import { useAuth } from "../lib/AuthContext";
import { fetchOnboardingStatus } from "../lib/onboarding";
import type { OnboardingStatus } from "../types";

// ---------------------------------------------------------------------------
// Checklist item config
// ---------------------------------------------------------------------------

interface ChecklistItem {
  key: keyof OnboardingStatus;
  title: string;
  description: string;
  successText: string;
  /** Route to navigate to when this item is still pending. */
  actionTo?: string;
  /** Label for the action link. */
  actionLabel?: string;
}

const checklist: ChecklistItem[] = [
  {
    key: "clusterConnected",
    title: "Cluster connected",
    description: "suparship can reach the Kubernetes API.",
    successText: "Connected",
    // No UI action — requires infra-level fix (kubeconfig / cluster).
  },
  {
    key: "authConfigured",
    title: "Authentication ready",
    description: "Admin credentials are bootstrapped.",
    successText: "Configured",
    // No UI action — run `suparship admin bootstrap` from the CLI.
  },
  {
    key: "orgExists",
    title: "Organization created",
    description: "Your default organization is set up.",
    successText: "Created",
    actionTo: "/settings/org",
    actionLabel: "View org",
  },
  {
    key: "hasEnvironments",
    title: "Environments defined",
    description: "At least one deployment environment exists.",
    successText: "Ready",
    actionTo: "/settings/org",
    actionLabel: "Add environment",
  },
  {
    key: "hasProjects",
    title: "First project created",
    description: "A project groups your apps and inherits the org's deployment pipeline.",
    successText: "Created",
    actionTo: "/?newProject=1",
    actionLabel: "Create project",
  },
  {
    key: "hasServices",
    title: "First app deployed",
    description: "An app has been created from a template and is running across its environments.",
    successText: "Deployed",
    actionTo: "/projects/demo/apps/new",
    actionLabel: "Create app",
  },
];

// ---------------------------------------------------------------------------
// Next-step CTAs
// ---------------------------------------------------------------------------

interface CTA {
  title: string;
  description: string;
  to: string;
  icon: "compass" | "branch" | "rocket";
  primary?: boolean;
}

const nextSteps: CTA[] = [
  {
    title: "Explore the platform",
    description: "View your apps, templates, and environments on the dashboard.",
    to: "/",
    icon: "compass",
    primary: true,
  },
  {
    title: "Browse templates",
    description: "Pick a golden path and deploy your first app to staging and prod.",
    to: "/templates",
    icon: "branch",
  },
  {
    title: "Organization settings",
    description: "Manage environments, teams, clusters, and role bindings.",
    to: "/settings/org",
    icon: "rocket",
  },
];

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function Onboarding() {
  const { user } = useAuth();
  const [status, setStatus] = useState<OnboardingStatus | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    fetchOnboardingStatus()
      .then((s) => {
        if (!cancelled) setStatus(s);
      })
      .catch(() => {})
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const completedCount = status
    ? checklist.filter((item) => status[item.key]).length
    : 0;
  const totalCount = checklist.length;
  const allComplete = status?.complete ?? false;
  const progressPct = totalCount > 0 ? (completedCount / totalCount) * 100 : 0;

  return (
    <div className="flex min-h-screen flex-col bg-gray-50">
      {/* Minimal header */}
      <header className="flex h-14 items-center justify-between border-b border-gray-200 bg-white px-6">
        <span className="text-lg font-semibold tracking-tight">suparship</span>
        {user && (
          <span className="text-sm text-gray-500">
            Signed in as <span className="font-medium text-gray-700">{user.username}</span>
          </span>
        )}
      </header>

      <div className="flex flex-1 items-start justify-center px-4 py-12">
        <div className="w-full max-w-2xl space-y-8">
          {/* Hero */}
          <div className="text-center">
            <div className="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-gray-900">
              <svg className="h-7 w-7 text-white" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                <path strokeLinecap="round" strokeLinejoin="round" d="M15.59 14.37a6 6 0 0 1-5.84 7.38v-4.8m5.84-2.58a14.98 14.98 0 0 0 6.16-12.12A14.98 14.98 0 0 0 9.631 8.41m5.96 5.96a14.926 14.926 0 0 1-5.841 2.58m-.119-8.54a6 6 0 0 0-7.381 5.84h4.8m2.581-5.84a14.927 14.927 0 0 0-2.58 5.84m2.699 2.7c-.103.021-.207.041-.311.06a15.09 15.09 0 0 1-2.448-2.448 14.9 14.9 0 0 1 .06-.312m-2.24 2.39a4.493 4.493 0 0 0-1.757 4.306 4.493 4.493 0 0 0 4.306-1.758M16.5 9a1.5 1.5 0 1 1-3 0 1.5 1.5 0 0 1 3 0Z" />
              </svg>
            </div>
            <h1 className="text-3xl font-bold tracking-tight text-gray-900">
              {allComplete ? "You're all set!" : "Welcome to suparship"}
            </h1>
            <p className="mx-auto mt-2 max-w-md text-base text-gray-500">
              {allComplete
                ? "Your platform is configured and ready. Explore the dashboard or dive into the next steps below."
                : "Let's make sure everything is connected and ready to go."}
            </p>
          </div>

          {/* Progress bar */}
          {!loading && status && (
            <div>
              <div className="mb-2 flex items-center justify-between text-sm">
                <span className="font-medium text-gray-700">
                  Setup progress
                </span>
                <span className="text-gray-500">
                  {completedCount} of {totalCount} complete
                </span>
              </div>
              <div className="h-2 overflow-hidden rounded-full bg-gray-200">
                <div
                  className={`h-full rounded-full transition-all duration-500 ${
                    allComplete ? "bg-emerald-500" : "bg-gray-900"
                  }`}
                  style={{ width: `${progressPct}%` }}
                />
              </div>
            </div>
          )}

          {/* Platform setup gates — the SRE-facing checklist with real
              readiness and remediation per step. */}
          {!loading && status?.gates && status.gates.length > 0 && (
            <div>
              <h2 className="mb-2 text-sm font-medium uppercase tracking-wider text-gray-400">
                Platform setup
              </h2>
              <div className="overflow-hidden rounded-xl border border-gray-200 bg-white">
                {status.gates.map((g, i) => (
                  <div
                    key={g.key}
                    className={`flex items-start gap-4 px-5 py-4 ${
                      i > 0 ? "border-t border-gray-100" : ""
                    }`}
                  >
                    <GateIcon status={g.status} />
                    <div className="min-w-0 flex-1">
                      <p
                        className={`text-sm font-medium ${
                          g.status === "ok" ? "text-gray-900" : "text-gray-700"
                        }`}
                      >
                        {g.title}
                      </p>
                      {g.message && (
                        <p
                          className={`mt-0.5 text-xs ${
                            g.status === "error"
                              ? "text-red-600"
                              : g.status === "incomplete"
                                ? "text-amber-600"
                                : "text-gray-400"
                          }`}
                        >
                          {g.message}
                        </p>
                      )}
                    </div>
                    {g.status === "ok" ? (
                      <span className="flex-shrink-0 rounded-full bg-emerald-50 px-2.5 py-0.5 text-xs font-medium text-emerald-700">
                        Ready
                      </span>
                    ) : g.action ? (
                      <Link
                        to={g.action}
                        className="flex-shrink-0 rounded-full border border-gray-900 bg-white px-3 py-1 text-xs font-medium text-gray-900 transition-colors hover:bg-gray-900 hover:text-white"
                      >
                        Fix
                      </Link>
                    ) : (
                      <span className="flex-shrink-0 rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-500">
                        Action needed
                      </span>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Onboarding checklist (org/projects/apps) */}
          {loading ? (
            <ChecklistSkeleton />
          ) : status ? (
            <div className="overflow-hidden rounded-xl border border-gray-200 bg-white">
              {checklist.map((item, i) => {
                const done = status[item.key] as boolean;
                return (
                  <div
                    key={item.key}
                    className={`flex items-center gap-4 px-5 py-4 ${
                      i > 0 ? "border-t border-gray-100" : ""
                    }`}
                  >
                    <StatusIcon done={done} />
                    <div className="min-w-0 flex-1">
                      <p className={`text-sm font-medium ${done ? "text-gray-900" : "text-gray-500"}`}>
                        {item.title}
                      </p>
                      <p className="text-xs text-gray-400">
                        {item.description}
                      </p>
                    </div>
                    {done ? (
                      <span className="flex-shrink-0 rounded-full bg-emerald-50 px-2.5 py-0.5 text-xs font-medium text-emerald-700">
                        {item.successText}
                      </span>
                    ) : item.actionTo ? (
                      <Link
                        to={item.actionTo}
                        className="flex-shrink-0 rounded-full border border-gray-900 bg-white px-3 py-1 text-xs font-medium text-gray-900 transition-colors hover:bg-gray-900 hover:text-white"
                      >
                        {item.actionLabel ?? "Fix"}
                      </Link>
                    ) : (
                      <span className="flex-shrink-0 rounded-full bg-gray-100 px-2.5 py-0.5 text-xs font-medium text-gray-500">
                        Pending
                      </span>
                    )}
                  </div>
                );
              })}
            </div>
          ) : null}

          {/* Next steps — always visible */}
          {!loading && (
            <div>
              <h2 className="mb-3 text-sm font-medium uppercase tracking-wider text-gray-400">
                {allComplete ? "Next steps" : "When you're ready"}
              </h2>
              <div className="grid gap-3 sm:grid-cols-3">
                {nextSteps.map((cta) => (
                  <Link
                    key={cta.to}
                    to={cta.to}
                    className={`group rounded-xl border p-4 transition-all ${
                      cta.primary
                        ? "border-gray-900 bg-gray-900 text-white hover:bg-gray-800"
                        : "border-gray-200 bg-white text-gray-900 hover:border-gray-300 hover:shadow-sm"
                    }`}
                  >
                    <div className="mb-2">
                      <CTAIcon type={cta.icon} primary={cta.primary} />
                    </div>
                    <p className="text-sm font-semibold">{cta.title}</p>
                    <p className={`mt-0.5 text-xs ${cta.primary ? "text-gray-300" : "text-gray-500"}`}>
                      {cta.description}
                    </p>
                  </Link>
                ))}
              </div>
            </div>
          )}

          {/* Footer actions */}
          {!loading && (
            <div className="flex items-center justify-between border-t border-gray-200 pt-6">
              <Link
                to="/"
                className="rounded-lg bg-gray-900 px-5 py-2.5 text-sm font-medium text-white transition-colors hover:bg-gray-700"
              >
                Go to dashboard
              </Link>
              <button
                className="rounded-lg border border-gray-200 bg-white px-4 py-2 text-sm font-medium text-gray-500 transition-colors hover:bg-gray-50 hover:text-gray-700"
                title="Reset the demo environment (coming soon)"
              >
                Reset demo
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

function StatusIcon({ done }: { done: boolean }) {
  if (done) {
    return (
      <div className="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-full bg-emerald-100">
        <svg className="h-4 w-4 text-emerald-600" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="m4.5 12.75 6 6 9-13.5" />
        </svg>
      </div>
    );
  }
  return (
    <div className="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-full border-2 border-dashed border-gray-300">
      <div className="h-2 w-2 rounded-full bg-gray-300" />
    </div>
  );
}

function GateIcon({ status }: { status: string }) {
  if (status === "ok") {
    return (
      <div className="mt-0.5 flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full bg-emerald-100">
        <svg className="h-4 w-4 text-emerald-600" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="m4.5 12.75 6 6 9-13.5" />
        </svg>
      </div>
    );
  }
  const color =
    status === "error"
      ? "bg-red-100 text-red-600"
      : "bg-amber-100 text-amber-600";
  return (
    <div className={`mt-0.5 flex h-7 w-7 flex-shrink-0 items-center justify-center rounded-full ${color}`}>
      <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
        <path strokeLinecap="round" strokeLinejoin="round" d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z" />
      </svg>
    </div>
  );
}

function CTAIcon({ type, primary }: { type: string; primary?: boolean }) {
  const cls = `h-5 w-5 ${primary ? "text-gray-300" : "text-gray-400 group-hover:text-gray-600"}`;
  switch (type) {
    case "compass":
      return (
        <svg className={cls} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M12 21a9.004 9.004 0 0 0 8.716-6.747M12 21a9.004 9.004 0 0 1-8.716-6.747M12 21c2.485 0 4.5-4.03 4.5-9S14.485 3 12 3m0 18c-2.485 0-4.5-4.03-4.5-9S9.515 3 12 3m0 0a8.997 8.997 0 0 1 7.843 4.582M12 3a8.997 8.997 0 0 0-7.843 4.582m15.686 0A11.953 11.953 0 0 1 12 10.5c-2.998 0-5.74-1.1-7.843-2.918m15.686 0A8.959 8.959 0 0 1 21 12c0 .778-.099 1.533-.284 2.253m0 0A17.919 17.919 0 0 1 12 16.5a17.92 17.92 0 0 1-8.716-2.247m0 0A8.966 8.966 0 0 1 3 12c0-1.97.633-3.794 1.708-5.276" />
        </svg>
      );
    case "branch":
      return (
        <svg className={cls} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="m21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25" />
        </svg>
      );
    case "rocket":
      return (
        <svg className={cls} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M9.594 3.94c.09-.542.56-.94 1.11-.94h2.593c.55 0 1.02.398 1.11.94l.213 1.281c.063.374.313.686.645.87.074.04.147.083.22.127.325.196.72.257 1.075.124l1.217-.456a1.125 1.125 0 0 1 1.37.49l1.296 2.247a1.125 1.125 0 0 1-.26 1.431l-1.003.827c-.293.241-.438.613-.43.992a7.723 7.723 0 0 1 0 .255c-.008.378.137.75.43.991l1.004.827c.424.35.534.955.26 1.43l-1.298 2.247a1.125 1.125 0 0 1-1.369.491l-1.217-.456c-.355-.133-.75-.072-1.076.124a6.47 6.47 0 0 1-.22.128c-.331.183-.581.495-.644.869l-.213 1.281c-.09.543-.56.94-1.11.94h-2.594c-.55 0-1.019-.398-1.11-.94l-.213-1.281c-.062-.374-.312-.686-.644-.87a6.52 6.52 0 0 1-.22-.127c-.325-.196-.72-.257-1.076-.124l-1.217.456a1.125 1.125 0 0 1-1.369-.49l-1.297-2.247a1.125 1.125 0 0 1 .26-1.431l1.004-.827c.292-.24.437-.613.43-.991a6.932 6.932 0 0 1 0-.255c.007-.38-.138-.751-.43-.992l-1.004-.827a1.125 1.125 0 0 1-.26-1.43l1.297-2.247a1.125 1.125 0 0 1 1.37-.491l1.216.456c.356.133.751.072 1.076-.124.072-.044.146-.086.22-.128.332-.183.582-.495.644-.869l.214-1.28Z" />
          <path strokeLinecap="round" strokeLinejoin="round" d="M15 12a3 3 0 1 1-6 0 3 3 0 0 1 6 0Z" />
        </svg>
      );
    default:
      return null;
  }
}

function ChecklistSkeleton() {
  return (
    <div className="overflow-hidden rounded-xl border border-gray-200 bg-white">
      {[1, 2, 3, 4, 5].map((n) => (
        <div
          key={n}
          className={`flex items-center gap-4 px-5 py-4 ${n > 1 ? "border-t border-gray-100" : ""}`}
        >
          <div className="h-8 w-8 animate-pulse rounded-full bg-gray-100" />
          <div className="flex-1 space-y-1.5">
            <div className="h-4 w-36 animate-pulse rounded bg-gray-100" />
            <div className="h-3 w-56 animate-pulse rounded bg-gray-50" />
          </div>
          <div className="h-5 w-16 animate-pulse rounded-full bg-gray-100" />
        </div>
      ))}
    </div>
  );
}
