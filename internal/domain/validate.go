package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// dnsLabelRE matches a valid Kubernetes / DNS label: lowercase alphanumeric
// and hyphens, 2–63 characters, starting with a letter, ending with an
// alphanumeric character.
//
// The same pattern is used in internal/preview and internal/project; it is
// intentionally re-declared here so the domain package remains self-contained
// with no cross-package imports.
var dnsLabelRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// IsDNSLabel reports whether s is a valid DNS label per the constraint used
// throughout suparship (see dnsLabelRE above).
func IsDNSLabel(s string) bool {
	return dnsLabelRE.MatchString(s)
}

// ValidateAppName returns an error if name is not a valid app identifier.
// App names follow the same DNS-label rules used for Kubernetes resource names:
// lowercase alphanumeric and hyphens, 2–63 characters, starting with a letter.
func ValidateAppName(name string) error {
	if name == "" {
		return fmt.Errorf("app name must not be empty")
	}
	if !IsDNSLabel(name) {
		return fmt.Errorf(
			"app name %q must be a valid DNS label (lowercase alphanumeric and hyphens, 2–63 chars, starting with a letter)",
			name,
		)
	}
	return nil
}

// ValidateComponentName returns an error if name is not a valid component
// identifier. Component names follow the same DNS-label rules as app names.
func ValidateComponentName(name string) error {
	if name == "" {
		return fmt.Errorf("component name must not be empty")
	}
	if !IsDNSLabel(name) {
		return fmt.Errorf(
			"component name %q must be a valid DNS label (lowercase alphanumeric and hyphens, 2–63 chars, starting with a letter)",
			name,
		)
	}
	return nil
}

// ValidateComponents checks a component list for structural correctness:
//   - at least one component is required
//   - each component name must be a valid DNS label
//   - component names must be unique within the list
//   - each component type must be one of the recognised MVP values
func ValidateComponents(components []ComponentSpec) error {
	if len(components) == 0 {
		return fmt.Errorf("app must have at least one component")
	}

	seen := make(map[string]bool, len(components))
	for i, c := range components {
		if err := ValidateComponentName(c.Name); err != nil {
			return fmt.Errorf("components[%d]: %w", i, err)
		}
		if seen[c.Name] {
			return fmt.Errorf("duplicate component name %q", c.Name)
		}
		seen[c.Name] = true

		if !c.Type.Valid() {
			return fmt.Errorf(
				"components[%d] %q: unsupported type %q (must be one of web, worker, cron)",
				i, c.Name, c.Type,
			)
		}
	}
	return nil
}

// ValidateSingleExposedComponent checks that at most one component has type
// ComponentWeb (the "primary exposed" component). Pass allowMultiple = true to
// skip this check when a template explicitly permits multiple web endpoints.
func ValidateSingleExposedComponent(components []ComponentSpec, allowMultiple bool) error {
	if allowMultiple {
		return nil
	}
	count := 0
	for _, c := range components {
		if c.Type == ComponentWeb {
			count++
		}
	}
	if count > 1 {
		return fmt.Errorf(
			"app has %d web components; at most one is allowed unless explicitly permitted",
			count,
		)
	}
	return nil
}

// ValidateComponentSpec checks a single ComponentSpec for field-level
// correctness beyond name and type:
//   - Replicas must be non-negative
//   - SizePreset, when set, must be one of small, medium, large
//   - Replicas and SizePreset are mutually exclusive
func ValidateComponentSpec(c ComponentSpec) error {
	if err := ValidateComponentName(c.Name); err != nil {
		return err
	}
	if !c.Type.Valid() {
		return fmt.Errorf("component %q: unsupported type %q (must be one of web, worker, cron)", c.Name, c.Type)
	}
	if c.Replicas < 0 {
		return fmt.Errorf("component %q: replicas must be non-negative, got %d", c.Name, c.Replicas)
	}
	if c.SizePreset != "" {
		if _, err := ParseSizePreset(string(c.SizePreset)); err != nil {
			return fmt.Errorf("component %q: %w", c.Name, err)
		}
	}
	if c.Replicas > 0 && c.SizePreset != "" {
		return fmt.Errorf("component %q: replicas and sizePreset are mutually exclusive", c.Name)
	}
	return nil
}

// SanitizePreviewName converts an arbitrary string (e.g. a Git branch name or
// PR identifier) into a best-effort valid DNS label for use as a preview
// environment name. The transformation is deterministic:
//  1. Lowercase the input.
//  2. Replace every run of non-alphanumeric characters with a single hyphen.
//  3. Strip leading and trailing hyphens.
//  4. Prepend "pr-" when the result starts with a digit.
//  5. Truncate to 63 characters, stripping any trailing hyphen left by the cut.
//
// If the result is empty after transformation, "preview" is returned. Callers
// should validate the result with ValidatePreviewName to confirm it is usable
// (e.g. a single-character input can produce a name that still fails the
// minimum-length rule).
func SanitizePreviewName(raw string) string {
	s := strings.ToLower(raw)

	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevHyphen = false
		} else if !prevHyphen {
			b.WriteRune('-')
			prevHyphen = true
		}
	}

	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "preview"
	}

	// DNS labels must start with a letter.
	if result[0] >= '0' && result[0] <= '9' {
		result = "pr-" + result
	}

	// Truncate to 63 characters and strip any trailing hyphen introduced by the cut.
	if len(result) > 63 {
		result = strings.TrimRight(result[:63], "-")
	}

	if result == "" {
		return "preview"
	}
	return result
}

// ValidatePreviewName returns an error if name is not a valid preview
// environment name. Preview names follow the same DNS-label rules as app names.
func ValidatePreviewName(name string) error {
	if name == "" {
		return fmt.Errorf("preview name must not be empty")
	}
	if !IsDNSLabel(name) {
		return fmt.Errorf(
			"preview name %q must be a valid DNS label (lowercase alphanumeric and hyphens, 2–63 chars, starting with a letter)",
			name,
		)
	}
	return nil
}

// AppPreviewNamespace returns the Kubernetes namespace for a preview instance
// of an app. Convention: {appName}-preview-{previewName}.
//
// Deprecated: Use GenerateNamespace(appName, previewName, AppEnvPreview)
// instead. AppPreviewNamespace is retained for callers in the compat layer
// that have not yet migrated to the app-centric model.
func AppPreviewNamespace(appName, previewName string) string {
	return GenerateNamespace(appName, previewName, AppEnvPreview)
}
