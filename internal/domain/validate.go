package domain

import (
	"fmt"
	"net/url"
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
		inheriting := c.InheritAppVars == nil || *c.InheritAppVars
		for _, e := range c.EnvVars {
			if err := validateComponentEnvVar(c.Name, e); err != nil {
				return fmt.Errorf("components[%d]: %w", i, err)
			}
			// Source-mapped entries (fromConfig/fromSecret) only mean anything
			// in the curated projection — while INHERITING app vars, the key
			// already arrives via envFrom, so a mapping would silently do
			// nothing. Literal Value entries are the inherit-mode
			// extend/override channel and are always fine.
			if inheriting && (e.FromConfig != "" || e.FromSecret != "") {
				return fmt.Errorf(
					"components[%d] %q env %q: fromConfig/fromSecret require inheritAppVars=false (a curated variable list); while inheriting, add a literal value or configure the variable at app/env scope",
					i, c.Name, e.Name,
				)
			}
		}
		for _, img := range c.Images {
			if err := validateComponentImage(c.Name, img); err != nil {
				return fmt.Errorf("components[%d]: %w", i, err)
			}
		}
	}
	return ValidateComposedComponents(components)
}

// tagKeyRE matches a dotted Helm values path (e.g. "image.tag",
// "controller.image.tag") — segments of identifier/number chars joined by dots.
var tagKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

// validateComponentImage checks a component image selection: only a well-formed
// dotted tag-key path is required. The repository is normally derived from the
// component's discovered values (empty here); it may still be present as a legacy
// fallback, but it is no longer required.
func validateComponentImage(comp string, img ComponentImage) error {
	if !tagKeyRE.MatchString(img.TagKey) {
		return fmt.Errorf("component %q: an image selection has an invalid tagKey %q (must be a dotted path like components.web.image.tag)", comp, img.TagKey)
	}
	return nil
}

// envVarNameRE matches a valid environment-variable name (C-identifier rules,
// the same shape the env-config editor enforces).
var envVarNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateComponentEnvVar checks one curated env entry: a valid name and at most
// one source (fromConfig / fromSecret), mutually exclusive with a literal value.
func validateComponentEnvVar(comp string, e ComponentEnvVar) error {
	if e.Name == "" {
		return fmt.Errorf("component %q: an env var has no name", comp)
	}
	if !envVarNameRE.MatchString(e.Name) {
		return fmt.Errorf("component %q: invalid env var name %q (must match %s)", comp, e.Name, envVarNameRE.String())
	}
	sources := 0
	if e.FromConfig != "" {
		sources++
	}
	if e.FromSecret != "" {
		sources++
	}
	if sources > 1 {
		return fmt.Errorf("component %q env %q: set only one of fromConfig or fromSecret", comp, e.Name)
	}
	if sources == 1 && e.Value != "" {
		return fmt.Errorf("component %q env %q: a literal value and a fromConfig/fromSecret source are mutually exclusive", comp, e.Name)
	}
	return nil
}

// ValidateComposedComponents enforces the composed-app contract: an app is
// composed when at least one component sets its own Template (each component is
// then a separate chart source in one multi-source Application). To keep
// rendering unambiguous, either NO component carries a Template (legacy
// single-chart app) or EVERY component does (fully composed) — a mix is
// rejected. Each composed component's Template must name a chart.
func ValidateComposedComponents(components []ComponentSpec) error {
	withTmpl := 0
	for _, c := range components {
		if c.Template != nil {
			withTmpl++
		}
	}
	if withTmpl == 0 {
		return nil // legacy single-chart app
	}
	if withTmpl != len(components) {
		return fmt.Errorf("composed app: every component must set its own template (%d of %d do); a mix of templated and inherited components is not supported",
			withTmpl, len(components))
	}
	for i, c := range components {
		if c.Template.Name == "" {
			return fmt.Errorf("components[%d] %q: template.name is required for a composed component", i, c.Name)
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
			"app has %d web components; an app may expose at most one HTTP surface — "+
				"split additional web components into their own apps and group them in a stack",
			count,
		)
	}
	return nil
}

