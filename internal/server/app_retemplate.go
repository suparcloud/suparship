package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/audit"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

// retemplateTargetDTO names the template (and optionally the version) a
// component should move to. An empty version lands on the target template's
// current version — the same rule a brand-new component follows.
type retemplateTargetDTO struct {
	Template string `json:"template"`
	Version  string `json:"version,omitempty"`
}

// retemplatedComponentDTO reports one component's template move.
type retemplatedComponentDTO struct {
	Name         string `json:"name"`
	FromTemplate string `json:"fromTemplate"`
	ToTemplate   string `json:"toTemplate"`
	FromVersion  string `json:"fromVersion,omitempty"`
	ToVersion    string `json:"toVersion"`
}

// retemplateWarningDTO lists the values-overlay keys a moved component (or the
// app, for a component-less app — Name "") carries that the target chart does
// not know. Overlays are chart-shaped, so such keys go silently inert; the
// developer decides whether to rename or drop them.
type retemplateWarningDTO struct {
	Component        string   `json:"component"`
	UnknownValueKeys []string `json:"unknownValueKeys"`
}

// retemplateApp moves components (or a component-less app) onto a DIFFERENT
// template. It is the sibling of the version-only upgrade paths and shares
// their contract: validate every target before mutating, one save + publish,
// full restore on a failed publish. Values overlays are KEPT — a migration is
// not a reset — and the response reports the overlay keys the new chart does
// not define so the developer can fix them; ?dryRun=1 returns that report
// without touching anything.
//
// Two things keyed by the component name are authored against the old chart
// and are reconciled here exactly as the PATCH retemplate path does
// (reconcileRetemplatedComponents): env-scoped version pins are dropped, and
// image bindings survive only when the new template declares their tag path.
func (ah *appHandler) retemplateApp(w http.ResponseWriter, r *http.Request, app *domain.App, req upgradeAppTemplateRequest, dryRun bool) {
	ctx := r.Context()
	projectName, appName := app.ProjectName, app.Name

	if strings.TrimSpace(req.Environment) != "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "a template migration applies to every environment; env-scoped pins are for versions of the same template",
		})
		return
	}
	if !dryRun && ah.gitOpsPublisher == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{
			Error: "gitops publisher not configured — set SUPARSHIP_GITOPS_REPO_URL to enable",
		})
		return
	}

	// ---- component-less app: the app-level pin IS the template ----
	if len(app.Spec.Components) == 0 {
		if len(req.Retemplate) > 0 {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: fmt.Sprintf("app %q has no components; migrate it with {\"template\": ..., \"version\": ...}", appName),
			})
			return
		}
		target := retemplateTargetDTO{Template: strings.TrimSpace(req.Template), Version: strings.TrimSpace(req.Version)}
		tmpl, ok := ah.resolveRetemplateTarget(w, r, target, "")
		if !ok {
			return
		}
		if tmpl.Metadata.Name == app.Spec.Template.Name {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: fmt.Sprintf("app %q already uses template %q — use version to upgrade it", appName, tmpl.Metadata.Name),
			})
			return
		}
		version := target.Version
		if version == "" {
			version = tmpl.Metadata.Version
		}
		if !ah.templateHasVersion(w, r, tmpl.Metadata.Name, version, "") {
			return
		}

		warnings := []retemplateWarningDTO{}
		if known, ok := ah.templateKnownValues(r, tmpl); ok {
			overlays := []map[string]any{app.Spec.RawValues}
			for _, ov := range app.Spec.EnvironmentDefaults {
				overlays = append(overlays, ov.RawValues)
			}
			if keys := unknownValueKeys(overlays, known); len(keys) > 0 {
				warnings = append(warnings, retemplateWarningDTO{Component: "", UnknownValueKeys: keys})
			}
		}
		moved := []retemplatedComponentDTO{{
			Name: "", FromTemplate: app.Spec.Template.Name, ToTemplate: tmpl.Metadata.Name,
			FromVersion: app.Spec.Template.Version, ToVersion: version,
		}}
		if dryRun {
			writeRetemplateResponse(w, projectName, appName, true, moved, warnings)
			return
		}

		prevTemplate := app.Spec.Template
		prevImages := app.Spec.Images
		prevEnvDefaults := snapshotEnvTemplateVersions(app)
		app.Spec.Template = domain.AppTemplateRef{Name: tmpl.Metadata.Name, Version: version}
		clearComponentEnvTemplatePins(&app.Spec, "")
		app.Spec.Images = retargetAppImages(app.Spec.Images, tmpl)

		if !ah.saveAndRepublishUpgrade(w, r, app, func() {
			app.Spec.Template = prevTemplate
			app.Spec.Images = prevImages
			restoreEnvTemplateVersions(app, prevEnvDefaults)
		}) {
			return
		}
		recordAudit(ctx, ah.auditor, "app.retemplate", projectName, appName, audit.ResultSuccess,
			map[string]string{"from": prevTemplate.Name, "to": tmpl.Metadata.Name, "version": version})
		slog.Info("app migrated to another template",
			"project", projectName, "app", appName, "from", prevTemplate.Name, "to", tmpl.Metadata.Name)
		writeRetemplateResponse(w, projectName, appName, false, moved, warnings)
		return
	}

	// ---- composed / single-component app: per-component targets ----
	if len(req.Retemplate) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: fmt.Sprintf("app %q has components; migrate them with {\"retemplate\": {\"<component>\": {\"template\": ...}}}", appName),
		})
		return
	}
	app.Spec.BackfillComponentTemplates()
	byName := map[string]*domain.ComponentSpec{}
	for i := range app.Spec.Components {
		byName[app.Spec.Components[i].Name] = &app.Spec.Components[i]
	}
	type resolvedTarget struct {
		tmpl    *tpl.Template
		version string
	}
	targets := map[string]resolvedTarget{}
	names := make([]string, 0, len(req.Retemplate))
	for name := range req.Retemplate {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		target := req.Retemplate[name]
		c, ok := byName[name]
		if !ok {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: fmt.Sprintf("app %q has no component %q", appName, name),
			})
			return
		}
		if c.Template == nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: fmt.Sprintf("component %q carries no template to migrate", name),
			})
			return
		}
		tmpl, ok := ah.resolveRetemplateTarget(w, r, target, name)
		if !ok {
			return
		}
		if tmpl.Metadata.Name == c.Template.Name {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: fmt.Sprintf("component %q already uses template %q — use components to upgrade its version", name, tmpl.Metadata.Name),
			})
			return
		}
		version := strings.TrimSpace(target.Version)
		if version == "" {
			version = tmpl.Metadata.Version
		}
		if !ah.templateHasVersion(w, r, tmpl.Metadata.Name, version, name) {
			return
		}
		targets[name] = resolvedTarget{tmpl: tmpl, version: version}
	}

	// Warnings and the move list are computed before mutation so a dry run
	// sees exactly what a real run would do.
	var moved []retemplatedComponentDTO
	warnings := []retemplateWarningDTO{}
	for _, name := range names {
		c := byName[name]
		t := targets[name]
		moved = append(moved, retemplatedComponentDTO{
			Name: name, FromTemplate: c.Template.Name, ToTemplate: t.tmpl.Metadata.Name,
			FromVersion: c.Template.Version, ToVersion: t.version,
		})
		if known, ok := ah.templateKnownValues(r, t.tmpl); ok {
			overlays := []map[string]any{c.Values}
			for _, ov := range app.Spec.EnvironmentDefaults {
				overlays = append(overlays, ov.ComponentValues[name])
			}
			if keys := unknownValueKeys(overlays, known); len(keys) > 0 {
				warnings = append(warnings, retemplateWarningDTO{Component: name, UnknownValueKeys: keys})
			}
		}
	}
	if dryRun {
		writeRetemplateResponse(w, projectName, appName, true, moved, warnings)
		return
	}

	// Snapshot for rollback (deep-copy the Template pointers), then apply.
	prevComponents := make([]domain.ComponentSpec, len(app.Spec.Components))
	copy(prevComponents, app.Spec.Components)
	for i := range prevComponents {
		if prevComponents[i].Template != nil {
			t := *prevComponents[i].Template
			prevComponents[i].Template = &t
		}
	}
	prevTemplate := app.Spec.Template
	prevEnvDefaults := snapshotEnvTemplateVersions(app)
	for _, name := range names {
		c := byName[name]
		t := targets[name]
		c.Template = &domain.AppTemplateRef{Name: t.tmpl.Metadata.Name, Version: t.version}
		clearComponentEnvTemplatePins(&app.Spec, name)
		c.Images = retargetComponentImages(c.Images, t.tmpl)
	}
	app.Spec.SyncPrimaryTemplate()

	if !ah.saveAndRepublishUpgrade(w, r, app, func() {
		app.Spec.Components = prevComponents
		app.Spec.Template = prevTemplate
		restoreEnvTemplateVersions(app, prevEnvDefaults)
	}) {
		return
	}
	detail := map[string]string{}
	for _, m := range moved {
		detail[m.Name] = m.FromTemplate + " -> " + m.ToTemplate + "@" + m.ToVersion
	}
	recordAudit(ctx, ah.auditor, "app.retemplate", projectName, appName, audit.ResultSuccess, detail)
	slog.Info("app components migrated to other templates",
		"project", projectName, "app", appName, "components", len(moved))
	writeRetemplateResponse(w, projectName, appName, false, moved, warnings)
}

