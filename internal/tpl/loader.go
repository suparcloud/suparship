package tpl

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// LoadFile reads and validates a single template from a YAML file.
func LoadFile(path string) (*Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading template %s: %w", path, err)
	}
	return Parse(data)
}

// LoadDir reads all templates from a directory. Each immediate subdirectory
// containing a template.yaml file is loaded and validated. Subdirectories
// without a template.yaml are silently skipped.
//
// The returned slice is sorted by template name for determinism.
func LoadDir(dir string) ([]*Template, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading template directory %s: %w", dir, err)
	}

	var templates []*Template
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		path := filepath.Join(dir, entry.Name(), TemplateFileName)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		}

		tmpl, err := LoadFile(path)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", entry.Name(), err)
		}
		templates = append(templates, tmpl)
	}

	sort.Slice(templates, func(i, j int) bool {
		return templates[i].Metadata.Name < templates[j].Metadata.Name
	})

	return templates, nil
}
