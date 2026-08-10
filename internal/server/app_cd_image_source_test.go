package server

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
)

// validateCDImageSource must accept every source the publisher actually
// watches — app selection, legacy image_repository, COMPONENT selections
// (composed apps keep images per component; checking only the app level
// rejected every composed app), and template-declared images while the
// selection was never explicitly configured (publish auto-binds them) — and
// reject an app with none of those.
func TestValidateCDImageSource(t *testing.T) {
	imageTemplate := &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "with-images", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:  "With Images",
			Engine: tpl.Engine{Type: tpl.EngineHelm},
			Images: []tpl.TemplateImage{{Name: "web", TagKey: "components.web.image.tag"}},
		},
	}
	bareTemplate := &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "bare", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:  "Bare",
			Engine: tpl.Engine{Type: tpl.EngineHelm},
		},
	}
	ah := newAppHandler(newMemAppStore(), []*tpl.Template{imageTemplate, bareTemplate}, nil, nil)

	tests := []struct {
		name             string
		spec             domain.AppSpec
		imagesConfigured bool
		wantErr          bool
	}{
		{name: "no source at all", wantErr: true},
		{
			name: "selected app image",
			spec: domain.AppSpec{Images: []domain.AppImageBinding{{Name: "web", TagKey: "components.web.image.tag"}}},
		},
		{
			name: "legacy image_repository value",
			spec: domain.AppSpec{Values: map[string]any{"image_repository": "ghcr.io/acme/legacy"}},
		},
		{
			name:    "blank image_repository is not a source",
			spec:    domain.AppSpec{Values: map[string]any{"image_repository": "  "}},
			wantErr: true,
		},
		{
			name: "composed: a component's stored selection counts",
			spec: domain.AppSpec{Components: []domain.ComponentSpec{
				{Name: "frontend"},
				{Name: "api", Images: []domain.ComponentImage{{Name: "api", TagKey: "image.tag"}}},
			}},
			imagesConfigured: true,
		},
		{
			name: "composed: never-configured auto-binds a component template's images",
			spec: domain.AppSpec{Components: []domain.ComponentSpec{
				{Name: "web", Template: &domain.AppTemplateRef{Name: "with-images"}},
			}},
		},
		{
			name: "never-configured auto-binds the app template's images",
			spec: domain.AppSpec{Template: domain.AppTemplateRef{Name: "with-images"}},
		},
		{
			name: "explicitly configured empty selection means watch nothing — template images stop counting",
			spec: domain.AppSpec{Components: []domain.ComponentSpec{
				{Name: "web", Template: &domain.AppTemplateRef{Name: "with-images"}},
			}},
			imagesConfigured: true,
			wantErr:          true,
		},
		{
			name: "composed: template without images is not a source",
			spec: domain.AppSpec{Components: []domain.ComponentSpec{
				{Name: "web", Template: &domain.AppTemplateRef{Name: "bare"}},
			}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app := &domain.App{Name: "x", ProjectName: "p", Spec: tc.spec}
			err := ah.validateCDImageSource(context.Background(), app, tc.imagesConfigured)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
