// Package version provides build-time and contract version information.
package version

// Build-time variables set via ldflags.
var (
	// Version is the semantic version (e.g., "v0.1.0").
	Version = "dev"
	// Commit is the git commit SHA.
	Commit = "unknown"
	// Date is the build date in RFC3339 format.
	Date = "unknown"
)

// Contract versions are compiled in (not ldflags) — they describe data/manifest
// formats this binary understands, independent of the release tag.
const (
	// Schema is the org-config schema version. It is stamped into the org
	// ConfigMap and compared on startup so an upgrade across a breaking config
	// change is detected and surfaced (see docs/upgrading.md). Bump only when
	// the persisted org config format changes incompatibly. Plain integer
	// string for easy ordering.
	Schema = "1"

	// Generator is the manifest contract version stamped onto every
	// platform-emitted GitOps manifest (the <domain>/generator-version label),
	// so a migration tool or SRE can tell which generator produced a file.
	// Bump when the generated manifest/label contract changes.
	Generator = "v0.1.0"
)
