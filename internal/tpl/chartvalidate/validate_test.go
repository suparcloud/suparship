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

// TestBuiltinTemplates_ProducesAllDeclaredKinds renders every built-in
// template chart and asserts each component's `produces` list is
// honoured. Catches the footgun where someone deletes a deployment.yaml
// or renames a chart's components.<name> path without updating
// template.yaml.
func TestBuiltinTemplates_ProducesAllDeclaredKinds(t *testing.T) {
	repoRoot := findRepoRoot(t)
	templatesDir := filepath.Join(repoRoot, "templates")

	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		t.Fatalf("read templates: %v", err)
	}

	// Per-template extra --set args needed to satisfy chart-specific
	// inputs (e.g. voiceai-agent's agent.type / agent.role aren't in
	// the canonical schema).
	extras := map[string]map[string]string{
		"voiceai-agent": {
			"agent.type": "outbound",
			"agent.role": "outbound",
		},
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		tmplPath := filepath.Join(templatesDir, e.Name(), "template.yaml")
		chartDir := filepath.Join(templatesDir, e.Name(), "chart")
		if _, err := os.Stat(tmplPath); err != nil {
			continue
		}
		if _, err := os.Stat(chartDir); err != nil {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			tmpl := loadTemplate(t, tmplPath)
			err := chartvalidate.ValidateProduces(chartDir, tmpl, extras[e.Name()])
			if errors.Is(err, chartvalidate.ErrHelmNotFound) {
				t.Skip("helm binary not available; skipping")
			}
			if err != nil {
				t.Errorf("%s: %v", e.Name(), err)
			}
		})
	}
}

func loadTemplate(t *testing.T, path string) *tpl.Template {
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

func findRepoRoot(t *testing.T) string {
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
			t.Fatalf("go.mod not found above %s", strings.TrimSpace(dir))
		}
		dir = parent
	}
}
