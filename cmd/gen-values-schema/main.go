// Command gen-values-schema emits the canonical Helm values JSON Schema
// to one or more chart directories.
//
// Run with no flags to write the schema to every templates/<name>/chart/
// directory in the repo. Use --out to target a single file.
//
// The schema is generated from internal/helmvalues/values.go via reflection
// (see helmvalues.Schema). Re-run after every change to that package.
//
// Usage:
//
//	go run ./cmd/gen-values-schema             # write to all templates/*/chart/values.schema.json
//	go run ./cmd/gen-values-schema --out -      # print to stdout
//	go run ./cmd/gen-values-schema --out path   # write to a single file
//	go run ./cmd/gen-values-schema --check      # exit non-zero if any chart's schema is stale
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/suparcloud/suparship/internal/helmvalues"
)

func main() {
	var (
		out        = flag.String("out", "", "output path; '-' for stdout; empty fans out to all chart dirs")
		templates  = flag.String("templates-dir", "", "templates directory to scan when --out is empty (default: <repo-root>/templates)")
		checkOnly  = flag.Bool("check", false, "exit non-zero if any chart's values.schema.json is stale (no writes)")
		schemaName = flag.String("name", "values.schema.json", "filename to write inside each chart directory")
	)
	flag.Parse()

	if *out == "" && *templates == "" {
		dir, err := repoRoot()
		if err != nil {
			fail(fmt.Errorf("locate repo root: %w (use --templates-dir to override)", err))
		}
		*templates = filepath.Join(dir, "templates")
	}

	body, err := renderSchema()
	if err != nil {
		fail(fmt.Errorf("render schema: %w", err))
	}

	switch {
	case *out == "-":
		os.Stdout.Write(body)
	case *out != "":
		if _, err := writeIfChanged(*out, body, *checkOnly); err != nil {
			fail(err)
		}
	default:
		paths, err := chartPaths(*templates, *schemaName)
		if err != nil {
			fail(err)
		}
		stale := []string{}
		for _, p := range paths {
			changed, err := writeIfChanged(p, body, *checkOnly)
			if err != nil {
				fail(err)
			}
			if changed {
				stale = append(stale, p)
			}
		}
		if *checkOnly && len(stale) > 0 {
			fmt.Fprintf(os.Stderr, "stale schemas (run `go generate ./internal/helmvalues/...`):\n")
			for _, p := range stale {
				fmt.Fprintf(os.Stderr, "  - %s\n", p)
			}
			os.Exit(1)
		}
		if !*checkOnly {
			for _, p := range paths {
				fmt.Println("wrote", p)
			}
		}
	}
}

// renderSchema serialises Schema() to indented JSON with a trailing newline.
func renderSchema() ([]byte, error) {
	body, err := json.MarshalIndent(helmvalues.Schema(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// chartPaths returns the values.schema.json target path for every chart
// found under templatesDir/<name>/chart/. Sorted for determinism.
func chartPaths(templatesDir, schemaName string) ([]string, error) {
	entries, err := os.ReadDir(templatesDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", templatesDir, err)
	}
	out := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		chartDir := filepath.Join(templatesDir, e.Name(), "chart")
		if _, err := os.Stat(filepath.Join(chartDir, "Chart.yaml")); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		out = append(out, filepath.Join(chartDir, schemaName))
	}
	sort.Strings(out)
	return out, nil
}

// writeIfChanged writes body to path when the existing content (if any)
// differs. In check mode it never writes. Returns whether the file would
// change.
func writeIfChanged(path string, body []byte, checkOnly bool) (bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if bytes.Equal(existing, body) {
		return false, nil
	}
	if checkOnly {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// repoRoot walks up from cwd until it finds a go.mod file. Lets the
// generator be invoked via `go generate ./...` from any package.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
