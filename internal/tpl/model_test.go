package tpl

import (
	"strings"
	"testing"
)

const validTemplateYAML = `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: web-service
  version: "1.0.0"
spec:
  title: Web Service
  description: Deploy a containerized web service
  category: web
  engine:
    type: helm
    chart: ./chart
  inputs:
    - name: image
      title: Container Image
      type: string
      required: true
    - name: replicas
      title: Replicas
      type: number
      default: 2
      min: 1
      max: 10
    - name: public
      title: Public Access
      type: boolean
      default: false
    - name: tier
      title: Service Tier
      type: enum
      options: [small, medium, large]
      default: small
  advancedInputs:
    - name: cpu_limit
      title: CPU Limit
      type: string
      default: "500m"
  secretInputs:
    - name: database_url
      title: Database URL
      secretRef: db-credentials.url
  mappings:
    image: "{{ .inputs.image }}"
    replicaCount: "{{ .inputs.replicas }}"
  presets:
    - name: starter
      title: Starter
      description: Minimal resources
      values:
        replicas: 1
        tier: small
    - name: production
      title: Production
      values:
        replicas: 3
        tier: large
`

func TestParseValidTemplate(t *testing.T) {
	tmpl, err := Parse([]byte(validTemplateYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if tmpl.Metadata.Name != "web-service" {
		t.Fatalf("expected name %q, got %q", "web-service", tmpl.Metadata.Name)
	}
	if tmpl.Metadata.Version != "1.0.0" {
		t.Fatalf("expected version %q, got %q", "1.0.0", tmpl.Metadata.Version)
	}
	if tmpl.Spec.Title != "Web Service" {
		t.Fatalf("expected title %q, got %q", "Web Service", tmpl.Spec.Title)
	}
	if tmpl.Spec.Engine.Type != EngineHelm {
		t.Fatalf("expected engine type %q, got %q", EngineHelm, tmpl.Spec.Engine.Type)
	}
	if len(tmpl.Spec.Inputs) != 4 {
		t.Fatalf("expected 4 inputs, got %d", len(tmpl.Spec.Inputs))
	}
	if len(tmpl.Spec.AdvancedInputs) != 1 {
		t.Fatalf("expected 1 advanced input, got %d", len(tmpl.Spec.AdvancedInputs))
	}
	if len(tmpl.Spec.SecretInputs) != 1 {
		t.Fatalf("expected 1 secret input, got %d", len(tmpl.Spec.SecretInputs))
	}
	if len(tmpl.Spec.Presets) != 2 {
		t.Fatalf("expected 2 presets, got %d", len(tmpl.Spec.Presets))
	}
	if len(tmpl.Spec.Mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(tmpl.Spec.Mappings))
	}
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := Parse([]byte(`{{{not yaml`))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParseMinimalValid(t *testing.T) {
	yaml := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: minimal
  version: "0.1.0"
spec:
  title: Minimal
  category: misc
  engine:
    type: helm
`
	tmpl, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("minimal template should be valid: %v", err)
	}
	if tmpl.Metadata.Name != "minimal" {
		t.Fatalf("expected name %q, got %q", "minimal", tmpl.Metadata.Name)
	}
}

func mustContain(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("expected error containing %q, got: %v", substr, err)
	}
}

func TestValidateWrongAPIVersion(t *testing.T) {
	tmpl := validTemplate()
	tmpl.APIVersion = "v99"
	mustContain(t, tmpl.Validate(), "unsupported apiVersion")
}

func TestValidateWrongKind(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Kind = "Service"
	mustContain(t, tmpl.Validate(), "unsupported kind")
}

func TestValidateMissingName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Metadata.Name = ""
	mustContain(t, tmpl.Validate(), "metadata.name")
}

func TestValidateMissingVersion(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Metadata.Version = ""
	mustContain(t, tmpl.Validate(), "metadata.version")
}

func TestValidateMissingTitle(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Title = ""
	mustContain(t, tmpl.Validate(), "spec.title")
}

func TestValidateMissingCategory(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Category = ""
	mustContain(t, tmpl.Validate(), "spec.category")
}

func TestValidateMissingEngineType(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Engine.Type = ""
	mustContain(t, tmpl.Validate(), "spec.engine.type")
}

func TestValidateUnsupportedEngineType(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Engine.Type = "kustomize"
	mustContain(t, tmpl.Validate(), "unsupported engine type")
}

func TestValidateInputMissingName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Title: "X", Type: InputTypeString}}
	mustContain(t, tmpl.Validate(), "name is required")
}

func TestValidateInputMissingTitle(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "x", Type: InputTypeString}}
	mustContain(t, tmpl.Validate(), "title is required")
}

func TestValidateInputInvalidType(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "x", Title: "X", Type: "file"}}
	mustContain(t, tmpl.Validate(), "unsupported type")
}

func TestValidateDuplicateInputName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{
		{Name: "dup", Title: "A", Type: InputTypeString},
		{Name: "dup", Title: "B", Type: InputTypeString},
	}
	mustContain(t, tmpl.Validate(), "duplicate input name")
}

func TestValidateDuplicateAcrossInputAndAdvanced(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "x", Title: "X", Type: InputTypeString}}
	tmpl.Spec.AdvancedInputs = []Input{{Name: "x", Title: "X2", Type: InputTypeString}}
	mustContain(t, tmpl.Validate(), "duplicate input name")
}

func TestValidateEnumWithoutOptions(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "e", Title: "E", Type: InputTypeEnum}}
	mustContain(t, tmpl.Validate(), "enum type requires at least one option")
}

func TestValidateNumberMinExceedsMax(t *testing.T) {
	tmpl := validTemplate()
	min, max := 10.0, 5.0
	tmpl.Spec.Inputs = []Input{{Name: "n", Title: "N", Type: InputTypeNumber, Min: &min, Max: &max}}
	mustContain(t, tmpl.Validate(), "min")
}

func TestValidateDefaultTypeMismatchString(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "s", Title: "S", Type: InputTypeString, Default: 42}}
	mustContain(t, tmpl.Validate(), "default must be a string")
}

func TestValidateDefaultTypeMismatchNumber(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "n", Title: "N", Type: InputTypeNumber, Default: "abc"}}
	mustContain(t, tmpl.Validate(), "default must be a number")
}

func TestValidateDefaultTypeMismatchBoolean(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "b", Title: "B", Type: InputTypeBoolean, Default: "yes"}}
	mustContain(t, tmpl.Validate(), "default must be a boolean")
}

func TestValidateEnumDefaultNotInOptions(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "e", Title: "E", Type: InputTypeEnum, Options: []string{"a", "b"}, Default: "c"}}
	mustContain(t, tmpl.Validate(), "not in options")
}

func TestValidateSecretInputMissingRef(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.SecretInputs = []SecretInput{{Name: "s", Title: "S"}}
	mustContain(t, tmpl.Validate(), "secretRef is required")
}

func TestValidateSecretInputBadRefFormat(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.SecretInputs = []SecretInput{{Name: "s", Title: "S", SecretRef: "noDot"}}
	mustContain(t, tmpl.Validate(), "secret-name.key")
}

func TestValidatePresetUnknownInput(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "x", Title: "X", Type: InputTypeString}}
	tmpl.Spec.Presets = []Preset{{Name: "p", Title: "P", Values: map[string]any{"ghost": "val"}}}
	mustContain(t, tmpl.Validate(), "unknown input")
}

func TestValidatePresetMissingName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Presets = []Preset{{Title: "P", Values: map[string]any{}}}
	mustContain(t, tmpl.Validate(), "name is required")
}

func validTemplate() *Template {
	return &Template{
		APIVersion: CurrentAPIVersion,
		Kind:       TemplateKind,
		Metadata:   Metadata{Name: "test", Version: "1.0.0"},
		Spec: TemplateSpec{
			Title:    "Test",
			Category: "misc",
			Engine:   Engine{Type: EngineHelm},
		},
	}
}

// ── spec.components — parse ───────────────────────────────────────────────────

const templateWithComponentsYAML = `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: multi-component
  version: "1.0.0"
spec:
  title: Multi Component
  category: web
  engine:
    type: helm
    chart: ./chart
  components:
    - name: web
      type: web
      required: true
      defaultEnabled: true
      previewEnabled: true
      exposed: true
    - name: worker
      type: worker
      defaultEnabled: false
      previewEnabled: false
      exposed: false
    - name: cron
      type: cron
      previewEnabled: false
  inputs:
    - name: image
      title: Container Image
      type: string
      component: web
    - name: schedule
      title: Cron Schedule
      type: string
      component: cron
    - name: replicas
      title: Replica Count
      type: number
      default: 2
`

func TestParseTemplateWithComponents(t *testing.T) {
	tmpl, err := Parse([]byte(templateWithComponentsYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(tmpl.Spec.Components) != 3 {
		t.Fatalf("expected 3 components, got %d", len(tmpl.Spec.Components))
	}

	web := tmpl.Spec.Components[0]
	if web.Name != "web" {
		t.Errorf("components[0].Name = %q, want %q", web.Name, "web")
	}
	if web.Type != TemplateComponentWeb {
		t.Errorf("components[0].Type = %q, want %q", web.Type, TemplateComponentWeb)
	}
	if !web.Required {
		t.Error("components[0].Required should be true")
	}
	if !web.PreviewEnabled {
		t.Error("components[0].PreviewEnabled should be true")
	}
	if !web.Exposed {
		t.Error("components[0].Exposed should be true")
	}
	if !web.IsDefaultEnabled() {
		t.Error("components[0].IsDefaultEnabled() should be true when defaultEnabled: true")
	}

	worker := tmpl.Spec.Components[1]
	if worker.Type != TemplateComponentWorker {
		t.Errorf("components[1].Type = %q, want %q", worker.Type, TemplateComponentWorker)
	}
	if worker.IsDefaultEnabled() {
		t.Error("components[1].IsDefaultEnabled() should be false when defaultEnabled: false")
	}
	if worker.Exposed {
		t.Error("components[1].Exposed should be false")
	}

	cron := tmpl.Spec.Components[2]
	if cron.Type != TemplateComponentCron {
		t.Errorf("components[2].Type = %q, want %q", cron.Type, TemplateComponentCron)
	}
	// defaultEnabled omitted → nil → treated as true
	if !cron.IsDefaultEnabled() {
		t.Error("components[2].IsDefaultEnabled() should be true when defaultEnabled is omitted")
	}
}

func TestParseComponentsInputScoping(t *testing.T) {
	tmpl, err := Parse([]byte(templateWithComponentsYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byName := make(map[string]Input)
	for _, inp := range tmpl.Spec.Inputs {
		byName[inp.Name] = inp
	}

	if byName["image"].Component != "web" {
		t.Errorf("input 'image'.Component = %q, want %q", byName["image"].Component, "web")
	}
	if byName["schedule"].Component != "cron" {
		t.Errorf("input 'schedule'.Component = %q, want %q", byName["schedule"].Component, "cron")
	}
	if byName["replicas"].Component != "" {
		t.Errorf("input 'replicas'.Component = %q, want %q (app-level)", byName["replicas"].Component, "")
	}
}

// ── IsDefaultEnabled ──────────────────────────────────────────────────────────

func TestIsDefaultEnabledNilMeansTrue(t *testing.T) {
	c := TemplateComponent{Name: "web", Type: TemplateComponentWeb}
	if !c.IsDefaultEnabled() {
		t.Error("IsDefaultEnabled() should be true when DefaultEnabled is nil")
	}
}

func TestIsDefaultEnabledExplicitTrue(t *testing.T) {
	v := true
	c := TemplateComponent{Name: "web", Type: TemplateComponentWeb, DefaultEnabled: &v}
	if !c.IsDefaultEnabled() {
		t.Error("IsDefaultEnabled() should be true when DefaultEnabled is &true")
	}
}

func TestIsDefaultEnabledExplicitFalse(t *testing.T) {
	v := false
	c := TemplateComponent{Name: "web", Type: TemplateComponentWeb, DefaultEnabled: &v}
	if c.IsDefaultEnabled() {
		t.Error("IsDefaultEnabled() should be false when DefaultEnabled is &false")
	}
}

// ── spec.components — validation ──────────────────────────────────────────────

func TestValidateComponentMissingName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{{Type: TemplateComponentWeb}}
	mustContain(t, tmpl.Validate(), "name is required")
}

func TestValidateComponentMissingType(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{{Name: "web"}}
	mustContain(t, tmpl.Validate(), "type is required")
}

func TestValidateComponentUnsupportedType(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{{Name: "db", Type: "database"}}
	mustContain(t, tmpl.Validate(), "unsupported type")
}

func TestValidateComponentDuplicateName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{
		{Name: "web", Type: TemplateComponentWeb},
		{Name: "web", Type: TemplateComponentWorker},
	}
	mustContain(t, tmpl.Validate(), "duplicate component name")
}

func TestValidateComponentAllValidTypes(t *testing.T) {
	for _, ct := range []TemplateComponentType{
		TemplateComponentWeb,
		TemplateComponentWorker,
		TemplateComponentCron,
	} {
		ct := ct
		t.Run(string(ct), func(t *testing.T) {
			tmpl := validTemplate()
			tmpl.Spec.Components = []TemplateComponent{{Name: "c", Type: ct}}
			if err := tmpl.Validate(); err != nil {
				t.Errorf("expected valid, got: %v", err)
			}
		})
	}
}

func TestValidateComponentMultipleComponentsValid(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{
		{Name: "web", Type: TemplateComponentWeb},
		{Name: "worker", Type: TemplateComponentWorker},
		{Name: "cron", Type: TemplateComponentCron},
	}
	if err := tmpl.Validate(); err != nil {
		t.Errorf("expected valid multi-component template, got: %v", err)
	}
}

// ── Input.Component cross-reference ──────────────────────────────────────────

func TestValidateInputComponentRefValid(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{{Name: "web", Type: TemplateComponentWeb}}
	tmpl.Spec.Inputs = []Input{{Name: "port", Title: "Port", Type: InputTypeNumber, Component: "web"}}
	if err := tmpl.Validate(); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidateInputComponentRefUnknown(t *testing.T) {
	tmpl := validTemplate()
	// No components defined; "web" is unknown.
	tmpl.Spec.Inputs = []Input{{Name: "port", Title: "Port", Type: InputTypeNumber, Component: "web"}}
	mustContain(t, tmpl.Validate(), "not defined in spec.components")
}

func TestValidateAdvancedInputComponentRefUnknown(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.AdvancedInputs = []Input{
		{Name: "cpu", Title: "CPU", Type: InputTypeString, Component: "nonexistent"},
	}
	mustContain(t, tmpl.Validate(), "not defined in spec.components")
}

func TestValidateInputNoComponentFieldIsAlwaysValid(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "image", Title: "Image", Type: InputTypeString}}
	if err := tmpl.Validate(); err != nil {
		t.Errorf("input without component field should always be valid: %v", err)
	}
}

// ── Backward compatibility ────────────────────────────────────────────────────

// Templates without a spec.components section must continue to parse and
// validate without modification (existing templates must not break).
func TestBackwardCompatNoComponentsSection(t *testing.T) {
	tmpl, err := Parse([]byte(validTemplateYAML))
	if err != nil {
		t.Fatalf("existing template without components section should still be valid: %v", err)
	}
	if len(tmpl.Spec.Components) != 0 {
		t.Errorf("expected 0 components when section is absent, got %d", len(tmpl.Spec.Components))
	}
}

func TestBackwardCompatMinimalTemplate(t *testing.T) {
	yaml := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: minimal
  version: "0.1.0"
spec:
  title: Minimal
  category: misc
  engine:
    type: helm
`
	if _, err := Parse([]byte(yaml)); err != nil {
		t.Errorf("minimal template without components should parse: %v", err)
	}
}