// resolveRetemplateTarget looks up the target template and refuses retired
// ones — the same gate app creation applies, which the version-only upgrade
// path never needed (it stays on the template the app already uses).
func (ah *appHandler) resolveRetemplateTarget(w http.ResponseWriter, r *http.Request, target retemplateTargetDTO, component string) (*tpl.Template, bool) {
	prefix := ""
	if component != "" {
		prefix = fmt.Sprintf("component %q: ", component)
	}
	name := strings.TrimSpace(target.Template)
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: prefix + "template is required"})
		return nil, false
	}
	tmpl, ok := ah.lookupTemplate(r.Context(), name)
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: fmt.Sprintf("%stemplate %q not found", prefix, name)})
		return nil, false
	}
	if ah.templateDisabled(r.Context(), name) {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: fmt.Sprintf("%stemplate %q is disabled", prefix, name)})
		return nil, false
	}
	return tmpl, true
}

// templateKnownValues returns the key space a template's chart understands:
// its chart defaults overlaid with the template's own default values (and the
// org override's). Reports false when the chart bundle is not readable (no
// cluster client, chart not stored) — then no warning can be computed and
// none is reported, rather than flagging every key.
func (ah *appHandler) templateKnownValues(r *http.Request, t *tpl.Template) (map[string]any, bool) {
	if ah.kubeClient == nil {
		return nil, false
	}
	chartVals, available := chartDefaults(r.Context(), ah.kubeClient, t)
	if !available {
		return nil, false
	}
	ov := loadOverride(r.Context(), ah.kubeClient, t.Metadata.Name)
	return computeEffectiveValues(chartVals, t, ov, "", "", nil, nil), true
}

