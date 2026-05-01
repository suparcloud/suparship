package project

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	"github.com/suparcloud/suparship/internal/tpl"
)

// ValidateAppInputs checks that the provided values and secret refs conform to
// the template's input schema. It enforces:
//   - required inputs are present (unless they have a default)
//   - provided values match known template inputs
//   - values have the correct type (string, number, boolean, enum)
//   - enum values are within the allowed options
//   - numbers are within min/max range
//   - strings match pattern if defined
//   - secret inputs are only provided as refs, never as plaintext values
//   - secret refs reference known secret inputs
func ValidateAppInputs(values map[string]any, secretRefs []SecretRef, tmpl *tpl.Template) error {
	return validateAppInputs(values, secretRefs, tmpl)
}

// ValidateServiceInputs is the legacy name for ValidateAppInputs.
//
// Deprecated: Call ValidateAppInputs for new code. ValidateServiceInputs is
// retained for the legacy POST /api/v1/projects/{project}/services handler and
// will be removed once that handler is deleted.
// See docs/migration-app-model.md.
func ValidateServiceInputs(values map[string]any, secretRefs []SecretRef, tmpl *tpl.Template) error {
	return validateAppInputs(values, secretRefs, tmpl)
}

// validateAppInputs is the shared implementation used by both
// ValidateAppInputs and ValidateServiceInputs.
func validateAppInputs(values map[string]any, secretRefs []SecretRef, tmpl *tpl.Template) error {
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
//
// Returns an error when any mapping expression fails to parse or execute —
// bad mappings are template-authoring bugs and should surface to the
// template-import flow rather than silently dropping values.
func RenderHelmValues(svc *Service, tmpl *tpl.Template) (map[string]any, error) {
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
		val, err := resolveMapping(expr, merged)
		if err != nil {
			return nil, fmt.Errorf("mapping %q (-> %s): %w", expr, helmKey, err)
		}
		if val != nil {
			setNestedValue(result, helmKey, val)
		}
	}

	return result, nil
}

// simpleInputRef matches a mapping that is exactly `{{ .inputs.NAME }}` —
// no filters, no conditionals, just a single input reference. The fast
// path returns the raw input value (preserving int / bool / etc), so a
// mapping like `replicaCount: "{{ .inputs.replicas }}"` produces an
// integer in the rendered values, not a string.
var simpleInputRef = regexp.MustCompile(`^\{\{-?\s*\.inputs\.(\w+)\s*-?\}\}$`)

// resolveMapping evaluates one mapping expression against the input
// context. Two paths:
//
//	Fast path (`{{ .inputs.foo }}`)         → returns raw input value;
//	                                          nil when the input is unset
//	                                          so the mapping is dropped.
//	General path (anything else)            → executed via text/template
//	                                          with a sealed FuncMap; the
//	                                          rendered string is returned.
//
// The sealed FuncMap covers the common authoring needs (default, upper,
// lower, quote, trim, join, replace) without pulling in Sprig as a
// dependency. Authors get conditionals (`if`, `with`), comparisons (`eq`,
// `ne`, `lt`, `gt`), and boolean ops (`and`, `or`, `not`) from the
// stdlib template engine itself.
func resolveMapping(expr string, values map[string]any) (any, error) {
	expr = strings.TrimSpace(expr)

	// Fast path: single `.inputs.NAME` reference, preserve type.
	if m := simpleInputRef.FindStringSubmatch(expr); m != nil {
		return values[m[1]], nil
	}

	tmpl, err := template.New("mapping").
		Option("missingkey=zero").
		Funcs(mappingFuncs).
		Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{"inputs": values}); err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}
	return buf.String(), nil
}

// mappingFuncs is the sealed function set available inside a mapping
// expression. Kept small on purpose — every function adds documentation
// surface and a behavioural footprint we'd later have to support.
var mappingFuncs = template.FuncMap{
	// default returns d when v is nil / empty / false / zero.
	"default": func(d, v any) any {
		if isEmptyValue(v) {
			return d
		}
		return v
	},
	"upper":   strings.ToUpper,
	"lower":   strings.ToLower,
	"trim":    strings.TrimSpace,
	"replace": strings.ReplaceAll,
	"contains": strings.Contains,
	"hasPrefix": strings.HasPrefix,
	"hasSuffix": strings.HasSuffix,
	"join": func(sep string, items []string) string { return strings.Join(items, sep) },
	"quote": func(v any) string {
		return fmt.Sprintf("%q", fmt.Sprintf("%v", v))
	},
}

// isEmptyValue mirrors `default`'s "empty" definition: nil, "", false,
// 0, and empty slices / maps.
func isEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case int:
		return x == 0
	case int64:
		return x == 0
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
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
