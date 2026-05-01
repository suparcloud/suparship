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

// --- ValidateAppInputs ---

// TestValidateAppInputs_Valid exercises the primary (non-deprecated) entry
// point to ensure it delegates correctly to the shared implementation.
func TestValidateAppInputs_Valid(t *testing.T) {
	err := ValidateAppInputs(
		map[string]any{"service_name": "my-app", "size": "medium"},
		[]SecretRef{{Name: "database_url", SecretRef: "db.url"}},
		testTemplate(),
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// TestValidateAppInputs_MissingRequired confirms that ValidateAppInputs
// enforces required-input constraints identically to ValidateServiceInputs.
func TestValidateAppInputs_MissingRequired(t *testing.T) {
	err := ValidateAppInputs(
		map[string]any{"size": "small"},
		nil,
		testTemplate(),
	)
	expectContains(t, err, "required input", "service_name")
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

	result, err := RenderHelmValues(svc, testTemplate())
	if err != nil {
		t.Fatalf("RenderHelmValues: %v", err)
	}

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

	result, err := RenderHelmValues(svc, testTemplate())
	if err != nil {
		t.Fatalf("RenderHelmValues: %v", err)
	}

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

	result, err := RenderHelmValues(svc, testTemplate())
	if err != nil {
		t.Fatalf("RenderHelmValues: %v", err)
	}

	ingress, ok := result["ingress"].(map[string]any)
	if !ok {
		t.Fatalf("expected ingress to be nested map, got %T: %v", result["ingress"], result["ingress"])
	}
	if ingress["enabled"] != true {
		t.Fatalf("expected ingress.enabled true, got %v", ingress["enabled"])
	}
}

// --- Mapping engine — extended Go-template features ---

func TestRenderHelmValues_DefaultFn(t *testing.T) {
	tmpl := testTemplate()
	tmpl.Spec.Mappings["resolvedSize"] = `{{ default "small" .inputs.size }}`
	tmpl.Spec.Mappings["fallbackEnv"] = `{{ default "prod" .inputs.unset_input }}`

	svc := &Service{Name: "x", Values: map[string]any{"service_name": "x"}}
	got, err := RenderHelmValues(svc, tmpl)
	if err != nil {
		t.Fatalf("RenderHelmValues: %v", err)
	}
	if got["resolvedSize"] != "small" {
		t.Errorf("resolvedSize = %v, want small", got["resolvedSize"])
	}
	if got["fallbackEnv"] != "prod" {
		t.Errorf("fallbackEnv = %v, want prod (default kicks in for unknown input)", got["fallbackEnv"])
	}
}

func TestRenderHelmValues_UpperLower(t *testing.T) {
	tmpl := testTemplate()
	tmpl.Spec.Mappings["sizeUpper"] = `{{ .inputs.size | upper }}`
	tmpl.Spec.Mappings["sizeLower"] = `{{ "MEDIUM" | lower }}`

	svc := &Service{Name: "x", Values: map[string]any{"service_name": "x", "size": "large"}}
	got, err := RenderHelmValues(svc, tmpl)
	if err != nil {
		t.Fatalf("RenderHelmValues: %v", err)
	}
	if got["sizeUpper"] != "LARGE" {
		t.Errorf("sizeUpper = %v, want LARGE", got["sizeUpper"])
	}
	if got["sizeLower"] != "medium" {
		t.Errorf("sizeLower = %v, want medium", got["sizeLower"])
	}
}

func TestRenderHelmValues_Conditional(t *testing.T) {
	tmpl := testTemplate()
	// Add an enum input so user-provided values pass validation, then
	// branch on it in the mapping.
	tmpl.Spec.Inputs = append(tmpl.Spec.Inputs, tpl.Input{
		Name: "agent_type", Type: tpl.InputTypeEnum,
		Options: []string{"outbound", "inbound"}, Default: "outbound",
	})
	tmpl.Spec.Mappings["commandPath"] = `{{ if eq .inputs.agent_type "inbound" }}/bin/sip_inbound.py{{ else }}/bin/sip_outbound.py{{ end }}`

	for _, tc := range []struct {
		variant string
		want    string
	}{
		{"outbound", "/bin/sip_outbound.py"},
		{"inbound", "/bin/sip_inbound.py"},
	} {
		svc := &Service{Name: "x", Values: map[string]any{"service_name": "x", "agent_type": tc.variant}}
		got, err := RenderHelmValues(svc, tmpl)
		if err != nil {
			t.Fatalf("variant=%s: %v", tc.variant, err)
		}
		if got["commandPath"] != tc.want {
			t.Errorf("variant=%s: commandPath = %v, want %v", tc.variant, got["commandPath"], tc.want)
		}
	}
}

func TestRenderHelmValues_PreservesScalarTypes(t *testing.T) {
	// Fast path: simple `{{ .inputs.replicas }}` returns the original int,
	// not a string. This matters for charts that read `replicaCount: 2`.
	svc := &Service{Name: "x", Values: map[string]any{"service_name": "x", "replicas": 5}}
	got, err := RenderHelmValues(svc, testTemplate())
	if err != nil {
		t.Fatalf("RenderHelmValues: %v", err)
	}
	if got["replicaCount"] != 5 {
		t.Errorf("replicaCount type/value = %T %v, want int 5", got["replicaCount"], got["replicaCount"])
	}
}

func TestRenderHelmValues_BadExpressionReturnsError(t *testing.T) {
	tmpl := testTemplate()
	tmpl.Spec.Mappings["broken"] = `{{ this is | not valid template }}`

	svc := &Service{Name: "x", Values: map[string]any{"service_name": "x"}}
	_, err := RenderHelmValues(svc, tmpl)
	if err == nil {
		t.Fatal("expected error for invalid template expression, got nil")
	}
	if !strings.Contains(err.Error(), "broken") && !strings.Contains(err.Error(), "parse") {
		t.Errorf("error %q should mention the broken mapping or parse failure", err.Error())
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