// unknownValueKeys returns, sorted and de-duplicated, every dotted leaf path
// present in any of the overlays that the known values do not define. A path
// counts as known when walking it through known finds every segment; a
// scalar met before the last segment (overlay "image.tag" vs known "image:
// nginx") makes the rest unknown.
func unknownValueKeys(overlays []map[string]any, known map[string]any) []string {
	seen := map[string]struct{}{}
	var out []string
	var walk func(prefix string, node map[string]any)
	walk = func(prefix string, node map[string]any) {
		for k, v := range node {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if child, ok := v.(map[string]any); ok && len(child) > 0 {
				walk(path, child)
				continue
			}
			if valuesPathKnown(known, strings.Split(path, ".")) {
				continue
			}
			if _, dup := seen[path]; dup {
				continue
			}
			seen[path] = struct{}{}
			out = append(out, path)
		}
	}
	for _, ov := range overlays {
		if len(ov) > 0 {
			walk("", ov)
		}
	}
	sort.Strings(out)
	return out
}

func valuesPathKnown(known map[string]any, segs []string) bool {
	cur := any(known)
	for _, s := range segs {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		next, ok := m[s]
		if !ok {
			return false
		}
		cur = next
	}
	return true
}

// retargetAppImages keeps only the app-level bindings whose slot name the new
// template declares (app-level bindings are keyed by the template's slot
// Name, not a tag path).
func retargetAppImages(images []domain.AppImageBinding, tmpl *tpl.Template) []domain.AppImageBinding {
	if len(images) == 0 || tmpl == nil {
		return nil
	}
	declared := make(map[string]struct{}, len(tmpl.Spec.Images))
	for _, img := range tmpl.Spec.Images {
		declared[img.Name] = struct{}{}
	}
	var kept []domain.AppImageBinding
	for _, img := range images {
		if _, ok := declared[img.Name]; ok {
			kept = append(kept, img)
		}
	}
	return kept
}

func writeRetemplateResponse(w http.ResponseWriter, projectName, appName string, dryRun bool, moved []retemplatedComponentDTO, warnings []retemplateWarningDTO) {
	msg := "app migrated — ArgoCD will sync the new chart shortly"
	if dryRun {
		msg = "dry run — nothing was changed"
	}
	if moved == nil {
		moved = []retemplatedComponentDTO{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message":    msg,
		"project":    projectName,
		"app":        appName,
		"dryRun":     dryRun,
		"components": moved,
		"warnings":   warnings,
	})
}
