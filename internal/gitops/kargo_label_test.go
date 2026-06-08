package gitops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestKargoManifestLabel verifies app/project pruning matches the stamped
// label exactly, so a hyphen-extended sibling (e.g. "web" vs "web-admin")
// is not wrongly matched by a shared filename prefix.
func TestKargoManifestLabel(t *testing.T) {
	dir := t.TempDir()

	// A Stage CR for app "web-admin" in project "demo".
	webAdmin := filepath.Join(dir, "demo-web-admin-staging-stage.yaml")
	mustWrite(t, webAdmin, `apiVersion: kargo.akuity.io/v1alpha1
kind: Stage
metadata:
  name: web-admin-staging
  namespace: demo
  labels:
    suparship.io/app: web-admin
    suparship.io/project: demo
`)

	// A Warehouse for app "web".
	web := filepath.Join(dir, "demo-web-warehouse.yaml")
	mustWrite(t, web, `apiVersion: kargo.akuity.io/v1alpha1
kind: Warehouse
metadata:
  name: web
  namespace: demo
  labels:
    suparship.io/app: web
    suparship.io/project: demo
`)

	// Deleting app "web" must NOT match web-admin's stage (the old prefix bug).
	if got := kargoManifestLabel(webAdmin, labelApp); got == "web" {
		t.Errorf("web-admin stage matched app 'web' (got label %q) — prefix overmatch", got)
	}
	if got := kargoManifestLabel(webAdmin, labelApp); got != "web-admin" {
		t.Errorf("labelApp = %q, want web-admin", got)
	}
	if got := kargoManifestLabel(web, labelApp); got != "web" {
		t.Errorf("labelApp = %q, want web", got)
	}
	// Both belong to project "demo".
	if kargoManifestLabel(web, labelProject) != "demo" || kargoManifestLabel(webAdmin, labelProject) != "demo" {
		t.Error("both CRs should carry project label 'demo'")
	}
	// Missing/unreadable file → empty (never a spurious match).
	if got := kargoManifestLabel(filepath.Join(dir, "nope.yaml"), labelApp); got != "" {
		t.Errorf("missing file should yield empty label, got %q", got)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
