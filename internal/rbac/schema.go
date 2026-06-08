package rbac

import (
	"fmt"
	"strconv"

	"github.com/suparcloud/suparship/internal/version"
)

// SchemaCheck classifies how a stored org config's schema version relates to
// the running binary's expected version (version.Schema).
type SchemaCheck int

const (
	// SchemaCurrent: stored version matches the binary. Nothing to do.
	SchemaCurrent SchemaCheck = iota
	// SchemaUnversioned: no version recorded (pre-versioning install). The
	// next save stamps the current version; review upgrade notes once.
	SchemaUnversioned
	// SchemaUpgrade: stored version is older than the binary — an upgrade.
	// The operator should review migration notes for the intervening versions.
	SchemaUpgrade
	// SchemaDowngrade: stored version is NEWER than the binary — the config
	// was written by a later release. Running an older binary risks dropping
	// fields it doesn't understand; surface loudly.
	SchemaDowngrade
)

// CheckSchema compares the org's stored SchemaVersion to the binary's expected
// version (version.Schema) and returns the classification plus a human-readable
// message (empty when current). Pure — no I/O — so startup wiring just logs it.
func CheckSchema(org *Org) (SchemaCheck, string) {
	if org == nil {
		return SchemaCurrent, ""
	}
	current := version.Schema
	stored := org.SchemaVersion

	if stored == current {
		return SchemaCurrent, ""
	}
	if stored == "" {
		return SchemaUnversioned, fmt.Sprintf(
			"org config has no schema version; treating as pre-v%s. It will be stamped on the next save. Review docs/upgrading.md.",
			current)
	}

	si, serr := strconv.Atoi(stored)
	ci, cerr := strconv.Atoi(current)
	switch {
	case serr == nil && cerr == nil && si < ci:
		return SchemaUpgrade, fmt.Sprintf(
			"org config schema v%s is older than this binary (v%s) — an upgrade. Review migration notes in docs/upgrading.md before proceeding.",
			stored, current)
	case serr == nil && cerr == nil && si > ci:
		return SchemaDowngrade, fmt.Sprintf(
			"org config schema v%s is NEWER than this binary (v%s). You appear to be running an older suparShip than wrote this config — upgrade the binary to avoid losing config it doesn't understand.",
			stored, current)
	default:
		// Non-integer versions: can't order them, just flag the mismatch.
		return SchemaUpgrade, fmt.Sprintf(
			"org config schema %q differs from this binary's %q. Review docs/upgrading.md.",
			stored, current)
	}
}
