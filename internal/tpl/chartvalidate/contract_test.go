package chartvalidate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/chartvalidate"
)

// TestValkey_AddonContractKeys asserts the valkey wrapper template's
// chart renders a Secret containing all four redis-contract keys.
// Catches "renamed REDIS_URL → CACHE_URL" or "forgot REDIS_PASSWORD"
// drift before merge.
func TestValkey_AddonContractKeys(t *testing.T) {
	repoRoot := chartvalidateRepoRoot(t)
	tmplPath := filepath.Join(repoRoot, "templates/valkey/template.yaml")
	chartDir := filepath.Join(repoRoot, "templates/valkey/chart")

	tmpl := loadAddonTemplate(t, tmplPath)
	err := chartvalidate.ValidateAddonContracts(chartDir, tmpl)
	if errors.Is(err, chartvalidate.ErrHelmNotFound) {
		t.Skip("helm binary not available; skipping")
	}
	if err != nil {
		t.Fatalf("ValidateAddonContracts: %v", err)
	}
}

// TestValkey_DriftDetected verifies the validator fails clearly when
// a contract key is renamed. Uses an in-test chart copy.
func TestValkey_DriftDetected(t *testing.T) {
	repoRoot := chartvalidateRepoRoot(t)
	src := filepath.Join(repoRoot, "templates/valkey/chart")

	dst := t.TempDir()
	mustCopyTree(t, src, dst)

	// Rename the REDIS_URL key in the rendered Secret to break the
	// contract.
	secretPath := filepath.Join(dst, "templates/connection-secret.yaml")
	body, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("read secret yaml: %v", err)
	}
	mutated := strings.Replace(string(body), "REDIS_URL:", "CACHE_URL:", 1)
	if err := os.WriteFile(secretPath, []byte(mutated), 0o644); err != nil {
		t.Fatalf("write mutated: %v", err)
	}

	tmpl := loadAddonTemplate(t, filepath.Join(repoRoot, "templates/valkey/template.yaml"))
	err = chartvalidate.ValidateAddonContracts(dst, tmpl)
	if errors.Is(err, chartvalidate.ErrHelmNotFound) {
		t.Skip("helm binary not available; skipping")
	}
	if err == nil {
		t.Fatal("expected validation to fail when REDIS_URL is renamed")
	}
	if !strings.Contains(err.Error(), "REDIS_URL") {
		t.Errorf("error %q should mention the missing key REDIS_URL", err.Error())
	}
}

// --- helpers ---

func loadAddonTemplate(t *testing.T, path string) *tpl.Template {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out tpl.Template
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return &out
}

func chartvalidateRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above starting CWD")
		}
		dir = parent
	}
}

// mustCopyTree mirrors the source chart into dst, sufficient for the
// drift test. Uses filepath.Walk for portability.
func mustCopyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.Walk(src, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, info.Mode())
	}); err != nil {
		t.Fatalf("copy tree: %v", err)
	}
}
