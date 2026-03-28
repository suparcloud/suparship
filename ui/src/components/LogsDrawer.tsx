import { useCallback, useEffect, useRef, useState } from "react";

import { fetchLogs } from "../lib/services";
import type { LogsResponse, ServiceEnv } from "../types";

interface LogsDrawerProps {
  open: boolean;
  onClose: () => void;
  project: string;
  service: string;
  environments: ServiceEnv[];
}

const TAIL_LINES = 200;

export function LogsDrawer({
  open,
  onClose,
  project,
  service,
  environments,
}: LogsDrawerProps) {
  const [env, setEnv] = useState("");
  const [pod, setPod] = useState("");
  const [container, setContainer] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<LogsResponse | null>(null);
  const scrollRef = useRef<HTMLPreElement>(null);

  useEffect(() => {
    const first = environments[0] as ServiceEnv | undefined;
    if (open && first && !env) {
      setEnv(first.environment);
    }
  }, [open, environments, env]);

  const loadLogs = useCallback(async () => {
    if (!env) return;
    setLoading(true);
    setError(null);
    try {
      const data = await fetchLogs(project, service, {
        environment: env,
        pod: pod || undefined,
        container: container || undefined,
        tailLines: TAIL_LINES,
      });
      setResult(data);
      requestAnimationFrame(() => {
        scrollRef.current?.scrollTo(0, scrollRef.current.scrollHeight);
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to fetch logs");
      setResult(null);
    } finally {
      setLoading(false);
    }
  }, [project, service, env, pod, container]);

  useEffect(() => {
    if (open && env) {
      loadLogs();
    }
  }, [open, env, loadLogs]);

  useEffect(() => {
    if (!open) {
      setResult(null);
      setError(null);
      setPod("");
      setContainer("");
      return;
    }
    const handleKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handleKey);
    return () => document.removeEventListener("keydown", handleKey);
  }, [open, onClose]);

  if (!open) return null;

  return (
    <>
      {/* Backdrop */}
      <div
        className="fixed inset-0 z-40 bg-black/30 transition-opacity"
        onClick={onClose}
      />

      {/* Drawer */}
      <div className="fixed inset-y-0 right-0 z-50 flex w-full max-w-2xl flex-col border-l border-gray-200 bg-white shadow-2xl">
        {/* Header */}
        <div className="flex items-center justify-between border-b border-gray-200 px-5 py-3">
          <div className="flex items-center gap-2">
            <svg
              className="h-5 w-5 text-gray-400"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
              strokeWidth={1.5}
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                d="m6.75 7.5 3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0 0 21 18V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v12a2.25 2.25 0 0 0 2.25 2.25Z"
              />
            </svg>
            <h2 className="text-sm font-semibold text-gray-900">
              Logs &mdash;{" "}
              <span className="font-mono font-normal text-gray-600">
                {service}
              </span>
            </h2>
          </div>
          <div className="flex items-center gap-2">
            <button
              onClick={loadLogs}
              disabled={loading || !env}
              className="inline-flex items-center gap-1.5 rounded-md border border-gray-200 bg-white px-2.5 py-1.5 text-xs font-medium text-gray-600 transition-colors hover:bg-gray-50 disabled:opacity-50"
              title="Refresh"
            >
              <svg
                className={`h-3.5 w-3.5 ${loading ? "animate-spin" : ""}`}
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                strokeWidth={2}
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182"
                />
              </svg>
              Refresh
            </button>
            <button
              onClick={onClose}
              className="rounded-md p-1 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600"
            >
              <svg
                className="h-5 w-5"
                fill="none"
                viewBox="0 0 24 24"
                stroke="currentColor"
                strokeWidth={1.5}
              >
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  d="M6 18 18 6M6 6l12 12"
                />
              </svg>
            </button>
          </div>
        </div>

        {/* Filters */}
        <div className="flex flex-wrap items-end gap-3 border-b border-gray-100 bg-gray-50/80 px-5 py-3">
          <FilterSelect
            label="Environment"
            value={env}
            onChange={(v) => {
              setEnv(v);
              setPod("");
              setContainer("");
            }}
            options={environments.map((e) => ({
              value: e.environment,
              label: e.environment,
            }))}
          />
          <FilterInput
            label="Pod"
            value={pod}
            onChange={setPod}
            placeholder="auto-select"
          />
          <FilterInput
            label="Container"
            value={container}
            onChange={setContainer}
            placeholder="default"
          />
        </div>

        {/* Resolved pod/container label */}
        {result && (
          <div className="flex items-center gap-3 border-b border-gray-100 px-5 py-2 text-xs text-gray-400">
            <span>
              pod:{" "}
              <span className="font-mono text-gray-600">{result.pod}</span>
            </span>
            <span>
              container:{" "}
              <span className="font-mono text-gray-600">
                {result.container}
              </span>
            </span>
          </div>
        )}

        {/* Log output */}
        <div className="relative flex-1 overflow-hidden">
          {loading && !result && <LogsSkeleton />}

          {error && (
            <div className="flex h-full flex-col items-center justify-center px-6 text-center">
              <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-red-50">
                <svg
                  className="h-5 w-5 text-red-400"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                  strokeWidth={1.5}
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    d="M12 9v3.75m9-.75a9 9 0 1 1-18 0 9 9 0 0 1 18 0Zm-9 3.75h.008v.008H12v-.008Z"
                  />
                </svg>
              </div>
              <p className="text-sm font-medium text-gray-700">{error}</p>
              <button
                onClick={loadLogs}
                className="mt-3 text-sm text-blue-600 hover:text-blue-800"
              >
                Try again
              </button>
            </div>
          )}

          {!loading && !error && result && result.logs.length === 0 && (
            <div className="flex h-full flex-col items-center justify-center px-6 text-center">
              <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-full bg-gray-100">
                <svg
                  className="h-5 w-5 text-gray-400"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                  strokeWidth={1.5}
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    d="m6.75 7.5 3 2.25-3 2.25m4.5 0h3m-9 8.25h13.5A2.25 2.25 0 0 0 21 18V6a2.25 2.25 0 0 0-2.25-2.25H5.25A2.25 2.25 0 0 0 3 6v12a2.25 2.25 0 0 0 2.25 2.25Z"
                  />
                </svg>
              </div>
              <p className="text-sm font-medium text-gray-500">
                No log output
              </p>
              <p className="mt-1 text-xs text-gray-400">
                The container has not produced any logs yet.
              </p>
            </div>
          )}

          {result && result.logs.length > 0 && (
            <pre
              ref={scrollRef}
              className="h-full overflow-auto bg-gray-950 px-4 py-3 font-mono text-xs leading-5 text-gray-200"
            >
              {result.logs}
            </pre>
          )}

          {loading && result && (
            <div className="absolute right-4 top-3">
              <div className="h-4 w-4 animate-spin rounded-full border-2 border-gray-400 border-t-transparent" />
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="flex items-center justify-between border-t border-gray-200 px-5 py-2 text-xs text-gray-400">
          <span>
            Showing last {TAIL_LINES} lines
          </span>
          <kbd className="rounded border border-gray-200 bg-gray-50 px-1.5 py-0.5 font-mono text-gray-500">
            Esc
          </kbd>
        </div>
      </div>
    </>
  );
}

// --- Sub-components ---

function FilterSelect({
  label,
  value,
  onChange,
  options,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  options: { value: string; label: string }[];
}) {
  return (
    <div>
      <label className="mb-1 block text-xs font-medium text-gray-500">
        {label}
      </label>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="rounded-md border border-gray-300 bg-white px-2.5 py-1.5 text-xs text-gray-900 focus:border-gray-500 focus:outline-none focus:ring-1 focus:ring-gray-500"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </div>
  );
}

function FilterInput({
  label,
  value,
  onChange,
  placeholder,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder: string;
}) {
  return (
    <div>
      <label className="mb-1 block text-xs font-medium text-gray-500">
        {label}
      </label>
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="w-36 rounded-md border border-gray-300 bg-white px-2.5 py-1.5 text-xs text-gray-900 placeholder-gray-400 focus:border-gray-500 focus:outline-none focus:ring-1 focus:ring-gray-500"
      />
    </div>
  );
}

function LogsSkeleton() {
  return (
    <div className="space-y-2 bg-gray-950 px-4 py-3">
      {Array.from({ length: 12 }).map((_, i) => (
        <div
          key={i}
          className="h-3.5 animate-pulse rounded bg-gray-800"
          style={{ width: `${40 + Math.random() * 50}%` }}
        />
      ))}
    </div>
  );
}
