// Package fetcher defines the contract for resolving a template-source
// declaration into the (template, chart-bytes) pairs the rest of the
// platform consumes.
//
// Three operator-facing source types funnel into the same internal shape:
//
//   - Git templates repo  (gitFetcher, today's registrysync)
//     Walks a templates repo for template.yaml files. ChartBytes is filled
//     when engine.chart is "./chart" (inline mode); left nil when it's a
//     registry ref (external mode — publisher delegates to Argo).
//
//   - Chart registry pull (chartRegistryFetcher, future)
//     Pulls a chart by ref from OCI/ChartMuseum/etc. and hands the .tgz to
//     chartimport.ParseArchive, which already understands a bundled
//     template.yaml and falls back to inferred. Always produces ChartBytes.
//
//   - Upload (uploadFetcher, future wrapping today's chartimport BYO flow)
//     Wraps an operator-uploaded .tgz the same way the registry path does.
//     Always produces ChartBytes.
//
// All three produce ResolvedTemplate; downstream code (registrysync's
// persistence, the kube template store, the gitops publisher) doesn't
// need to know which fetcher ran.
package fetcher

import (
	"context"

	"github.com/suparcloud/suparship/internal/tpl"
)

// ResolvedTemplate is one (template, optional chart bundle) pair.
//
// ChartBytes is non-nil only for inline (engine.chart="./chart"), bundled
// (engine.chart omitted, chart is the artifact this came from), and upload
// modes. For external mode (engine.chart={repository,name,version}) the
// chart bytes never exist on suparship's side — the publisher emits a
// multi-source ArgoCD Application that points Argo's repo-server directly
// at the registry.
type ResolvedTemplate struct {
	Template   *tpl.Template
	ChartBytes []byte
}

// Fetcher resolves a source declaration into zero or more templates.
//
// Implementations are expected to be safe for concurrent use across
// goroutines — callers may fan out across many sources in parallel.
//
// Per-template failures (e.g. one malformed template.yaml in a repo of
// 20) should be surfaced via the Result.Err on the offending entry,
// not returned as a top-level error — partial success matters.
// Top-level error is reserved for catastrophic failures (clone failed,
// registry unreachable, no source path at all).
type Fetcher interface {
	// Fetch resolves the supplied source. The Source type is opaque to
	// the interface; each implementation accepts its own concrete type
	// (gitFetcher takes tpl.ExternalTemplateRepo, chartRegistryFetcher
	// will take a registry-ref descriptor, etc.). Callers wire the right
	// fetcher to the right source.
	Fetch(ctx context.Context, source any) (FetchResult, error)
}

// FetchResult bundles per-template outcomes from one fetch pass. The
// Templates slice contains successfully resolved entries; PartialErrs
// captures per-template failures so callers can surface "3 imported,
// 1 failed" without losing either signal.
type FetchResult struct {
	Templates    []ResolvedTemplate
	PartialErrs  []PartialError
}

// PartialError describes a single-template failure within an otherwise
// successful fetch. The Name may be empty when the failure happened
// before the template's metadata could be parsed (e.g. invalid YAML).
type PartialError struct {
	Name string
	Err  error
}
