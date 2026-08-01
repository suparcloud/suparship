package server

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

// DiscoverAppImages finds image blocks in the app's effective values. With no
// kube client (no chart defaults) it still discovers images set in the app's
// rawValues — the values-editor-first path.
func TestDiscoverAppImages_FromRawValues(t *testing.T) {
	app := &domain.App{
		Name:        "web",
		ProjectName: "proj",
		Spec: domain.AppSpec{
			RawValues: map[string]any{
				"image": map[string]any{"repository": "ghcr.io/acme/web", "tag": "v1"},
			},
		},
	}

	got := DiscoverAppImages(context.Background(), nil, nil, nil, app,
		"staging", domain.AppEnvStaging, "proj-web-staging", "org",
		app.Spec.RawValues, nil)

	var found bool
	for _, img := range got {
		if img.TagKey == "image.tag" {
			found = true
			if img.Repository != "ghcr.io/acme/web" {
				t.Errorf("repository = %q, want ghcr.io/acme/web", img.Repository)
			}
		}
	}
	if !found {
		t.Fatalf("expected to discover the root image block, got %+v", got)
	}
}

// An image block without a repository is not discovered (DetectImageMappings rule),
// so it can't be selected for CD.
func TestDiscoverAppImages_SkipsRepolessImage(t *testing.T) {
	app := &domain.App{
		Name:        "web",
		ProjectName: "proj",
		Spec: domain.AppSpec{
			RawValues: map[string]any{
				"image": map[string]any{"tag": "v1"}, // no repository
			},
		},
	}
	got := DiscoverAppImages(context.Background(), nil, nil, nil, app,
		"staging", domain.AppEnvStaging, "proj-web-staging", "org",
		app.Spec.RawValues, nil)
	for _, img := range got {
		if img.TagKey == "image.tag" {
			t.Fatalf("repository-less image must not be discovered: %+v", img)
		}
	}
}

// DiscoverComponentImages finds the image a composed component sets in its OWN
// Values overlay (canonical layout components.<key>.image), returning the fully
// qualified tagKey the promotion writes into — proving a component image is
// auto-identified from values, not hand-typed.
func TestDiscoverComponentImages_FromOverlay(t *testing.T) {
	overlay := map[string]any{
		"components": map[string]any{
			"web": map[string]any{
				"image": map[string]any{
					"repository": "acr.azurecr.io/telephony-frontend",
					"tag":        "abc1234",
				},
			},
		},
	}
	got := DiscoverComponentImages(context.Background(), nil, &tpl.Template{}, nil, "staging", overlay, nil)

	var found bool
	for _, img := range got {
		if img.TagKey == "components.web.image.tag" {
			found = true
			if img.Repository != "acr.azurecr.io/telephony-frontend" {
				t.Errorf("repository = %q, want acr.azurecr.io/telephony-frontend", img.Repository)
			}
		}
	}
	if !found {
		t.Fatalf("expected to discover components.web.image, got %+v", got)
	}
}

// A component whose template declares no image slot and whose BASE overlay leaves
// image.repository empty must still have its image discovered when the repository is
// set only in the component's PER-ENV override (envOverlay) — otherwise the image is
// dropped by DetectImageMappings and never wired to Kargo. Regression for a `web`
// component pointing image at its own repo via its staging values.
func TestDiscoverComponentImages_FromPerEnvOverlay(t *testing.T) {
	base := map[string]any{
		"components": map[string]any{
			"web": map[string]any{
				"image": map[string]any{"tag": "abc1234"},
			},
		},
	}
	envOverlay := map[string]any{
		"components": map[string]any{
			"web": map[string]any{
				"image": map[string]any{
					"repository": "acr.azurecr.io/biglysales-voiceai-livekit",
				},
			},
		},
	}
	got := DiscoverComponentImages(context.Background(), nil, &tpl.Template{}, nil, "staging", base, envOverlay)

	var found bool
	for _, img := range got {
		if img.TagKey == "components.web.image.tag" {
			found = true
			if img.Repository != "acr.azurecr.io/biglysales-voiceai-livekit" {
				t.Errorf("repository = %q, want acr.azurecr.io/biglysales-voiceai-livekit", img.Repository)
			}
		}
	}
	if !found {
		t.Fatalf("per-env image repository must be discovered, got %+v", got)
	}
}
