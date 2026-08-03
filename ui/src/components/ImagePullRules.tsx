import type { TemplateImage } from "../types";
import {
  DEFAULT_TAG_PATTERN,
  SELECTION_STRATEGIES,
  groupByRepo,
  type ImageRule,
  type ImageRules,
} from "../lib/imageRules";

// ImagePullRules is the per-UNIQUE-IMAGE CD editor, reused in create / manage /
// detail. It lists one row per unique image repository (how Kargo actually watches —
// one subscription per repo), with a "watched" toggle + tag rule. Declared images
// (the template defines a pull rule) are watched by default and show a "from
// template" hint; undeclared images (sidecars) default off. The parent owns the
// ImageRules state and fans them out to per-component/app bindings on save.
export function ImagePullRules({
  discovered,
  rules,
  onChange,
  loading,
}: {
  discovered: TemplateImage[];
  rules: ImageRules;
  onChange: (next: ImageRules) => void;
  loading?: boolean;
}) {
  const groups = groupByRepo(discovered);

  const set = (repo: string, patch: Partial<ImageRule>) => {
    const cur = rules[repo] ?? {
      watched: false,
      tagPattern: DEFAULT_TAG_PATTERN,
      selectionStrategy: SELECTION_STRATEGIES[0],
    };
    onChange({ ...rules, [repo]: { ...cur, ...patch } });
  };

  if (loading && groups.length === 0) {
    return (
      <p className="text-xs text-gray-400">Detecting images from values…</p>
    );
  }
  if (groups.length === 0) {
    return (
      <p className="rounded-md bg-amber-50 px-3 py-2 text-xs text-amber-700">
        ⚠ No image detected for CD — set a component's{" "}
        <code className="font-mono">image.repository</code> (or add one via the
        template) to enable promotions. Nothing is watched until then.
      </p>
    );
  }

  return (
    <div className="space-y-3">
      {groups.map((g) => {
        const rule = rules[g.repository];
        const watched = rule?.watched ?? g.declared;
        return (
          <div key={g.repository} className="text-xs text-gray-600">
            <label className="flex items-start gap-2">
              <input
                type="checkbox"
                checked={watched}
                onChange={(e) => set(g.repository, { watched: e.target.checked })}
                className="mt-0.5 h-4 w-4 rounded border-gray-300"
              />
              <span className="min-w-0">
                <span className="font-mono font-medium text-gray-700">
                  {g.repository}
                </span>
                {g.declared && (
                  <span className="ml-2 rounded bg-indigo-50 px-1.5 py-0.5 text-[10px] font-medium text-indigo-600">
                    from template
                  </span>
                )}
                {/* Which components/tag keys this one repo feeds. */}
                <span className="mt-0.5 block text-[11px] text-gray-400">
                  {g.images
                    .map((i) =>
                      i.component ? `${i.component} · ${i.tagKey}` : i.tagKey,
                    )
                    .join(",  ")}
                </span>
              </span>
            </label>
            {watched && (
              <div className="mt-2 ml-6 flex flex-wrap items-center gap-x-4 gap-y-2">
                <label className="flex items-center gap-1">
                  <span className="text-gray-500">Tag regex</span>
                  <input
                    type="text"
                    value={rule?.tagPattern ?? g.inherited.tagPattern}
                    onChange={(e) =>
                      set(g.repository, { tagPattern: e.target.value })
                    }
                    placeholder={DEFAULT_TAG_PATTERN}
                    className="w-48 rounded-md border border-gray-300 px-2 py-1 font-mono"
                  />
                </label>
                <label className="flex items-center gap-1">
                  <span className="text-gray-500">Strategy</span>
                  <select
                    value={
                      rule?.selectionStrategy ?? g.inherited.selectionStrategy
                    }
                    onChange={(e) =>
                      set(g.repository, { selectionStrategy: e.target.value })
                    }
                    className="rounded-md border border-gray-300 px-2 py-1"
                  >
                    {SELECTION_STRATEGIES.map((s) => (
                      <option key={s} value={s}>
                        {s}
                      </option>
                    ))}
                  </select>
                </label>
              </div>
            )}
          </div>
        );
      })}
      <p className="text-xs text-gray-400">
        One rule per unique image (Kargo watches one subscription per repository).
        Declared images inherit the template's rule; override above. Leave out
        images you don't want under CD (e.g. sidecars).
      </p>
    </div>
  );
}

export default ImagePullRules;
