package tpl

import "testing"

func TestValidate_TemplateNameCharset(t *testing.T) {
	base := func(name string) *Template {
		return &Template{
			APIVersion: CurrentAPIVersion, Kind: TemplateKind,
			Metadata: Metadata{Name: name, Version: "1.0.0"},
			Spec:     TemplateSpec{Title: "T", Category: "web", Engine: Engine{Type: EngineHelm}},
		}
	}
	for _, good := range []string{"web", "web-service", "acme.web", "a1.b2-c3"} {
		if err := base(good).Validate(); err != nil {
			t.Errorf("name %q rejected: %v", good, err)
		}
	}
	for _, bad := range []string{"Web", "web_service", ".web", "web.", "acme/web", "web "} {
		if err := base(bad).Validate(); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
}
