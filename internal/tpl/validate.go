package tpl

import (
	"fmt"
	"strings"
)

var validEngineTypes = map[string]bool{
	EngineHelm: true,
}

var validInputTypes = map[InputType]bool{
	InputTypeString:  true,
	InputTypeNumber:  true,
	InputTypeBoolean: true,
	InputTypeEnum:    true,
}

// Validate checks the Template for structural correctness.
func (t *Template) Validate() error {
	if t.APIVersion != CurrentAPIVersion {
		return fmt.Errorf("unsupported apiVersion %q, expected %q", t.APIVersion, CurrentAPIVersion)
	}
	if t.Kind != TemplateKind {
		return fmt.Errorf("unsupported kind %q, expected %q", t.Kind, TemplateKind)
	}
	if t.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if t.Metadata.Version == "" {
		return fmt.Errorf("metadata.version is required")
	}
	if t.Spec.Title == "" {
		return fmt.Errorf("spec.title is required")
	}
	if t.Spec.Category == "" {
		return fmt.Errorf("spec.category is required")
	}

	if err := validateEngine(t.Spec.Engine); err != nil {
		return err
	}

	inputNames := make(map[string]bool)

	for i, inp := range t.Spec.Inputs {
		if err := validateInput(inp, fmt.Sprintf("spec.inputs[%d]", i)); err != nil {
			return err
		}
		if inputNames[inp.Name] {
			return fmt.Errorf("duplicate input name %q", inp.Name)
		}
		inputNames[inp.Name] = true
	}

	for i, inp := range t.Spec.AdvancedInputs {
		if err := validateInput(inp, fmt.Sprintf("spec.advancedInputs[%d]", i)); err != nil {
			return err
		}
		if inputNames[inp.Name] {
			return fmt.Errorf("duplicate input name %q", inp.Name)
		}
		inputNames[inp.Name] = true
	}

	for i, si := range t.Spec.SecretInputs {
		if err := validateSecretInput(si, fmt.Sprintf("spec.secretInputs[%d]", i)); err != nil {
			return err
		}
	}

	for i, p := range t.Spec.Presets {
		if err := validatePreset(p, inputNames, fmt.Sprintf("spec.presets[%d]", i)); err != nil {
			return err
		}
	}

	return nil
}

func validateEngine(e Engine) error {
	if e.Type == "" {
		return fmt.Errorf("spec.engine.type is required")
	}
	if !validEngineTypes[e.Type] {
		return fmt.Errorf("unsupported engine type %q", e.Type)
	}
	return nil
}

func validateInput(inp Input, path string) error {
	if inp.Name == "" {
		return fmt.Errorf("%s: name is required", path)
	}
	if inp.Title == "" {
		return fmt.Errorf("%s (%s): title is required", path, inp.Name)
	}
	if !validInputTypes[inp.Type] {
		return fmt.Errorf("%s (%s): unsupported type %q", path, inp.Name, inp.Type)
	}

	if inp.Type == InputTypeEnum && len(inp.Options) == 0 {
		return fmt.Errorf("%s (%s): enum type requires at least one option", path, inp.Name)
	}

	if inp.Min != nil && inp.Max != nil && *inp.Min > *inp.Max {
		return fmt.Errorf("%s (%s): min (%g) must not exceed max (%g)", path, inp.Name, *inp.Min, *inp.Max)
	}

	if inp.Default != nil {
		if err := validateDefault(inp); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}

	return nil
}

func validateDefault(inp Input) error {
	switch inp.Type {
	case InputTypeString:
		if _, ok := inp.Default.(string); !ok {
			return fmt.Errorf("input %q: default must be a string", inp.Name)
		}
	case InputTypeNumber:
		if !isNumber(inp.Default) {
			return fmt.Errorf("input %q: default must be a number", inp.Name)
		}
	case InputTypeBoolean:
		if _, ok := inp.Default.(bool); !ok {
			return fmt.Errorf("input %q: default must be a boolean", inp.Name)
		}
	case InputTypeEnum:
		s, ok := inp.Default.(string)
		if !ok {
			return fmt.Errorf("input %q: default must be a string", inp.Name)
		}
		found := false
		for _, opt := range inp.Options {
			if opt == s {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("input %q: default %q not in options %v", inp.Name, s, inp.Options)
		}
	}
	return nil
}

func validateSecretInput(si SecretInput, path string) error {
	if si.Name == "" {
		return fmt.Errorf("%s: name is required", path)
	}
	if si.Title == "" {
		return fmt.Errorf("%s (%s): title is required", path, si.Name)
	}
	if si.SecretRef == "" {
		return fmt.Errorf("%s (%s): secretRef is required", path, si.Name)
	}
	if !strings.Contains(si.SecretRef, ".") {
		return fmt.Errorf("%s (%s): secretRef must be in format \"secret-name.key\"", path, si.Name)
	}
	return nil
}

func validatePreset(p Preset, validInputs map[string]bool, path string) error {
	if p.Name == "" {
		return fmt.Errorf("%s: name is required", path)
	}
	if p.Title == "" {
		return fmt.Errorf("%s (%s): title is required", path, p.Name)
	}
	for key := range p.Values {
		if !validInputs[key] {
			return fmt.Errorf("%s (%s): references unknown input %q", path, p.Name, key)
		}
	}
	return nil
}

func isNumber(v any) bool {
	switch v.(type) {
	case int, int64, float64:
		return true
	default:
		return false
	}
}
