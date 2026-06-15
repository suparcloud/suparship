import { useCallback, useMemo, useRef } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { yaml } from "@codemirror/lang-yaml";
import { linter, lintGutter, type Diagnostic } from "@codemirror/lint";
import type { EditorView } from "@codemirror/view";
import { parse } from "yaml";

import type { ConfigVariables } from "../lib/configVars";
import { parseYamlOverlay } from "../lib/yamlDoc";
import { VariablePicker } from "./VariablePicker";

interface ValuesEditorProps {
  value: string;
  onChange?: (text: string) => void;
  /** When provided (and not readOnly), shows a VariablePicker in the header. */
  configVars?: ConfigVariables | null;
  readOnly?: boolean;
  placeholder?: string;
  /** Editor height (CSS), e.g. "16rem". Defaults to "18rem". */
  height?: string;
  /**
   * Called on every change with the parsed overlay (null when invalid) and an
   * error message (null when valid). Empty text is valid → {}.
   */
  onValidChange?: (
    parsed: Record<string, unknown> | null,
    error: string | null,
  ) => void;
  /** Header label. Omit to hide the header entirely. */
  label?: string;
  /** Extra header controls (e.g. a "Load chart defaults" button). */
  toolbar?: React.ReactNode;
}

// yamlLinter parses the buffer with the `yaml` package and surfaces parse errors
// as CodeMirror diagnostics positioned at the offending range when known.
const yamlLinter = linter((view): Diagnostic[] => {
  const text = view.state.doc.toString();
  if (text.trim() === "") return [];
  try {
    parse(text);
    return [];
  } catch (e: unknown) {
    const err = e as { message?: string; pos?: [number, number] };
    const len = view.state.doc.length;
    const from = err.pos?.[0] ?? 0;
    const to = err.pos?.[1] ?? len;
    return [
      {
        from: Math.min(from, len),
        to: Math.min(Math.max(to, from + 1), len),
        severity: "error",
        message: err.message ?? "Invalid YAML",
      },
    ];
  }
});

// ValuesEditor is a CodeMirror-backed YAML editor for Helm values overlays. The
// developer edits ONLY their override layer; a sibling read-only instance is used
// for the effective-values preview. VariablePicker inserts {platform.*}/{vars.*}
// tokens at the cursor via a CodeMirror transaction (no <textarea> to target).
export function ValuesEditor({
  value,
  onChange,
  configVars,
  readOnly = false,
  placeholder,
  height = "18rem",
  onValidChange,
  label,
  toolbar,
}: ValuesEditorProps) {
  const viewRef = useRef<EditorView | null>(null);

  const extensions = useMemo(
    () => (readOnly ? [yaml()] : [yaml(), lintGutter(), yamlLinter]),
    [readOnly],
  );

  const handleChange = useCallback(
    (text: string) => {
      onChange?.(text);
      if (onValidChange) {
        const { value: parsed, error } = parseYamlOverlay(text);
        onValidChange(parsed, error);
      }
    },
    [onChange, onValidChange],
  );

  const insertToken = useCallback((token: string) => {
    const view = viewRef.current;
    if (!view) return;
    view.dispatch(view.state.replaceSelection(token));
    view.focus();
  }, []);

  return (
    <div>
      {(label || (configVars && !readOnly) || toolbar) && (
        <div className="mb-2 flex items-center justify-between gap-3">
          <span className="text-xs font-medium uppercase tracking-wide text-gray-500">
            {label}
          </span>
          <div className="flex items-center gap-2">
            {toolbar}
            {configVars && !readOnly && (
              <VariablePicker variables={configVars} onInsert={insertToken} />
            )}
          </div>
        </div>
      )}
      <div className="overflow-hidden rounded-lg border border-gray-300 focus-within:border-indigo-500 focus-within:ring-1 focus-within:ring-indigo-500">
        <CodeMirror
          value={value}
          height={height}
          editable={!readOnly}
          readOnly={readOnly}
          extensions={extensions}
          placeholder={placeholder}
          onChange={handleChange}
          onCreateEditor={(view) => {
            viewRef.current = view;
          }}
          basicSetup={{
            lineNumbers: true,
            foldGutter: true,
            highlightActiveLine: !readOnly,
            autocompletion: false,
          }}
        />
      </div>
    </div>
  );
}

export default ValuesEditor;
