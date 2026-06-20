package chartimport_test

import (
	"reflect"
	"testing"

	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/chartimport"
)

func TestDetectImageMappings(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]any
		want   []tpl.TemplateImage
	}{
		{
			name: "root image block (voiceai-livekit shape)",
			values: map[string]any{
				"image": map[string]any{"repository": "acr.io/org/livekit", "tag": "seed"},
			},
			want: []tpl.TemplateImage{
				{Name: "image", Repository: "acr.io/org/livekit", TagKey: "image.tag"},
			},
		},
		{
			name: "canonical component image",
			values: map[string]any{
				"components": map[string]any{
					"web": map[string]any{"image": map[string]any{"repository": "acr.io/org/web"}},
				},
			},
			want: []tpl.TemplateImage{
				{Name: "web", Repository: "acr.io/org/web", TagKey: "components.web.image.tag"},
			},
		},
		{
			name: "multiple top-level services (sorted, deterministic)",
			values: map[string]any{
				"caller": map[string]any{"image": map[string]any{"repository": "acr.io/org/caller"}},
				"agent":  map[string]any{"image": map[string]any{"repository": "acr.io/org/agent"}},
			},
			want: []tpl.TemplateImage{
				{Name: "agent", Repository: "acr.io/org/agent", TagKey: "agent.image.tag"},
				{Name: "caller", Repository: "acr.io/org/caller", TagKey: "caller.image.tag"},
			},
		},
		{
			name: "image block without repository is skipped (stays valid)",
			values: map[string]any{
				"image": map[string]any{"tag": "seed"},
			},
			want: nil,
		},
		{
			name:   "no image blocks",
			values: map[string]any{"replicas": 2},
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chartimport.DetectImageMappings(tc.values)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("DetectImageMappings:\n got  %+v\n want %+v", got, tc.want)
			}
		})
	}
}

// TestDetectImageMappings_ProducesValidTemplate guards the contract that every
// emitted mapping satisfies template validation (so ToTemplate never produces an
// invalid spec from detection).
func TestDetectImageMappings_ProducesValidTemplate(t *testing.T) {
	images := chartimport.DetectImageMappings(map[string]any{
		"image": map[string]any{"repository": "acr.io/org/app"},
	})
	tmpl := &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "t", Version: "0.1.0"},
		Spec: tpl.TemplateSpec{
			Title:    "t",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm, Chart: tpl.ChartLocator{Path: "./chart"}},
			Images:   images,
		},
	}
	if err := tmpl.Validate(); err != nil {
		t.Fatalf("template with detected images failed validation: %v", err)
	}
}
