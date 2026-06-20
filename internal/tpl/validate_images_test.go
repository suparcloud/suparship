package tpl

import "testing"

func TestValidateImages(t *testing.T) {
	cases := []struct {
		name    string
		images  []TemplateImage
		wantErr string // substring; "" = expect success
	}{
		{
			name:    "valid single image",
			images:  []TemplateImage{{Name: "web", Repository: "r", TagKey: "image.tag"}},
			wantErr: "",
		},
		{
			name: "valid multi-image with strategy + pattern",
			images: []TemplateImage{
				{Name: "agent", Repository: "r1", TagKey: "agent.image.tag", TagPattern: `^[0-9a-f]{7,40}$`, SelectionStrategy: "NewestBuild"},
				{Name: "caller", Repository: "r2", TagKey: "caller.image.tag"},
			},
			wantErr: "",
		},
		{
			name:    "missing repository",
			images:  []TemplateImage{{Name: "web", TagKey: "image.tag"}},
			wantErr: "repository is required",
		},
		{
			name:    "missing tagKey",
			images:  []TemplateImage{{Name: "web", Repository: "r"}},
			wantErr: "tagKey is required",
		},
		{
			name: "duplicate name",
			images: []TemplateImage{
				{Name: "web", Repository: "r", TagKey: "a"},
				{Name: "web", Repository: "r", TagKey: "b"},
			},
			wantErr: "duplicate image name",
		},
		{
			name:    "bad strategy",
			images:  []TemplateImage{{Name: "web", Repository: "r", TagKey: "image.tag", SelectionStrategy: "Bogus"}},
			wantErr: "unsupported selectionStrategy",
		},
		{
			name:    "bad tag pattern regex",
			images:  []TemplateImage{{Name: "web", Repository: "r", TagKey: "image.tag", TagPattern: "["}},
			wantErr: "invalid tagPattern",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := validTemplate()
			tmpl.Spec.Images = tc.images
			err := tmpl.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			mustContain(t, err, tc.wantErr)
		})
	}
}
