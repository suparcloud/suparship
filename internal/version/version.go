// Package version provides build-time version information.
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
