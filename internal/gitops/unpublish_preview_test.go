package gitops_test

import (
	"os"
	"path/filepath"
	"testing"
)

// Unpublishing an app must also remove its PREVIEW trees across every open preview
// name. This is what was missing when per-component apps (lk-sh-web, …) were
// consolidated into a composed app (voiceai-lk-sh): the old apps' single-source
// preview folders lingered under each PR and kept rendering as phantom Applications.
func TestUnpublishAppFiles_PrunesPreviewTreesAllPRs(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)

	mk := func(parts ...string) {
		full := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Stale single-source preview trees for lk-sh-web under TWO open PRs + the
	// platform-resources tree.
	for _, pr := range []string{"pr-724", "pr-712"} {
		mk("previews", "staging", "voiceai", pr, "lk-sh-web", "app.yaml")
		mk("previews", "staging", "voiceai", pr, "lk-sh-web", "values.yaml")
		mk("_app-resources", "previews", "staging", "voiceai", pr, "lk-sh-web", "cm.yaml")
	}
	// A composed preview manifest tree for the same app (defensive).
	mk("_composed-apps", "_previews", "staging", "voiceai", "pr-724", "lk-sh-web", "application.yaml")
	// A SIBLING app's preview that must SURVIVE (same PR, different app).
	mk("previews", "staging", "voiceai", "pr-724", "keep-me", "app.yaml")
	// A different PROJECT's app named the same must SURVIVE.
	mk("previews", "staging", "other", "pr-724", "lk-sh-web", "app.yaml")

	removed, err := p.UnpublishAppFilesForTest(dir, "voiceai", "lk-sh-web")
	if err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if !removed {
		t.Fatal("expected removed=true")
	}

	gone := func(parts ...string) {
		if _, err := os.Stat(filepath.Join(append([]string{dir}, parts...)...)); !os.IsNotExist(err) {
			t.Errorf("expected %v pruned, err=%v", filepath.Join(parts...), err)
		}
	}
	stays := func(parts ...string) {
		if _, err := os.Stat(filepath.Join(append([]string{dir}, parts...)...)); err != nil {
			t.Errorf("expected %v to survive: %v", filepath.Join(parts...), err)
		}
	}

	for _, pr := range []string{"pr-724", "pr-712"} {
		gone("previews", "staging", "voiceai", pr, "lk-sh-web")
		gone("_app-resources", "previews", "staging", "voiceai", pr, "lk-sh-web")
	}
	gone("_composed-apps", "_previews", "staging", "voiceai", "pr-724", "lk-sh-web")
	stays("previews", "staging", "voiceai", "pr-724", "keep-me", "app.yaml")
	stays("previews", "staging", "other", "pr-724", "lk-sh-web", "app.yaml")
}
