package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
)

// MirrorTemplateConfig implements server.TemplateConfigMirrorer: it snapshots
// the cluster's template registry (desired state only) and every template
// override and commits them to the gitops repo under _platform/. Called after
// each desired-state change; an unchanged tree produces no commit.
func (a *gitOpsPublisherAdapter) MirrorTemplateConfig(ctx context.Context) error {
	if a.inner == nil || a.kubeClient == nil {
		return nil
	}
	reg, err := tpl.NewRegistryStore(a.kubeClient).Get(ctx)
	if err != nil && !errors.Is(err, tpl.ErrRegistryNotFound) {
		return fmt.Errorf("read template registry: %w", err)
	}
	overrides, err := kube.ListTemplateOverrides(ctx, a.kubeClient)
	if err != nil {
		return fmt.Errorf("list template overrides: %w", err)
	}
	return a.inner.PublishTemplateConfig(ctx, gitops.TemplateConfigFromRegistry(reg, overrides))
}

// RestoreTemplateConfig reads the _platform/ mirror and recreates any registry
// or override ConfigMap that is MISSING from the cluster. Existing cluster
// objects are never overwritten — the cluster is authoritative; the mirror is
// for recovery. Returns whether the registry was restored and how many
// overrides were.
func (a *gitOpsPublisherAdapter) RestoreTemplateConfig(ctx context.Context) (bool, int, error) {
	if a.inner == nil || a.kubeClient == nil {
		return false, 0, nil
	}
	m, present, err := a.inner.LoadTemplateConfig(ctx)
	if err != nil {
		return false, 0, err
	}
	if !present {
		return false, 0, nil
	}
	return restoreTemplateConfigFromMirror(ctx, a.kubeClient, m)
}

// restoreTemplateConfigFromMirror is the cluster-side half of the restore,
// separated from the git read so it is testable with a fake clientset.
func restoreTemplateConfigFromMirror(ctx context.Context, client kubernetes.Interface, m gitops.TemplateConfigMirror) (restoredRegistry bool, restoredOverrides int, err error) {
	store := tpl.NewRegistryStore(client)
	if _, gerr := store.Get(ctx); errors.Is(gerr, tpl.ErrRegistryNotFound) {
		if len(m.External) > 0 || len(m.BuiltIn) > 0 {
			// Rows are derived: the periodic sync rebuilds sources[] from
			// external[] on its next tick.
			reg := &tpl.TemplateRegistry{
				BuiltIn:  append([]string{}, m.BuiltIn...),
				External: append([]tpl.ExternalTemplateRepo(nil), m.External...),
				Sources:  []tpl.TemplateSource{},
			}
			if serr := store.Save(ctx, reg); serr != nil {
				return false, 0, fmt.Errorf("restore template registry: %w", serr)
			}
			restoredRegistry = true
		}
	} else if gerr != nil {
		return false, 0, fmt.Errorf("read template registry: %w", gerr)
	}
	for name, ov := range m.Overrides {
		if ov == nil || ov.IsEmpty() {
			continue
		}
		existing, lerr := kube.LoadTemplateOverride(ctx, client, name)
		if lerr != nil {
			return restoredRegistry, restoredOverrides, fmt.Errorf("read template override %s: %w", name, lerr)
		}
		if existing != nil {
			continue
		}
		if serr := kube.SaveTemplateOverride(ctx, client, name, ov); serr != nil {
			return restoredRegistry, restoredOverrides, fmt.Errorf("restore template override %s: %w", name, serr)
		}
		restoredOverrides++
	}
	return restoredRegistry, restoredOverrides, nil
}

// restoreThenMirrorTemplateConfig is the startup / hot-reload hook: recover
// anything the cluster lost from the mirror, then write the current cluster
// state back so an install that predates the mirror (or a wiped tree) gets
// populated. Best effort, logged only.
func restoreThenMirrorTemplateConfig(ctx context.Context, a *gitOpsPublisherAdapter, logger *slog.Logger) {
	if a == nil {
		return
	}
	reg, n, err := a.RestoreTemplateConfig(ctx)
	switch {
	case err != nil:
		logger.Warn("template config restore: reading the gitops mirror failed", "error", err)
	case reg || n > 0:
		logger.Info("template config restore: recreated missing cluster objects from the gitops mirror",
			"registry", reg, "overrides", n)
	}
	if err := a.MirrorTemplateConfig(ctx); err != nil {
		logger.Warn("template config mirror: initial write failed", "error", err)
		return
	}
	logger.Info("template config mirror: registry and overrides written to the gitops repo (_platform/)")
}
