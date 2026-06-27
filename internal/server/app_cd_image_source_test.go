package server

import (
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

// validateCDImageSource accepts a CD-managed app that has either a selected image
// or a legacy image_repository, and rejects one with neither.
func TestValidateCDImageSource(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]any
		images  []domain.AppImageBinding
		wantErr bool
	}{
		{name: "no source at all", wantErr: true},
		{
			name:   "selected image",
			images: []domain.AppImageBinding{{Name: "web", TagKey: "components.web.image.tag"}},
		},
		{
			name:   "legacy image_repository value",
			values: map[string]any{"image_repository": "ghcr.io/acme/legacy"},
		},
		{
			name:    "blank image_repository is not a source",
			values:  map[string]any{"image_repository": "  "},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCDImageSource(tc.values, tc.images)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