// ValidateExposeModes enforces the routing-profile contract for an app's
// component list:
//
//  1. Every component's ExposeMode is one of the recognised values
//     (disabled/internal/external — empty is treated as disabled).
//  2. Each non-disabled mode resolves against the configured org/env
//     RoutingProfiles. Unknown modes fail loudly here at app save time
//     rather than silently dropping the ingress at gitops publish time.
//  3. At most one component is non-disabled — UNLESS the app is composed. A
//     single-chart app has one HTTP surface (one values.yaml → one routing.host),
//     so two exposed components would collide. A composed app renders each
//     component as its own Helm source with its own per-component values.yaml and
//     its own {app}-{component} host (helmvalues.MapComponentHelmValuesForEnv),
//     so multiple exposed components are fine and this limit is lifted.
//
// orgProfiles and envProfiles may both be nil — the validator treats them
// as empty maps. When both are empty, profile-lookup errors are skipped so
// callers that haven't yet configured profiles can still save apps whose
// components are all ExposeDisabled.
func ValidateExposeModes(components []ComponentSpec, orgProfiles, envProfiles RoutingProfiles) error {
	hasProfiles := len(orgProfiles) > 0 || len(envProfiles) > 0

	// Multi-component (composed) apps render one Ingress per component with a
	// distinct {app}-{component} host, so the single-HTTP-surface limit below does
	// not apply. In the unified model every component carries a Template, so the
	// signal is component COUNT (mirrors AppSpec.IsComposed), not "has a Template".
	composed := len(components) > 1

	exposed := 0
	for _, c := range components {
		if !c.ExposeMode.Valid() {
			return fmt.Errorf("component %q: invalid exposeMode %q", c.Name, c.ExposeMode)
		}
		if c.ExposeMode == ExposeDisabled || c.ExposeMode == "" {
			continue
		}
		exposed++
		if hasProfiles {
			// App-save validation has no cluster context (cluster routing is
			// resolved at publish time) — pass nil for the cluster layer.
			if _, err := ResolveRoutingProfile(orgProfiles, envProfiles, nil, c.ExposeMode); err != nil {
				return fmt.Errorf("component %q: %w", c.Name, err)
			}
		}
	}
	if exposed > 1 && !composed {
		return fmt.Errorf(
			"app has %d components with a non-disabled exposeMode; a single-chart app may expose at most one HTTP surface — "+
				"give each component its own template (composed app) for multiple HTTP surfaces, or split them into separate apps grouped in a stack",
			exposed,
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

// SanitizeAppName converts an arbitrary string into a best-effort valid DNS
// label for use as an app name. The transformation mirrors SanitizePreviewName
// but uses an "app-" prefix when the result would otherwise start with a digit,
// and falls back to "app" when the result is empty.
//
// Transformation steps:
//  1. Lowercase the input.
//  2. Replace every run of non-alphanumeric characters with a single hyphen.
//  3. Strip leading and trailing hyphens.
//  4. Prepend "app-" when the result starts with a digit.
//  5. Truncate to 63 characters, stripping any trailing hyphen left by the cut.
//
// Callers should validate the result with ValidateAppName to confirm it is
// usable (short or degenerate inputs may produce names that still fail the
// minimum-length rule).
func SanitizeAppName(raw string) string {
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
		return "app"
	}

	if result[0] >= '0' && result[0] <= '9' {
		result = "app-" + result
	}

	if len(result) > 63 {
		result = strings.TrimRight(result[:63], "-")
	}

	if result == "" {
		return "app"
	}
	return result
}

// AppPreviewNamespace returns the Kubernetes namespace for a preview instance
// of an app. Convention: {appName}-{previewName}.
//
// Deprecated: Use GenerateNamespace(appName, previewName, AppEnvPreview)
// instead. AppPreviewNamespace is retained for callers in the compat layer
// that have not yet migrated to the app-centric model.
func AppPreviewNamespace(appName, previewName string) string {
	return GenerateNamespace(appName, previewName, AppEnvPreview)
}

// ValidateNamespacePattern returns an error if pattern is not a valid
// namespace pattern. Rules:
//   - Must contain the {app} token (ensures uniqueness per app).
//   - Must only contain allowed tokens: {app}, {env}, {project}.
//   - When resolved with realistic inputs (appName=a, envName=b, projectName=c),
//     the result must be a valid DNS label (≤ 63 chars, lowercase alnum + hyphens).
//
// An empty pattern is valid and resolves to the default "{app}-{env}".
func ValidateNamespacePattern(pattern string) error {
	if pattern == "" {
		return nil // empty → default "{app}-{env}"
	}
	if !strings.Contains(pattern, "{app}") {
		return fmt.Errorf("namespace pattern %q must contain the {app} token", pattern)
	}
	// Check for unknown tokens: strip known ones and look for leftover braces.
	cleaned := pattern
	for _, tok := range []string{"{app}", "{env}", "{project}"} {
		cleaned = strings.ReplaceAll(cleaned, tok, "")
	}
	if strings.ContainsAny(cleaned, "{}") {
		return fmt.Errorf("namespace pattern %q contains unsupported tokens; allowed: {app}, {env}, {project}", pattern)
	}
	// Validate that a realistic resolution produces a valid DNS label.
	resolved := GenerateNamespaceFromPattern("app", "env", "project", pattern)
	if !dnsLabelRE.MatchString(resolved) {
		return fmt.Errorf("namespace pattern %q resolves to %q which is not a valid DNS label", pattern, resolved)
	}
	return nil
}

// ValidatePreviewNamespacePattern returns an error if pattern is not a valid
// preview namespace pattern. Rules:
//   - Must only contain allowed tokens: {project}, {app}, {name}.
//   - When resolved with realistic inputs the result must be a valid DNS label
//     (≤ 63 chars, lowercase alnum + hyphens).
//
// Omitting {name} is allowed: it yields a SHARED namespace for all of a
// project's previews (e.g. "{project}-previews"). The publisher then suffixes
// each preview's platform resources with the preview name to avoid collisions,
// and workloads should include ((platform.previewName)) in their resource names.
//
// An empty pattern is valid and resolves to DefaultPreviewNamespacePattern.
func ValidatePreviewNamespacePattern(pattern string) error {
	if pattern == "" {
		return nil // empty → DefaultPreviewNamespacePattern
	}
	// Check for unknown tokens: strip known ones and look for leftover braces.
	cleaned := pattern
	for _, tok := range []string{"{project}", "{app}", "{name}"} {
		cleaned = strings.ReplaceAll(cleaned, tok, "")
	}
	if strings.ContainsAny(cleaned, "{}") {
		return fmt.Errorf("preview namespace pattern %q contains unsupported tokens; allowed: {project}, {app}, {name}", pattern)
	}
	// Validate that a realistic resolution produces a valid DNS label.
	resolved := GeneratePreviewNamespaceFromPattern("app", "pr-1", "project", pattern)
	if !dnsLabelRE.MatchString(resolved) {
		return fmt.Errorf("preview namespace pattern %q resolves to %q which is not a valid DNS label", pattern, resolved)
	}
	return nil
}

// ValidateClusterName returns an error if name is not a valid cluster identifier.
// Cluster names follow the same DNS-label rules as app names.
func ValidateClusterName(name string) error {
	if name == "" {
		return fmt.Errorf("cluster name must not be empty")
	}
	if !dnsLabelRE.MatchString(name) {
		return fmt.Errorf("cluster name %q must be a valid DNS label: lowercase letters, digits, and hyphens, 2–63 characters, starting with a letter", name)
	}
	return nil
}

// ValidateAPIServerURL checks that a cluster API server URL is well-formed
// before it is stored. A malformed value (most often stray whitespace) is
// copied verbatim into ArgoCD AppProject/ApplicationSet destinations, where it
// silently breaks every app on that cluster with "cluster ... not found" — so
// reject it at the door. The caller should Cluster.Normalize() (trim) first;
// this catches the rest. Returns a message safe to show the user.
func ValidateAPIServerURL(raw string) error {
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("API server URL has leading or trailing whitespace")
	}
	if raw == "" {
		return fmt.Errorf("API server URL must not be empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("API server URL is not a valid URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("API server URL must use https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("API server URL must include a host (e.g. https://10.0.0.1:6443)")
	}
	return nil
}

// ValidateImageRepository checks that an app's image_repository value is a
// bare image reference (e.g. "ghcr.io/acme/web", "registry.example.com:5000/web"),
// not a scheme'd URL and without a tag or whitespace. A malformed value yields
// pods stuck in InvalidImageName/ErrImagePull that only surface at deploy time.
// Empty is allowed — many templates ship their own image.
func ValidateImageRepository(raw string) error {
	if raw == "" {
		return nil
	}
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("image_repository %q has leading or trailing whitespace", raw)
	}
	if strings.ContainsAny(raw, " \t") {
		return fmt.Errorf("image_repository %q must not contain spaces", raw)
	}
	if strings.Contains(raw, "://") {
		return fmt.Errorf("image_repository must be a bare image reference without a scheme (e.g. \"ghcr.io/acme/web\", not %q)", raw)
	}
	if strings.Contains(raw, "@") {
		return fmt.Errorf("image_repository %q must not include a digest; set the tag/digest at deploy time", raw)
	}
	// A tag belongs in image_tag, not the repository.
	if i := strings.LastIndex(raw, ":"); i > strings.LastIndex(raw, "/") && i != -1 {
		return fmt.Errorf("image_repository %q must not include a tag — use the separate image tag field", raw)
	}
	return nil
}

// ValidateConnectEndpoint checks that a 1Password Connect server URL is
// well-formed. The endpoint is an in-cluster address suparship cannot reach
// from the control plane, so this is a shape check, not a reachability probe:
// it catches typos (bad scheme, missing host, stray whitespace) that would
// otherwise only surface as an ESO "store not ready" much later. Empty is
// allowed (callers fall back to the org/built-in default).
func ValidateConnectEndpoint(raw string) error {
	if raw == "" {
		return nil
	}
	if strings.TrimSpace(raw) != raw {
		return fmt.Errorf("Connect server URL has leading or trailing whitespace")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("Connect server URL is not a valid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("Connect server URL must use http or https (got %q)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("Connect server URL must include a host (e.g. http://onepassword-connect.onepassword-connect.svc.cluster.local:8080)")
	}
	return nil
}
