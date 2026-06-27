package gitops_test

import (
	"os"
	"path/filepath"
	"testing"
)

// removeAppEnvFiles must delete only the target env's trees for the target app,
// leaving other envs of the same app and other apps in the same env intact.
func TestRemoveAppEnvFiles(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)

	mk := func(parts ...string) string {
		full := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return filepath.Dir(full)
	}

	stagingValkey := mk("envs", "staging", "demo", "valkey", "app.yaml")
	prodValkey := mk("envs", "prod", "demo", "valkey", "app.yaml")
	prodValkeyRes := mk("_app-resources", "prod", "demo", "valkey", "cm.yaml")
	prodAPI := mk("envs", "prod", "demo", "api", "app.yaml") // sibling app, same env

	removed, err := p.RemoveAppEnvFilesForTest(dir, "demo", "valkey", "prod")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !removed {
		t.Fatal("expected removed=true")
	}

	gone := func(d string) {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("expected %q removed, stat err = %v", d, err)
		}
	}
	intact := func(d string) {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("expected %q intact: %v", d, err)
		}
	}

	gone(prodValkey)
	gone(prodValkeyRes)
	intact(stagingValkey) // other env of the same app survives
	intact(prodAPI)       // sibling app in the same env survives

	// No-op when nothing to remove.
	removed, err = p.RemoveAppEnvFilesForTest(dir, "demo", "valkey", "prod")
	if err != nil || removed {
		t.Fatalf("second remove should be a no-op: removed=%v err=%v", removed, err)
	}
}
