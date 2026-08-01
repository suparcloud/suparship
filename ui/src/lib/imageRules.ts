import type {
  AppImageBinding,
  ComponentImage,
  TemplateImage,
} from "../types";

// Image pull (Kargo CD) rules, keyed by UNIQUE image repository — which is how the
// Warehouse actually works (one subscription per repo). Multiple components can
// share one repo; the rule is defined once here and fanned out to each component's
// per-tagKey binding on save.

// Defaults mirror the backend's gitops.DefaultImageTagPattern / SelectionStrategy:
// a 7-char commit SHA, promote the newest build.
export const DEFAULT_TAG_PATTERN = "^[0-9a-f]{7}$";
export const DEFAULT_SELECTION_STRATEGY = "NewestBuild";
export const SELECTION_STRATEGIES = [
  "NewestBuild",
  "SemVer",
  "Digest",
  "Lexical",
] as const;

// ImageRule is the per-repo pull rule: whether Kargo watches it, and how it picks a tag.
export interface ImageRule {
  watched: boolean;
  tagPattern: string;
  selectionStrategy: string;
}
// ImageRules is keyed by repository.
export type ImageRules = Record<string, ImageRule>;

// RepoGroup aggregates the discovered images that share one repository.
export interface RepoGroup {
  repository: string;
  images: TemplateImage[]; // every discovered image on this repo (its (component, tagKey) fan-out)
  declared: boolean; // the template declares a pull rule for at least one
  // The inherited rule (from a declared image), or platform defaults.
  inherited: { tagPattern: string; selectionStrategy: string };
}

// groupByRepo collapses discovered images into one entry per unique repository,
// preserving discovery order of first appearance. Images with no repository are
// skipped (nothing to watch).
export function groupByRepo(discovered: TemplateImage[]): RepoGroup[] {
  const order: string[] = [];
  const by = new Map<string, RepoGroup>();
  for (const img of discovered) {
    const repo = img.repository?.trim();
    if (!repo) continue;
    let g = by.get(repo);
    if (!g) {
      g = {
        repository: repo,
        images: [],
        declared: false,
        inherited: {
          tagPattern: DEFAULT_TAG_PATTERN,
          selectionStrategy: DEFAULT_SELECTION_STRATEGY,
        },
      };
      by.set(repo, g);
      order.push(repo);
    }
    g.images.push(img);
    if (img.declared) {
      g.declared = true;
      // Inherit the declared slot's rule where it set one.
      if (img.tagPattern) g.inherited.tagPattern = img.tagPattern;
      if (img.selectionStrategy)
        g.inherited.selectionStrategy = img.selectionStrategy;
    }
  }
  return order.map((r) => by.get(r)!);
}

// seedImageRules builds the initial per-repo rules from the discovered images and the
// app's SAVED bindings (by tagKey). When the app has any saved binding, those win
// (watched = repo has a saved tagKey; rule = the saved rule). When there are NO saved
// bindings, default to inherit-from-template: declared repos are watched with their
// inherited rule (mirrors the backend auto-bind); undeclared repos default off.
export function seedImageRules(
  discovered: TemplateImage[],
  savedByTagKey: Record<string, { tagPattern?: string; selectionStrategy?: string }>,
): ImageRules {
  const hasSaved = Object.keys(savedByTagKey).length > 0;
  const out: ImageRules = {};
  for (const g of groupByRepo(discovered)) {
    const savedImg = g.images.find((i) => savedByTagKey[i.tagKey]);
    if (hasSaved) {
      const s = savedImg ? savedByTagKey[savedImg.tagKey] : undefined;
      out[g.repository] = {
        watched: !!savedImg,
        tagPattern: s?.tagPattern || g.inherited.tagPattern,
        selectionStrategy: s?.selectionStrategy || g.inherited.selectionStrategy,
      };
    } else {
      out[g.repository] = {
        watched: g.declared,
        tagPattern: g.inherited.tagPattern,
        selectionStrategy: g.inherited.selectionStrategy,
      };
    }
  }
  return out;
}

