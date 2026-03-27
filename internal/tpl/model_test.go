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
