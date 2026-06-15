package domain

import "testing"

func TestTemplateOverride_IsEmpty(t *testing.T) {
	cases := []struct {
		name string
		ov   *TemplateOverride
		want bool
	}{
		{"nil", nil, true},
		{"zero", &TemplateOverride{}, true},
		{"empty maps", &TemplateOverride{
			EnvValues:     map[string]map[string]any{"prod": {}},
			ClusterValues: map[string]map[string]any{"c1": {}},
		}, true},
		{"default set", &TemplateOverride{DefaultValues: map[string]any{"a": 1}}, false},
		{"env set", &TemplateOverride{EnvValues: map[string]map[string]any{"prod": {"a": 1}}}, false},
		{"cluster set", &TemplateOverride{ClusterValues: map[string]map[string]any{"c1": {"a": 1}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ov.IsEmpty(); got != tc.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tc.want)
			}
		})
	}
}
