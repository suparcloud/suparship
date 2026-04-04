package tpl

import (
	"testing"
)

func TestBuiltinTemplates(t *testing.T) {
	templates, err := LoadDir("../../templates")
	if err != nil {
		t.Fatalf("LoadDir(templates): %v", err)
	}
	if len(templates) == 0 {
		t.Fatal("expected at least one built-in template")
	}

	var foundWebService bool
	for _, tmpl := range templates {
		if tmpl.Metadata.Name == "web-service" {
			foundWebService = true
			assertWebServiceTemplate(t, tmpl)
		}
	}

	if !foundWebService {
		t.Fatal("web-service template not found")
	}
}

func assertWebServiceTemplate(t *testing.T, tmpl *Template) {
	t.Helper()

	if tmpl.Metadata.Version != "1.0.0" {
		t.Errorf("expected version 1.0.0, got %q", tmpl.Metadata.Version)
	}
	if tmpl.Spec.Engine.Type != EngineHelm {
		t.Errorf("expected engine type %q, got %q", EngineHelm, tmpl.Spec.Engine.Type)
	}
	if tmpl.Spec.Category != "web" {
		t.Errorf("expected category %q, got %q", "web", tmpl.Spec.Category)
	}

	inputNames := make(map[string]bool)
	for _, inp := range tmpl.Spec.Inputs {
		inputNames[inp.Name] = true
	}
	for _, inp := range tmpl.Spec.AdvancedInputs {
		inputNames[inp.Name] = true
	}

	for _, required := range []string{"service_name", "image_repository", "port", "size", "health_path"} {
		if !inputNames[required] {
			t.Errorf("expected input %q to be defined", required)
		}
	}

	if len(tmpl.Spec.SecretInputs) == 0 {
		t.Error("expected at least one secret input")
	}
	if len(tmpl.Spec.Presets) < 2 {
		t.Error("expected at least two presets")
	}
	if len(tmpl.Spec.Mappings) == 0 {
		t.Error("expected at least one mapping")
	}

	// Component topology assertions.
	if len(tmpl.Spec.Components) == 0 {
		t.Fatal("expected at least one component in spec.components")
	}
	var webComp *TemplateComponent
	for i := range tmpl.Spec.Components {
		if tmpl.Spec.Components[i].Name == "web" {
			webComp = &tmpl.Spec.Components[i]
			break
		}
	}
	if webComp == nil {
		t.Fatal("expected a component named \"web\" in spec.components")
	}
	if webComp.Type != TemplateComponentWeb {
		t.Errorf("web component type = %q, want %q", webComp.Type, TemplateComponentWeb)
	}
	if !webComp.Required {
		t.Error("web component should be required")
	}
	if !webComp.Exposed {
		t.Error("web component should be exposed")
	}
	if !webComp.PreviewEnabled {
		t.Error("web component should be previewEnabled")
	}
	if !webComp.IsDefaultEnabled() {
		t.Error("web component should be default-enabled")
	}
}
