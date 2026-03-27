package tpl

import (
	"os"
	"path/filepath"
	"testing"
)

const testTemplateA = `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: alpha-service
  version: "1.0.0"
spec:
  title: Alpha Service
  category: web
  engine:
    type: helm
  inputs:
    - name: image
      title: Image
      type: string
      required: true
`

const testTemplateB = `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: beta-worker
  version: "2.0.0"
spec:
  title: Beta Worker
  category: worker
  engine:
    type: helm
`

func writeTemplate(t *testing.T, dir, name, content string) {
	t.Helper()
	tplDir := filepath.Join(dir, name)
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tplDir, TemplateFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "alpha", testTemplateA)

	tmpl, err := LoadFile(filepath.Join(dir, "alpha", TemplateFileName))
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if tmpl.Metadata.Name != "alpha-service" {
		t.Fatalf("expected name %q, got %q", "alpha-service", tmpl.Metadata.Name)
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := LoadFile("/nonexistent/template.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadFileInvalidTemplate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, TemplateFileName)
	if err := os.WriteFile(path, []byte(`apiVersion: bad`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(path)
	if err == nil {
		t.Fatal("expected error for invalid template")
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "beta", testTemplateB)
	writeTemplate(t, dir, "alpha", testTemplateA)

	templates, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(templates) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(templates))
	}
	// Sorted by name: alpha-service < beta-worker
	if templates[0].Metadata.Name != "alpha-service" {
		t.Fatalf("expected first template %q, got %q", "alpha-service", templates[0].Metadata.Name)
	}
	if templates[1].Metadata.Name != "beta-worker" {
		t.Fatalf("expected second template %q, got %q", "beta-worker", templates[1].Metadata.Name)
	}
}

func TestLoadDirSkipsNonDirectories(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "valid", testTemplateA)
	// Create a regular file at the top level — should be skipped.
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	templates, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
}

func TestLoadDirSkipsDirsWithoutTemplate(t *testing.T) {
	dir := t.TempDir()
	writeTemplate(t, dir, "valid", testTemplateA)
	// Create a directory without template.yaml — should be skipped.
	if err := os.MkdirAll(filepath.Join(dir, "empty-dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	templates, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
}

func TestLoadDirEmpty(t *testing.T) {
	dir := t.TempDir()

	templates, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(templates) != 0 {
		t.Fatalf("expected 0 templates, got %d", len(templates))
	}
}

func TestLoadDirInvalidTemplate(t *testing.T) {
	dir := t.TempDir()
	tplDir := filepath.Join(dir, "bad")
	if err := os.MkdirAll(tplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tplDir, TemplateFileName), []byte(`apiVersion: wrong`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("expected error for invalid template in directory")
	}
}

func TestLoadDirNotFound(t *testing.T) {
	_, err := LoadDir("/nonexistent/dir")
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
}
