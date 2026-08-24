package gitops

import (
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

func indexByTagKey(imgs []KargoImage) map[string]KargoImage {
	m := make(map[string]KargoImage, len(imgs))
	for _, img := range imgs {
		m[img.TagKey] = img
	}
	return m
}

// SelectKargoImages keeps only the selected images, reading repo + tagKey from
// the discovered set — the core "exclude the sidecar" behaviour.
func TestSelectKargoImages_ExcludesUnselected(t *testing.T) {
	discovered := []tpl.TemplateImage{
		{Name: "web", Repository: "ghcr.io/acme/web", TagKey: "components.web.image.tag"},
		{Name: "sidecar", Repository: "ghcr.io/acme/proxy", TagKey: "components.sidecar.image.tag"},
	}
	selection := []domain.AppImageBinding{{Name: "web", TagKey: "components.web.image.tag"}}

	got := indexByTagKey(SelectKargoImages(discovered, selection))
	if len(got) != 1 {
		t.Fatalf("expected only the selected image, got %d: %+v", len(got), got)
	}
	web, ok := got["components.web.image.tag"]
	if !ok || web.Repository != "ghcr.io/acme/web" {
		t.Errorf("web image wrong: %+v", got)
	}
	// A selection that doesn't override gets the platform defaults.
	if web.TagPattern != DefaultImageTagPattern || web.SelectionStrategy != DefaultImageSelectionStrategy {
		t.Errorf("defaults not applied: pattern=%q strategy=%q", web.TagPattern, web.SelectionStrategy)
	}
	if _, ok := got["components.sidecar.image.tag"]; ok {
		t.Error("sidecar image must be excluded — it wasn't selected")
	}
}

// A selection override (pattern/strategy) wins over the discovered defaults; repo
// and tagKey still come from discovery.
func TestSelectKargoImages_SelectionOverrides(t *testing.T) {
	discovered := []tpl.TemplateImage{{
		Name: "web", Repository: "ghcr.io/acme/web", TagKey: "image.tag",
		TagPattern: "^v", SelectionStrategy: "SemVer",
	}}
	selection := []domain.AppImageBinding{{
		Name: "web", TagKey: "image.tag", TagPattern: "^sha-", SelectionStrategy: "NewestBuild",
	}}
	got := SelectKargoImages(discovered, selection)
	if len(got) != 1 {
		t.Fatalf("expected 1, got %d", len(got))
	}
	if got[0].Repository != "ghcr.io/acme/web" || got[0].TagKey != "image.tag" {
		t.Errorf("repo/tagKey should come from discovery: %+v", got[0])
	}
	if got[0].TagPattern != "^sha-" || got[0].SelectionStrategy != "NewestBuild" {
		t.Errorf("selection overrides not applied: %+v", got[0])
	}
}

// A selected image that no longer appears in the discovered values is skipped
// (rather than emitting a Warehouse subscription that never resolves).
func TestSelectKargoImages_SkipsMissing(t *testing.T) {
	discovered := []tpl.TemplateImage{{Name: "web", Repository: "ghcr.io/acme/web", TagKey: "image.tag"}}
	selection := []domain.AppImageBinding{{Name: "gone", TagKey: "components.gone.image.tag"}}
	if got := SelectKargoImages(discovered, selection); len(got) != 0 {
		t.Fatalf("expected missing selection to be skipped, got %+v", got)
	}
}

// Empty selection → nil (publisher then applies the legacy fallback).
func TestSelectKargoImages_EmptySelection(t *testing.T) {
	discovered := []tpl.TemplateImage{{Name: "web", Repository: "r", TagKey: "image.tag"}}
	if got := SelectKargoImages(discovered, nil); got != nil {
		t.Fatalf("expected nil for empty selection, got %+v", got)
	}
}
