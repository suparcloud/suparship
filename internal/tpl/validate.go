package tpl

import (
	"fmt"
	"regexp"
	"strings"
)

var validEngineTypes = map[string]bool{
	EngineHelm: true,
}

// validImageSelectionStrategies are the Kargo tag-selection strategies allowed
// on a TemplateImage. Empty is permitted and defaults to "SemVer".
var validImageSelectionStrategies = map[string]bool{
	"NewestBuild": true,
	"SemVer":      true,
	"Digest":      true,
	"Lexical":     true,
}

var validInputTypes = map[InputType]bool{
	InputTypeString:  true,
	InputTypeNumber:  true,
	InputTypeBoolean: true,
	InputTypeEnum:    true,
}

var validTemplateComponentTypes = map[TemplateComponentType]bool{
	TemplateComponentWeb:    true,
	TemplateComponentWorker: true,
	TemplateComponentCron:   true,
	TemplateComponentJob:    true,
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

	// Validate spec.components first so component names are available for
	// cross-referencing from inputs below.
	componentNames, err := validateComponents(t.Spec.Components)
	if err != nil {
		return err
	}

	inputNames := make(map[string]bool)

	for i, inp := range t.Spec.Inputs {
		if err := validateInput(inp, fmt.Sprintf("spec.inputs[%d]", i), componentNames); err != nil {
			return err
		}
		if inputNames[inp.Name] {
			return fmt.Errorf("duplicate input name %q", inp.Name)
		}
		inputNames[inp.Name] = true
	}

	for i, inp := range t.Spec.AdvancedInputs {
		if err := validateInput(inp, fmt.Sprintf("spec.advancedInputs[%d]", i), componentNames); err != nil {
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

	if err := validateImages(t.Spec.Images); err != nil {
		return err
	}

	if err := validateDeveloperValues(t.Spec.DeveloperValues); err != nil {
		return err
	}

	return nil
}

// validateDeveloperValues checks the spec.developerValues projection: each entry
// needs a path, paths are unique (two entries for one path would seed the key
// twice), the type (when set) is recognised, an enum declares options, and any
// bounds/pattern are coherent. Type is OPTIONAL here — unlike an Input, a
// ValueField with no type is a valid free-form passthrough that the 0.1 YAML
// editor renders fine; the type only starts mattering to a future form renderer.
func validateDeveloperValues(fields []ValueField) error {
	paths := make(map[string]bool, len(fields))
	for i, f := range fields {
		path := fmt.Sprintf("spec.developerValues[%d]", i)
		if f.Path == "" {
			return fmt.Errorf("%s: path is required", path)
		}
		if paths[f.Path] {
			return fmt.Errorf("%s: duplicate path %q", path, f.Path)
		}
		paths[f.Path] = true
		if f.Type != "" && !validInputTypes[f.Type] {
			return fmt.Errorf("%s (%s): unsupported type %q", path, f.Path, f.Type)
		}
		if f.Type == InputTypeEnum && len(f.Options) == 0 {
			return fmt.Errorf("%s (%s): enum type requires at least one option", path, f.Path)
		}
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return fmt.Errorf("%s (%s): min (%g) must not exceed max (%g)", path, f.Path, *f.Min, *f.Max)
		}
		if f.Pattern != "" {
			if _, err := regexp.Compile(f.Pattern); err != nil {
				return fmt.Errorf("%s (%s): invalid pattern %q: %w", path, f.Path, f.Pattern, err)
			}
		}
	}
	return nil
}

// validateImages checks the spec.images slice: each entry needs a name,
// repository, and tagKey; names are unique; the selection strategy (when set)
// is recognised; and the tag pattern (when set) is a valid regex.
func validateImages(images []TemplateImage) error {
	names := make(map[string]bool, len(images))
	for i, img := range images {
		path := fmt.Sprintf("spec.images[%d]", i)
		if img.Name == "" {
			return fmt.Errorf("%s: name is required", path)
		}
		if names[img.Name] {
			return fmt.Errorf("%s: duplicate image name %q", path, img.Name)
		}
		names[img.Name] = true
		if img.Repository == "" {
			return fmt.Errorf("%s (%s): repository is required", path, img.Name)
		}
		if img.TagKey == "" {
			return fmt.Errorf("%s (%s): tagKey is required", path, img.Name)
		}
		if img.SelectionStrategy != "" && !validImageSelectionStrategies[img.SelectionStrategy] {
			return fmt.Errorf("%s (%s): unsupported selectionStrategy %q (must be one of NewestBuild, SemVer, Digest, Lexical)",
				path, img.Name, img.SelectionStrategy)
		}
		if img.TagPattern != "" {
			if _, err := regexp.Compile(img.TagPattern); err != nil {
				return fmt.Errorf("%s (%s): invalid tagPattern %q: %w", path, img.Name, img.TagPattern, err)
			}
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

// validateComponents validates the spec.components slice and returns the set
// of declared component names so callers can cross-reference from inputs.
func validateComponents(components []TemplateComponent) (map[string]bool, error) {
	names := make(map[string]bool, len(components))
	for i, c := range components {
		path := fmt.Sprintf("spec.components[%d]", i)
		if err := validateTemplateComponent(c, path); err != nil {
			return nil, err
		}
		if names[c.Name] {
			return nil, fmt.Errorf("duplicate component name %q in spec.components", c.Name)
		}
		names[c.Name] = true
	}
	return names, nil
}

// validateTemplateComponent checks structural correctness of one component entry.
func validateTemplateComponent(c TemplateComponent, path string) error {
	if c.Name == "" {
		return fmt.Errorf("%s: name is required", path)
	}
	if c.Type == "" {
		return fmt.Errorf("%s (%s): type is required", path, c.Name)
	}
	if !validTemplateComponentTypes[c.Type] {
		return fmt.Errorf("%s (%s): unsupported type %q (must be one of web, worker, cron, job)",
			path, c.Name, c.Type)
	}
	return nil
}

func validateInput(inp Input, path string, componentNames map[string]bool) error {
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

	if inp.Component != "" && !componentNames[inp.Component] {
		return fmt.Errorf("%s (%s): component %q is not defined in spec.components",
			path, inp.Name, inp.Component)
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
