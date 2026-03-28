package project

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/tpl"
)

func testTemplate() *tpl.Template {
	min1, max10 := 1.0, 10.0
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "web-service", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Web Service",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm, Chart: "./chart"},
			Inputs: []tpl.Input{
				{Name: "service_name", Title: "Service Name", Type: tpl.InputTypeString, Required: true, Pattern: "^[a-z][a-z0-9-]+$"},
				{Name: "port", Title: "Port", Type: tpl.InputTypeNumber, Default: 8080, Min: &min1, Max: &max10},
				{Name: "ingress_enabled", Title: "Enable Ingress", Type: tpl.InputTypeBoolean, Default: false},
				{Name: "size", Title: "Size", Type: tpl.InputTypeEnum, Options: []string{"small", "medium", "large"}, Default: "small"},
			},
			AdvancedInputs: []tpl.Input{
				{Name: "replicas", Title: "Replicas", Type: tpl.InputTypeNumber, Default: 2, Min: &min1},
			},
			SecretInputs: []tpl.SecretInput{
				{Name: "database_url", Title: "Database URL", SecretRef: "db.url"},
			},
			Mappings: map[string]string{
				"fullnameOverride": "{{ .inputs.service_name }}",
				"port":             "{{ .inputs.port }}",
				"ingress.enabled":  "{{ .inputs.ingress_enabled }}",
				"size":             "{{ .inputs.size }}",
				"replicaCount":     "{{ .inputs.replicas }}",
			},
		},
	}
}

// --- ValidateServiceInputs ---

func TestValidateInputsValid(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"service_name": "my-api", "size": "medium"},
		[]SecretRef{{Name: "database_url", SecretRef: "db.url"}},
		testTemplate(),
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateInputsMissingRequired(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"size": "small"},
		nil,
		testTemplate(),
	)
	expectContains(t, err, "required input", "service_name")
}

func TestValidateInputsUnknownInput(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"service_name": "api", "bogus": "val"},
		nil,
		testTemplate(),
	)
	expectContains(t, err, "unknown input", "bogus")
}

func TestValidateInputsWrongType(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"service_name": "api", "port": "not-a-number"},
		nil,
		testTemplate(),
	)
	expectContains(t, err, "expected number")
}

func TestValidateInputsEnumInvalid(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"service_name": "api", "size": "xlarge"},
		nil,
		testTemplate(),
	)
	expectContains(t, err, "not a valid option")
}

func TestValidateInputsNumberBelowMin(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"service_name": "api", "port": 0.5},
		nil,
		testTemplate(),
	)
	expectContains(t, err, "below minimum")
}

func TestValidateInputsNumberAboveMax(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"service_name": "api", "port": 100.0},
		nil,
		testTemplate(),
	)
	expectContains(t, err, "exceeds maximum")
}

func TestValidateInputsPatternMismatch(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"service_name": "UPPER"},
		nil,
		testTemplate(),
	)
	expectContains(t, err, "does not match pattern")
}

func TestValidateInputsBooleanWrongType(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"service_name": "api", "ingress_enabled": "yes"},
		nil,
		testTemplate(),
	)
	expectContains(t, err, "expected boolean")
}

func TestValidateInputsSecretAsPlaintext(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"service_name": "api", "database_url": "postgres://..."},
		nil,
		testTemplate(),
	)
	expectContains(t, err, "secret", "secretRef")
}

func TestValidateInputsUnknownSecretRef(t *testing.T) {
	err := ValidateServiceInputs(
		map[string]any{"service_name": "api"},
		[]SecretRef{{Name: "unknown_secret", SecretRef: "x.y"}},
		testTemplate(),
	)
	expectContains(t, err, "unknown secret input")
}

// --- RenderHelmValues ---

func TestRenderHelmValuesWithDefaults(t *testing.T) {
	svc := &Service{
		Name:     "api",
		Template: TemplateRef{Name: "web-service"},
		Values:   map[string]any{"service_name": "my-api"},
	}

	result := RenderHelmValues(svc, testTemplate())

	if result["fullnameOverride"] != "my-api" {
		t.Fatalf("expected fullnameOverride %q, got %v", "my-api", result["fullnameOverride"])
	}
	if result["size"] != "small" {
		t.Fatalf("expected size default %q, got %v", "small", result["size"])
	}
	if result["replicaCount"] != 2 {
		t.Fatalf("expected replicaCount default 2, got %v", result["replicaCount"])
	}
}

func TestRenderHelmValuesOverridesDefaults(t *testing.T) {
	svc := &Service{
		Name:     "api",
		Template: TemplateRef{Name: "web-service"},
		Values:   map[string]any{"service_name": "api", "size": "large", "replicas": 5},
	}

	result := RenderHelmValues(svc, testTemplate())

	if result["size"] != "large" {
		t.Fatalf("expected size %q, got %v", "large", result["size"])
	}
	if result["replicaCount"] != 5 {
		t.Fatalf("expected replicaCount 5, got %v", result["replicaCount"])
	}
}

func TestRenderHelmValuesNestedKeys(t *testing.T) {
	svc := &Service{
		Name:     "api",
		Template: TemplateRef{Name: "web-service"},
		Values:   map[string]any{"service_name": "api", "ingress_enabled": true},
	}

	result := RenderHelmValues(svc, testTemplate())

	ingress, ok := result["ingress"].(map[string]any)
	if !ok {
		t.Fatalf("expected ingress to be nested map, got %T: %v", result["ingress"], result["ingress"])
	}
	if ingress["enabled"] != true {
		t.Fatalf("expected ingress.enabled true, got %v", ingress["enabled"])
	}
}

// --- Helpers ---

func expectContains(t *testing.T, err error, substrs ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %v, got nil", substrs)
	}
	for _, s := range substrs {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("expected error containing %q, got: %v", s, err)
		}
	}
}
