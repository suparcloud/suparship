package server

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
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
