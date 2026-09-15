package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

// Template configuration mirror — config as code for the template registry
// and the org-level template overrides.
//
// The cluster stays authoritative (ConfigMaps in suparship-system); every
// desired-state change is ALSO written here so the platform team gets
// history, review and disaster recovery, and a fresh tooling cluster can be
// restored from the repo (see RestoreTemplateConfig in the server adapter).
// Nothing applies these files: _platform/ is deliberately outside _infra/,
// which the root Application syncs recursively as Kubernetes manifests.
//
//	<subpath>/_platform/
//	  template-registry.yaml            # builtIn + external[] — desired state only
//	  template-overrides/<name>.yaml    # one file per template override
//
// The registry file carries NO synced rows (sources[] is derived state the
// periodic sync rebuilds, and its syncedAt would churn a commit every tick)
// and NO credentials (external[].existingSecret is only a Secret name; the
// SealedSecrets stay in the cluster and travel via the config export).
const (
	platformDir              = "_platform"
	templateRegistryFile     = "template-registry.yaml"
	templateOverridesDir     = "template-overrides"
	templateConfigCommitBody = "feat(templates): reconcile template registry and overrides"
)

// TemplateConfigMirror is the desired-state view of template configuration
// that is written to / read from the GitOps repo.
type TemplateConfigMirror struct {
	BuiltIn   []string                            `yaml:"builtIn"`
	External  []tpl.ExternalTemplateRepo          `yaml:"external,omitempty"`
	Overrides map[string]*domain.TemplateOverride `yaml:"-"`
}

// TemplateConfigFromRegistry builds the mirror from live cluster state,
// dropping the synced rows. Empty overrides are skipped (their ConfigMap is
// deleted when emptied, so the file must go too).
func TemplateConfigFromRegistry(reg *tpl.TemplateRegistry, overrides map[string]*domain.TemplateOverride) TemplateConfigMirror {
	m := TemplateConfigMirror{BuiltIn: []string{}, Overrides: map[string]*domain.TemplateOverride{}}
	if reg != nil {
		if reg.BuiltIn != nil {
			m.BuiltIn = append([]string(nil), reg.BuiltIn...)
		}
		m.External = append([]tpl.ExternalTemplateRepo(nil), reg.External...)
	}
	sort.Slice(m.External, func(i, j int) bool { return m.External[i].Name < m.External[j].Name })
	for name, ov := range overrides {
		if ov == nil || ov.IsEmpty() {
			continue
		}
		m.Overrides[name] = ov
	}
	return m
}

// WriteTemplateConfig writes the mirror into repoDir: the registry file and one
// override file per template, pruning override files no longer in the desired
// set (the WriteSecretStores pattern). Ownership convention: _platform/ is
// owned by this writer.
func (p *Publisher) WriteTemplateConfig(repoDir string, m TemplateConfigMirror) error {
	if m.BuiltIn == nil {
		m.BuiltIn = []string{}
	}
	sort.Slice(m.External, func(i, j int) bool { return m.External[i].Name < m.External[j].Name })
	regBytes, err := yaml.Marshal(struct {
		BuiltIn  []string                   `yaml:"builtIn"`
		External []tpl.ExternalTemplateRepo `yaml:"external,omitempty"`
	}{BuiltIn: m.BuiltIn, External: m.External})
	if err != nil {
		return fmt.Errorf("marshal template registry mirror: %w", err)
	}
	if err := p.writeFile(p.outputDir(repoDir, platformDir, templateRegistryFile), regBytes); err != nil {
		return fmt.Errorf("writing template registry mirror: %w", err)
	}

	ovDir := p.outputDir(repoDir, platformDir, templateOverridesDir)
	names := make([]string, 0, len(m.Overrides))
	for name, ov := range m.Overrides {
		if ov == nil || ov.IsEmpty() {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		file := name + ".yaml"
		wanted[file] = true
		b, err := yaml.Marshal(m.Overrides[name])
		if err != nil {
			return fmt.Errorf("marshal template override %s: %w", name, err)
		}
		if err := p.writeFile(filepath.Join(ovDir, file), b); err != nil {
			return fmt.Errorf("writing template override mirror %s: %w", name, err)
		}
	}
	entries, err := os.ReadDir(ovDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s/%s: %w", platformDir, templateOverridesDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") || wanted[name] {
			continue
		}
		if err := os.Remove(filepath.Join(ovDir, name)); err != nil {
			return fmt.Errorf("prune stale template override mirror %s: %w", name, err)
		}
	}
	return nil
}

// ReadTemplateConfig parses the mirror back from repoDir. present is false
// when the tree has never been written (no registry file).
func (p *Publisher) ReadTemplateConfig(repoDir string) (m TemplateConfigMirror, present bool, err error) {
	m = TemplateConfigMirror{BuiltIn: []string{}, Overrides: map[string]*domain.TemplateOverride{}}
	regBytes, err := os.ReadFile(p.outputDir(repoDir, platformDir, templateRegistryFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, false, nil
		}
		return m, false, fmt.Errorf("read template registry mirror: %w", err)
	}
	var reg struct {
		BuiltIn  []string                   `yaml:"builtIn"`
		External []tpl.ExternalTemplateRepo `yaml:"external"`
	}
	if err := yaml.Unmarshal(regBytes, &reg); err != nil {
		return m, true, fmt.Errorf("parse template registry mirror: %w", err)
	}
	if reg.BuiltIn != nil {
		m.BuiltIn = reg.BuiltIn
	}
	m.External = reg.External

	ovDir := p.outputDir(repoDir, platformDir, templateOverridesDir)
	entries, err := os.ReadDir(ovDir)
	if err != nil {
		if os.IsNotExist(err) {
			return m, true, nil
		}
		return m, true, fmt.Errorf("read template override mirrors: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ovDir, e.Name()))
		if err != nil {
			return m, true, fmt.Errorf("read template override mirror %s: %w", e.Name(), err)
		}
		var ov domain.TemplateOverride
		if err := yaml.Unmarshal(b, &ov); err != nil {
			return m, true, fmt.Errorf("parse template override mirror %s: %w", e.Name(), err)
		}
		m.Overrides[strings.TrimSuffix(e.Name(), ".yaml")] = &ov
	}
	return m, true, nil
}

// PublishTemplateConfig writes the mirror and commits. Idempotent: an
// unchanged tree produces no commit.
func (p *Publisher) PublishTemplateConfig(ctx context.Context, m TemplateConfigMirror) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		if err := p.WriteTemplateConfig(repoDir, m); err != nil {
			return err
		}
		return p.commitAndPush(ctx, repoDir, templateConfigCommitBody)
	})
}

// LoadTemplateConfig reads the mirror from the repo without committing.
func (p *Publisher) LoadTemplateConfig(ctx context.Context) (m TemplateConfigMirror, present bool, err error) {
	err = p.withClonedRepo(ctx, func(repoDir string) error {
		var rerr error
		m, present, rerr = p.ReadTemplateConfig(repoDir)
		return rerr
	})
	return m, present, err
}