// savedByTagKeyFromComponents / ...FromApp flatten stored bindings into a
// tagKey → rule lookup for seedImageRules.
export function savedByTagKeyFromComponents(
  components: { images?: ComponentImage[] }[],
): Record<string, { tagPattern?: string; selectionStrategy?: string }> {
  const out: Record<string, { tagPattern?: string; selectionStrategy?: string }> =
    {};
  for (const c of components) {
    for (const im of c.images ?? []) {
      out[im.tagKey] = {
        tagPattern: im.tagPattern,
        selectionStrategy: im.selectionStrategy,
      };
    }
  }
  return out;
}
export function savedByTagKeyFromApp(
  images: AppImageBinding[] | undefined,
): Record<string, { tagPattern?: string; selectionStrategy?: string }> {
  const out: Record<string, { tagPattern?: string; selectionStrategy?: string }> =
    {};
  for (const im of images ?? []) {
    out[im.tagKey] = {
      tagPattern: im.tagPattern,
      selectionStrategy: im.selectionStrategy,
    };
  }
  return out;
}

// resolvedRuleFn returns a lookup that, for a WATCHED repo, gives its CONCRETE pull
// rule — the user's value, else the template-inherited rule, else the platform
// default — NEVER empty. Persisting the concrete rule makes the saved binding
// self-contained: publish uses it verbatim (no re-derivation, so it never falls back
// to the platform default {7} for an image whose template rule failed to re-annotate
// at publish), and reload shows the stored rule instead of reverting to the template.
// Returns null for an unwatched repo.
function resolvedRuleFn(
  discovered: TemplateImage[],
  rules: ImageRules,
): (repo: string) => { tagPattern: string; selectionStrategy: string } | null {
  const inherited: Record<string, RepoGroup["inherited"]> = {};
  for (const g of groupByRepo(discovered)) inherited[g.repository] = g.inherited;
  return (repo) => {
    const r = rules[repo];
    if (!r?.watched) return null;
    const inh = inherited[repo];
    return {
      tagPattern: r.tagPattern || inh?.tagPattern || DEFAULT_TAG_PATTERN,
      selectionStrategy:
        r.selectionStrategy || inh?.selectionStrategy || DEFAULT_SELECTION_STRATEGY,
    };
  };
}

// imageRulesToComponentImages fans the per-repo rules out to per-component bindings
// (composed apps). Every component in allComponents is present (empty when nothing
// of its is watched) so unchecking clears a prior selection. Only watched repos emit
// bindings.
export function imageRulesToComponentImages(
  discovered: TemplateImage[],
  rules: ImageRules,
  allComponents: string[],
): Record<string, ComponentImage[]> {
  const resolve = resolvedRuleFn(discovered, rules);
  const out: Record<string, ComponentImage[]> = {};
  for (const name of allComponents) out[name] = [];
  for (const img of discovered) {
    const repo = img.repository?.trim();
    if (!repo || !img.component) continue;
    const r = resolve(repo);
    if (!r) continue;
    (out[img.component] ??= []).push({
      name: img.name,
      tagKey: img.tagKey,
      tagPattern: r.tagPattern,
      selectionStrategy: r.selectionStrategy,
    });
  }
  return out;
}

// imageRulesToAppImages fans the per-repo rules out to app-level bindings
// (single-source apps).
export function imageRulesToAppImages(
  discovered: TemplateImage[],
  rules: ImageRules,
): AppImageBinding[] {
  const resolve = resolvedRuleFn(discovered, rules);
  const out: AppImageBinding[] = [];
  const seen = new Set<string>();
  for (const img of discovered) {
    const repo = img.repository?.trim();
    if (!repo) continue;
    const r = resolve(repo);
    if (!r || seen.has(img.tagKey)) continue;
    seen.add(img.tagKey);
    out.push({
      name: img.name,
      tagKey: img.tagKey,
      tagPattern: r.tagPattern,
      selectionStrategy: r.selectionStrategy,
    });
  }
  return out;
}
