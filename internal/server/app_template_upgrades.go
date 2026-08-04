package server

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/suparcloud/suparship/internal/kube"
)

// templateVersionsTTL bounds how stale the per-template archive listing served on
// the app detail page may be. Templates are published by an operator or a
// registry sync (default 5m), so a short cache costs nothing in freshness while
// collapsing the repeated ConfigMap LISTs an app-detail render would otherwise do
// once per component.
const templateVersionsTTL = 30 * time.Second

// templateVersionCache memoizes ListTemplateVersions by template name. A miss is
// cheap (one labelled ConfigMap LIST) and errors are never cached, so a transient
// cluster blip self-heals on the next request.
type templateVersionCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	entries map[string]templateVersionCacheEntry
}

type templateVersionCacheEntry struct {
	versions  []TemplateVersionDTO
	expiresAt time.Time
}

func newTemplateVersionCache(ttl time.Duration) *templateVersionCache {
	return &templateVersionCache{ttl: ttl, entries: map[string]templateVersionCacheEntry{}}
}

func (c *templateVersionCache) get(name string) ([]TemplateVersionDTO, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	e, ok := c.entries[name]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.versions, true
}

func (c *templateVersionCache) put(name string, versions []TemplateVersionDTO) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries[name] = templateVersionCacheEntry{versions: versions, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
}

// templateVersions returns a template's archived versions newest-first, cached.
// Returns nil for a template with no archives — built-ins loaded from disk have
// none, and that must read as "not version-managed", not "upgrade to nothing".
func (ah *appHandler) templateVersions(ctx context.Context, name string) []TemplateVersionDTO {
	if ah.kubeClient == nil || name == "" {
		return nil
	}
	if v, ok := ah.templateVersionCache.get(name); ok {
		return v
	}
	versions, err := kube.ListTemplateVersions(ctx, ah.kubeClient, name)
	if err != nil {
		// Degrade silently: an upgrade hint is an affordance, not data the page
		// depends on. Don't cache — retry on the next render.
		slog.Warn("template versions lookup failed", "template", name, "err", err)
		return nil
	}
	sort.Slice(versions, func(i, j int) bool {
		return semverGreater(versions[i].Version, versions[j].Version)
	})
	if len(versions) > maxTemplateVersionsServed {
		versions = versions[:maxTemplateVersionsServed]
	}
	out := make([]TemplateVersionDTO, 0, len(versions))
	for _, v := range versions {
		out = append(out, TemplateVersionDTO{Version: v.Version, CreatedAt: v.CreatedAt})
	}
	ah.templateVersionCache.put(name, out)
	return out
}

// maxTemplateVersionsServed caps the per-template version list embedded in the
// app detail response. The picker only ever needs recent history, and an
// operator who has published hundreds of versions shouldn't bloat every render.
const maxTemplateVersionsServed = 20

// decorateTemplateUpgrades fills in the upgrade-availability fields on an app
// detail DTO: per component (its own template's newest archive) and rolled up at
// app level.
//
// It is deliberately detail-only. The list view renders many apps per request,
// and even a cached lookup per distinct template would turn one page into a
// fan-out of cluster reads for an affordance nobody can act on from a list row.
//
// A template with no archives yields empty LatestVersion and UpgradeAvailable
// false, which the UI reads as "not version-managed" and renders nothing —
// matching how a built-in template behaves today.
func (ah *appHandler) decorateTemplateUpgrades(ctx context.Context, detail *AppDetailDTO) {
	if ah == nil || ah.kubeClient == nil || detail == nil {
		return
	}
	byTemplate := map[string][]TemplateVersionDTO{}
	for i := range detail.Components {
		c := &detail.Components[i]
		if c.Template == "" {
			continue
		}
		versions, seen := byTemplate[c.Template]
		if !seen {
			versions = ah.templateVersions(ctx, c.Template)
			byTemplate[c.Template] = versions
		}
		if len(versions) == 0 {
			continue
		}
		c.LatestVersion = versions[0].Version
		// A component with no pin tracks latest already, so it is never "behind".
		if c.TemplateVersion != "" && semverGreater(c.LatestVersion, c.TemplateVersion) {
			c.UpgradeAvailable = true
			detail.UpgradesAvailable++
		}
	}
	// Drop templates that turned out to have no archives so the map only carries
	// pickable versions.
	for name, versions := range byTemplate {
		if len(versions) == 0 {
			delete(byTemplate, name)
		}
	}
	if len(byTemplate) > 0 {
		detail.TemplateVersions = byTemplate
	}
	if versions := byTemplate[detail.Template.Name]; len(versions) > 0 {
		detail.TemplateLatestVersion = versions[0].Version
	}
}
