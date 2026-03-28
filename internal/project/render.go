package project

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/suparcloud/suparship/internal/tpl"
)

// ValidateServiceInputs checks that the provided values and secret refs
// conform to the template's input schema. It enforces:
//   - required inputs are present (unless they have a default)
//   - provided values match known template inputs
//   - values have the correct type (string, number, boolean, enum)
//   - enum values are within the allowed options
//   - numbers are within min/max range
//   - strings match pattern if defined
//   - secret inputs are only provided as refs, never as plaintext values
//   - secret refs reference known secret inputs
func ValidateServiceInputs(values map[string]any, secretRefs []SecretRef, tmpl *tpl.Template) error {
	allInputs := make([]tpl.Input, 0, len(tmpl.Spec.Inputs)+len(tmpl.Spec.AdvancedInputs))
	allInputs = append(allInputs, tmpl.Spec.Inputs...)
	allInputs = append(allInputs, tmpl.Spec.AdvancedInputs...)

	inputDefs := make(map[string]tpl.Input, len(allInputs))
	for _, inp := range allInputs {
		inputDefs[inp.Name] = inp
	}

	secretInputNames := make(map[string]bool, len(tmpl.Spec.SecretInputs))
	for _, si := range tmpl.Spec.SecretInputs {
		secretInputNames[si.Name] = true
	}

	for _, inp := range allInputs {
		if inp.Required {
			if _, ok := values[inp.Name]; !ok && inp.Default == nil {
				return fmt.Errorf("required input %q (%s) is missing", inp.Name, inp.Title)
			}
		}
	}

	for name, val := range values {
		if secretInputNames[name] {
			return fmt.Errorf("input %q is a secret — provide it as a secretRef, not a plaintext value", name)
		}
		inp, ok := inputDefs[name]
		if !ok {
			return fmt.Errorf("unknown input %q — not defined in template %q", name, tmpl.Metadata.Name)
		}
		if err := validateInputValue(inp, val); err != nil {
			return fmt.Errorf("input %q: %w", name, err)
		}
	}

	for _, sr := range secretRefs {
		if !secretInputNames[sr.Name] {
			return fmt.Errorf("unknown secret input %q — not defined in template %q", sr.Name, tmpl.Metadata.Name)
		}
	}

	return nil
}

func validateInputValue(inp tpl.Input, val any) error {
	switch inp.Type {
	case tpl.InputTypeString:
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("expected string, got %T", val)
		}
		if inp.Pattern != "" {
			re, err := regexp.Compile(inp.Pattern)
			if err == nil && !re.MatchString(s) {
				return fmt.Errorf("value %q does not match pattern %s", s, inp.Pattern)
			}
		}
	case tpl.InputTypeNumber:
		n, ok := toFloat64(val)
		if !ok {
			return fmt.Errorf("expected number, got %T", val)
		}
		if inp.Min != nil && n < *inp.Min {
			return fmt.Errorf("value %g is below minimum %g", n, *inp.Min)
		}
		if inp.Max != nil && n > *inp.Max {
			return fmt.Errorf("value %g exceeds maximum %g", n, *inp.Max)
		}
	case tpl.InputTypeBoolean:
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("expected boolean, got %T", val)
		}
	case tpl.InputTypeEnum:
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("expected string for enum, got %T", val)
		}
		found := false
		for _, opt := range inp.Options {
			if opt == s {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("value %q is not a valid option (valid: %s)", s, strings.Join(inp.Options, ", "))
		}
	}
	return nil
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// RenderHelmValues applies the template's mappings to produce a Helm values
// override map. Template defaults are merged with user-provided values, then
// mappings are resolved. Dotted keys like "image.repository" are expanded
// into nested maps: {"image": {"repository": "..."}}.
func RenderHelmValues(svc *Service, tmpl *tpl.Template) map[string]any {
	merged := make(map[string]any)

	allInputs := make([]tpl.Input, 0, len(tmpl.Spec.Inputs)+len(tmpl.Spec.AdvancedInputs))
	allInputs = append(allInputs, tmpl.Spec.Inputs...)
	allInputs = append(allInputs, tmpl.Spec.AdvancedInputs...)
	for _, inp := range allInputs {
		if inp.Default != nil {
			merged[inp.Name] = inp.Default
		}
	}
	for k, v := range svc.Values {
		merged[k] = v
	}

	result := make(map[string]any)
	for helmKey, expr := range tmpl.Spec.Mappings {
		val := resolveMapping(expr, merged)
		if val != nil {
			setNestedValue(result, helmKey, val)
		}
	}

	return result
}

func resolveMapping(expr string, values map[string]any) any {
	expr = strings.TrimSpace(expr)
	if strings.HasPrefix(expr, "{{") && strings.HasSuffix(expr, "}}") {
		inner := strings.TrimSpace(expr[2 : len(expr)-2])
		if strings.HasPrefix(inner, ".inputs.") {
			name := strings.TrimPrefix(inner, ".inputs.")
			return values[name]
		}
	}
	return expr
}

func setNestedValue(m map[string]any, dottedKey string, val any) {
	parts := strings.Split(dottedKey, ".")
	current := m
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = val
		} else {
			sub, ok := current[part].(map[string]any)
			if !ok {
				sub = make(map[string]any)
				current[part] = sub
			}
			current = sub
		}
	}
}
