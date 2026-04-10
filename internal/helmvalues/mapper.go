package helmvalues

import (
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/domain"
)

// defaultReplicas is the replica count used when a ComponentSpec does not
// set an explicit value and no environment override is present.
const defaultReplicas int32 = 2

// defaultPreviewReplicas is used instead of defaultReplicas for preview
// environments to minimise resource usage.
const defaultPreviewReplicas int32 = 1

// imageRepositoryKey and imageTagKey are the AppSpec.Values keys that carry
// the primary container image coordinates. They mirror the template.yaml
// input names used by the built-in web-service template.
const (
	imageRepositoryKey = "image_repository"
	imageTagKey        = "image_tag"
)

// MapToHelmValues derives a deterministic HelmValues from an App and a
// target environment using the default "localhost" base domain.
// It is a pure function: same inputs always produce the same output.
//
// For environments with a custom baseDomain (e.g. "staging.acme.com"), use
// MapToHelmValuesWithDomain so that routing.host reflects the real hostname.
//
// Mapping rules:
//
//   - app.name  ← app.Name
//   - app.env   ← envName
//
//   - For each ComponentSpec in app.Spec.Components (processed in name order):
//   - enabled   ← ComponentSpec.Enabled; additionally false when
//     envType==preview and ComponentSpec.PreviewEnabled==false
//   - image     ← AppSpec.Values["image_repository"] / ["image_tag"]
//     (shared across all components in MVP; per-component overrides future)
//   - replicas  ← ComponentSpec.Replicas → envOverride.Replicas → default
//   - expose    ← ComponentSpec.Expose
//   - env       ← ComponentSpec.Config merged with envOverride.Config
//     (override wins on key conflict)
//   - resources ← ComponentSpec.SizePreset → envOverride.SizePreset
//     (omitted entirely when no preset is set)
//
//   - routing.host      ← domain.GenerateURLWithDomain(app.Name, envName, envType, baseDomain)
//   - routing.component ← first exposed component (alphabetical; falls back
//     to first web component, then to first component overall)
//
// Environment overrides are taken from app.Spec.EnvironmentDefaults[envName].
// They apply uniformly to all components in that environment.
func MapToHelmValues(app *domain.App, envName string, envType domain.AppEnvironmentType) HelmValues {
	return MapToHelmValuesWithDomain(app, envName, envType, "localhost")
}

// MapToHelmValuesWithDomain is like MapToHelmValues but derives routing.host
// from the provided baseDomain instead of the default "localhost".
//
// Use this when the environment has a registered cluster with a known
// baseDomain (e.g. "staging.acme.com"), so that the Ingress host in the
// generated chart values reflects the real cluster URL.
//
// When baseDomain is empty, "localhost" is used.
func MapToHelmValuesWithDomain(app *domain.App, envName string, envType domain.AppEnvironmentType, baseDomain string) HelmValues {
	if baseDomain == "" {
		baseDomain = "localhost"
	}

	envOverride := app.Spec.EnvironmentDefaults[envName] // zero value if absent

	imageRepo, imageTag := extractImage(app.Spec.Values)

	components := buildComponents(app.Spec.Components, envType, envOverride, imageRepo, imageTag)

	routingComponent := resolveRoutingComponent(app.Spec.Components)
	routingHost := stripScheme(domain.GenerateURLWithDomain(app.Name, envName, envType, baseDomain))

	return HelmValues{
		App: AppContext{
			Name: app.Name,
			Env:  envName,
		},
		Components: components,
		Routing: RoutingValues{
			Host:      routingHost,
			Component: routingComponent,
		},
	}
}

// extractImage pulls the image repository and tag from an AppSpec Values map.
// Returns empty strings when the keys are absent or not string-typed.
func extractImage(values map[string]any) (repository, tag string) {
	if r, ok := values[imageRepositoryKey].(string); ok {
		repository = r
	}
	if t, ok := values[imageTagKey].(string); ok {
		tag = t
	}
	return
}

// buildComponents constructs the component map in sorted-name order.
func buildComponents(
	specs []domain.ComponentSpec,
	envType domain.AppEnvironmentType,
	envOverride domain.EnvironmentOverride,
	imageRepo, imageTag string,
) map[string]*ComponentValues {
	if len(specs) == 0 {
		return map[string]*ComponentValues{}
	}

	// Sort by name for deterministic output regardless of slice order in AppSpec.
	sorted := make([]domain.ComponentSpec, len(specs))
	copy(sorted, specs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	components := make(map[string]*ComponentValues, len(sorted))
	for _, c := range sorted {
		components[c.Name] = buildComponentValues(c, envType, envOverride, imageRepo, imageTag)
	}
	return components
}

// buildComponentValues resolves a single ComponentSpec into ComponentValues,
// applying env-level overrides where applicable.
func buildComponentValues(
	c domain.ComponentSpec,
	envType domain.AppEnvironmentType,
	envOverride domain.EnvironmentOverride,
	imageRepo, imageTag string,
) *ComponentValues {
	enabled := c.Enabled
	if envType == domain.AppEnvPreview && !c.PreviewEnabled {
		enabled = false
	}

	replicas := resolveReplicas(c.Replicas, envOverride.Replicas, envType)

	env := mergeConfig(c.Config, envOverride.Config)

	sizePreset := string(c.SizePreset)
	if envOverride.SizePreset != "" {
		sizePreset = string(envOverride.SizePreset)
	}

	cv := &ComponentValues{
		Enabled: enabled,
		Image: ImageValues{
			Repository: imageRepo,
			Tag:        imageTag,
		},
		Replicas: replicas,
		Expose:   c.Expose,
	}
	if len(env) > 0 {
		cv.Env = env
	}
	if sizePreset != "" {
		cv.Resources = &ResourceValues{Size: sizePreset}
	}
	return cv
}

// resolveReplicas applies the precedence chain:
//
//	componentReplicas (if > 0) → envReplicas (if > 0) → env-type default
func resolveReplicas(componentReplicas, envReplicas int32, envType domain.AppEnvironmentType) int32 {
	if componentReplicas > 0 {
		return componentReplicas
	}
	if envReplicas > 0 {
		return envReplicas
	}
	if envType == domain.AppEnvPreview {
		return defaultPreviewReplicas
	}
	return defaultReplicas
}

// mergeConfig produces a new map that contains all keys from base, with keys
// from override added or overwriting where they conflict. Returns nil when
// both inputs are empty to avoid emitting an empty map in YAML output.
func mergeConfig(base, override map[string]string) map[string]string {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(override))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged
}

// resolveRoutingComponent picks the name of the primary exposed component.
//
// Selection order (all candidates sorted alphabetically for determinism):
//  1. First component where Expose == true.
//  2. First component where Type == ComponentWeb.
//  3. First component alphabetically (ultimate fallback).
//
// Returns an empty string when specs is empty.
func resolveRoutingComponent(specs []domain.ComponentSpec) string {
	if len(specs) == 0 {
		return ""
	}

	sorted := make([]domain.ComponentSpec, len(specs))
	copy(sorted, specs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	for _, c := range sorted {
		if c.Expose {
			return c.Name
		}
	}
	for _, c := range sorted {
		if c.Type == domain.ComponentWeb {
			return c.Name
		}
	}
	return sorted[0].Name
}

// stripScheme removes the leading "http://" or "https://" scheme from a URL
// so that routing.host contains only the hostname (as expected by Ingress).
func stripScheme(url string) string {
	if after, ok := strings.CutPrefix(url, "https://"); ok {
		return after
	}
	if after, ok := strings.CutPrefix(url, "http://"); ok {
		return after
	}
	return url
}
