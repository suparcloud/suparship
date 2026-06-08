// Package branding controls how the platform stamps its identity on
// resources it writes to the GitOps repo.
//
// SRE contractors who white-label suparship can configure their own
// platform name + label domain in the org config without touching
// individual generators. The two effective getters apply sensible
// defaults so a fresh org doesn't need any explicit configuration.
//
// Lives in its own package (rather than rbac or gitops) so the gitops
// publisher can consume it without taking a dependency on rbac, and so
// the rbac org definition can embed it without pulling in publisher
// concerns.
package branding

import (
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/version"
)

// generatorVersion is stamped onto every platform-emitted manifest so a
// future migration tool (or an SRE auditing the repo) can tell which
// version of the generator produced a given file. Sourced from the version
// package so the manifest contract version has a single definition.
const generatorVersion = version.Generator

const (
	// DefaultName is the platform identity used when Config.Name is
	// empty. Appears as the value of `app.kubernetes.io/managed-by` on
	// every resource the GitOps publisher writes.
	DefaultName = "suparship"
	// DefaultLabelDomain is the label/annotation key prefix for
	// platform-specific metadata (e.g. `<domain>/source`). Mirrors the
	// existing convention of suparship.io/* keys.
	DefaultLabelDomain = "suparship.io"
)

// Config carries the platform identity used by the GitOps generators.
// Both fields are optional; the effective getters apply defaults.
type Config struct {
	// Name is the human-readable platform name embedded in label values
	// and commit metadata. Default: "suparship".
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// LabelDomain is the prefix for platform-specific labels/annotations
	// (e.g. <LabelDomain>/source). Must be a DNS subdomain. Default:
	// "suparship.io".
	LabelDomain string `json:"labelDomain,omitempty" yaml:"labelDomain,omitempty"`
}

// EffectiveName returns the configured platform name, or DefaultName
// when unset.
func (c Config) EffectiveName() string {
	if c.Name == "" {
		return DefaultName
	}
	return c.Name
}

// EffectiveLabelDomain returns the configured label domain, or
// DefaultLabelDomain when unset.
func (c Config) EffectiveLabelDomain() string {
	if c.LabelDomain == "" {
		return DefaultLabelDomain
	}
	return c.LabelDomain
}

// ManagedByLabels returns the standard ownership labels every resource
// the platform writes should carry:
//
//	app.kubernetes.io/managed-by: <name>            (k8s convention)
//	<domain>/generator-version: <version>           (for migrations)
//
// Callers add resource-specific labels (e.g. <domain>/env) on top via
// SourceLabel and MergeLabels. Returns a fresh map per call so callers
// can safely mutate.
func (c Config) ManagedByLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by":                 c.EffectiveName(),
		c.EffectiveLabelDomain() + "/generator-version": generatorVersion,
	}
}

// SourceLabel returns a one-entry map carrying the resource's source-of-
// truth pointer. Stored as a label (not annotation) so operators can
// `kubectl get -l <domain>/source=...` to find resources tied to a given
// platform UI path.
//
// Empty path returns an empty map, so callers can pass through whatever
// they have without nil-checking.
func (c Config) SourceLabel(path string) map[string]string {
	if path == "" {
		return map[string]string{}
	}
	return map[string]string{
		c.EffectiveLabelDomain() + "/source": path,
	}
}

// LabelKey returns "{domain}/{suffix}" — useful for callers that need a
// single label key (e.g. "<domain>/env") without string-builder
// boilerplate.
func (c Config) LabelKey(suffix string) string {
	return c.EffectiveLabelDomain() + "/" + suffix
}

// LabelsYAML serialises a label map as YAML key/value lines indented by
// `indent` spaces. Keys are emitted in sorted order so output is
// deterministic across runs (important for `git diff`-stable manifests).
//
// Used by call sites that build YAML via fmt.Sprintf templates rather
// than a marshalled struct (e.g. the ESO ClusterSecretStore generators
// where the rest of the manifest is a string literal).
//
// Example with indent=4:
//
//	"    app.kubernetes.io/managed-by: suparship\n    suparship.io/generator-version: v0.1.0"
//
// (no trailing newline so the caller controls termination).
func LabelsYAML(labels map[string]string, indent int) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	prefix := ""
	for i := 0; i < indent; i++ {
		prefix += " "
	}
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(prefix)
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(labels[k])
	}
	return b.String()
}

// MergeLabels combines an arbitrary number of label maps into a fresh
// map. Later entries win on key collision. Tiny convenience so call
// sites that need ManagedByLabels + SourceLabel + their own resource-
// specific labels stay readable.
func MergeLabels(maps ...map[string]string) map[string]string {
	total := 0
	for _, m := range maps {
		total += len(m)
	}
	out := make(map[string]string, total)
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
