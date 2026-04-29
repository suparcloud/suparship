import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "sonner";

import { ApiError } from "../lib/api";
import {
  importTemplate,
  importTemplatePreview,
} from "../lib/templates";
import type { TemplateImportPreview } from "../types";

// kibibyteFmt formats a byte count for the chart-files panel; charts are
// almost always tiny, so KiB granularity is plenty.
function kibibyteFmt(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  return `${(bytes / 1024).toFixed(1)} KiB`;
}

export function TemplateImport() {
  const navigate = useNavigate();

  const [file, setFile] = useState<File | null>(null);
  const [preview, setPreview] = useState<TemplateImportPreview | null>(null);
  const [editedYAML, setEditedYAML] = useState<string>("");
  const [previewing, setPreviewing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // handlePreview ships the chart to the backend for introspection. The
  // returned template.yaml seeds the editor; the operator can refine it
  // before saving.
  async function handlePreview(f: File) {
    setError(null);
    setPreview(null);
    setEditedYAML("");
    setPreviewing(true);
    try {
      const p = await importTemplatePreview(f);
      setPreview(p);
      setEditedYAML(p.templateYAML);
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? err.message
          : err instanceof Error
            ? err.message
            : "Failed to parse chart";
      setError(msg);
      toast.error(msg);
    } finally {
      setPreviewing(false);
    }
  }

  // handleSave persists the (possibly edited) template + chart bundle to
  // the cluster. We re-upload the original .tgz because the backend is
  // stateless — the preview request didn't store anything.
  async function handleSave() {
    if (!file || !preview) return;
    setSaving(true);
    setError(null);
    try {
      const result = await importTemplate(file, editedYAML);
      toast.success(`Imported ${result.name} v${result.version}`);
      navigate(`/templates/${encodeURIComponent(result.name)}`);
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? err.message
          : err instanceof Error
            ? err.message
            : "Save failed";
      setError(msg);
      toast.error(msg);
    } finally {
      setSaving(false);
    }
  }

  function handleFileChange(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    setFile(f);
    handlePreview(f);
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold text-gray-900">
          Import a Helm chart
        </h1>
        <p className="mt-1 text-sm text-gray-500">
          Upload a packaged chart (.tgz). suparship reads Chart.yaml +
          values.schema.json (or values.yaml) and generates a starter
          template you can review before saving.
        </p>
      </div>

      <div className="rounded-xl border border-gray-200 bg-white p-6">
        <label className="block text-sm font-medium text-gray-900">
          Chart archive
        </label>
        <p className="mt-1 text-xs text-gray-500">
          Run <code className="rounded bg-gray-100 px-1.5 py-0.5 font-mono">helm package ./your-chart</code>{" "}
          to produce a .tgz, then upload it here.
        </p>
        <input
          type="file"
          accept=".tgz,.tar.gz,application/gzip"
          onChange={handleFileChange}
          disabled={previewing || saving}
          className="mt-3 block w-full text-sm text-gray-700 file:mr-4 file:rounded-md file:border-0 file:bg-gray-900 file:px-4 file:py-2 file:text-sm file:font-medium file:text-white hover:file:bg-gray-800 disabled:opacity-50"
        />
        {file && (
          <p className="mt-2 text-xs text-gray-500">
            Selected: <span className="font-mono">{file.name}</span> ({kibibyteFmt(file.size)})
          </p>
        )}
      </div>

      {error && !preview && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">{error}</p>
        </div>
      )}

      {previewing && (
        <div className="rounded-xl border border-gray-200 bg-white p-6">
          <p className="text-sm text-gray-500">Parsing chart…</p>
        </div>
      )}

      {preview && <PreviewPanel preview={preview} />}

      {preview && (
        <div className="rounded-xl border border-gray-200 bg-white p-6">
          <div className="flex items-center justify-between">
            <div>
              <h2 className="text-base font-semibold text-gray-900">
                Generated template.yaml
              </h2>
              <p className="mt-1 text-xs text-gray-500">
                Review and edit before saving. Categories, titles, and input
                descriptions are best-effort guesses.
              </p>
            </div>
          </div>
          <textarea
            value={editedYAML}
            onChange={(e) => setEditedYAML(e.target.value)}
            spellCheck={false}
            rows={20}
            className="mt-4 block w-full rounded-md border border-gray-200 bg-gray-50 px-3 py-2 font-mono text-xs text-gray-800 focus:border-gray-400 focus:outline-none"
          />
        </div>
      )}

      {preview && error && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-4">
          <p className="text-sm text-red-700">{error}</p>
        </div>
      )}

      {preview && (
        <div className="flex items-center justify-end gap-3">
          <button
            type="button"
            onClick={() => navigate("/templates")}
            disabled={saving}
            className="rounded-md border border-gray-300 bg-white px-4 py-2 text-sm font-medium text-gray-700 hover:bg-gray-50 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={handleSave}
            disabled={saving || !editedYAML.trim()}
            className="rounded-md bg-gray-900 px-4 py-2 text-sm font-medium text-white hover:bg-gray-800 disabled:opacity-50"
          >
            {saving ? "Saving…" : "Save template"}
          </button>
        </div>
      )}
    </div>
  );
}

function PreviewPanel({ preview }: { preview: TemplateImportPreview }) {
  const s = preview.summary;
  return (
    <div className="rounded-xl border border-gray-200 bg-white p-6">
      <h2 className="text-base font-semibold text-gray-900">
        Detected from chart
      </h2>
      <dl className="mt-4 grid grid-cols-2 gap-x-6 gap-y-3 text-sm sm:grid-cols-4">
        <div>
          <dt className="text-xs uppercase tracking-wide text-gray-400">Chart</dt>
          <dd className="mt-1 font-mono text-gray-900">{s.chartName}</dd>
        </div>
        <div>
          <dt className="text-xs uppercase tracking-wide text-gray-400">Version</dt>
          <dd className="mt-1 font-mono text-gray-900">{s.chartVersion || "—"}</dd>
        </div>
        <div>
          <dt className="text-xs uppercase tracking-wide text-gray-400">Schema</dt>
          <dd className="mt-1 text-gray-900">
            {s.hasSchema ? "values.schema.json" : "values.yaml (inferred)"}
          </dd>
        </div>
        <div>
          <dt className="text-xs uppercase tracking-wide text-gray-400">Inputs</dt>
          <dd className="mt-1 text-gray-900">
            {s.inputCount} input{s.inputCount === 1 ? "" : "s"}, {s.mappingCount} mapping
            {s.mappingCount === 1 ? "" : "s"}
          </dd>
        </div>
      </dl>
      {s.description && (
        <p className="mt-4 text-sm leading-relaxed text-gray-600">{s.description}</p>
      )}
      {preview.chartFiles.length > 0 && (
        <details className="mt-5">
          <summary className="cursor-pointer text-xs font-medium text-gray-500 hover:text-gray-700">
            {preview.chartFiles.length} files in archive
          </summary>
          <ul className="mt-2 max-h-48 overflow-y-auto rounded border border-gray-100 bg-gray-50 p-2 text-xs font-mono text-gray-600">
            {preview.chartFiles.map((f) => (
              <li key={f.path} className="flex justify-between gap-3">
                <span className="truncate">{f.path}</span>
                <span className="shrink-0 text-gray-400">{kibibyteFmt(f.size)}</span>
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
