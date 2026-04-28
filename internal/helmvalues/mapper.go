package helmvalues

import (
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/secrets"
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
	return MapToHelmValuesForEnv(app, envName, envType, baseDomain, "", "", secrets.ResourceNaming{}, "")
}

// MapToHelmValuesForEnv is the canonical mapper. The naming and orgName
// arguments mirror what the publisher uses to render ExternalSecret /
// ConfigMap names in gitops-output, so values.yaml's
// suparship.secretName / configName always match the K8s resource the
// platform-managed publisher actually creates.
//
// Cluster is used only for the cluster-scope envFrom name; pass "" for
// unbound envs (the cluster scope is then omitted from the lists).
//
// Namespace is plumbed for backwards-compatibility (callers that have it
// pass it; the mapper does not currently use it for naming — names come
// from the configurable ResourceNaming patterns).
func MapToHelmValuesForEnv(
	app *domain.App,
	envName string,
	envType domain.AppEnvironmentType,
	baseDomain, namespace, cluster string,
	naming secrets.ResourceNaming,
	orgName string,
) HelmValues {
	if baseDomain == "" {
		baseDomain = "localhost"
	}
	if orgName == "" {
		orgName = "default"
	}

	envOverride := app.Spec.EnvironmentDefaults[envName] // zero value if absent

	imageRepo, imageTag := extractImage(app.Spec.Values)

	components := buildComponents(app.Spec.Components, envType, envOverride, imageRepo, imageTag)

	routingComponent := resolveRoutingComponent(app.Spec.Components)
	routingHost := stripScheme(domain.GenerateURLWithDomain(app.Name, envName, envType, baseDomain))

	// Resource names come from the same ResourceNaming patterns the
	// publisher uses, so values.yaml lines up with the K8s resources
	// actually created — RenderAppResource → ExternalSecret target,
	// RenderAppConfigMap → published ConfigMap.
	np := secrets.NamingParams{
		Org:     orgName,
		Env:     envName,
		Project: app.ProjectName,
		App:     app.Name,
		Cluster: cluster,
	}
	cms, secs := envFromLists(
		app.ProjectName, app.Name, envName, string(envType), cluster,
		naming.RenderAppResource(np),
		naming.RenderAppConfigMap(np),
	)

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
		EnvLayers: envconfig.ToHelmEnvLayers(envconfig.EnvLayers{
			App:    app.Spec.EnvConfig,
			AppEnv: envOverride.EnvConfig,
		}),
		Suparship: SuparshipValues{
			EnvFromConfigMaps: cms,
			EnvFromSecrets:    secs,
		},
	}
}

// envFromLists returns the full hierarchy of ConfigMap and Secret names the
// chart should envFrom, in precedence order — org → env-type → project → app
// → app-env → cluster. Names match what the suparship platform-managed path
// writes (publisher commits + K8s upper-level writer + Stakater Replicator):
// the chart needs no string concatenation or backend awareness, just iterate.
//
// The most-specific app-env entry includes both names that may exist:
//   - appEnvESOName  — the ExternalSecret target (RenderAppResource), which
//     ESO materialises in the env namespace on the 1Password backend.
//   - appEnvK8sName  — the suparship-system K8s Secret name written by the
//     UpperLevelSecretWriter on the K8s backend, replicated by Stakater into
//     the env namespace.
//
// On a given backend only one of these actually exists; the other resolves
// to a no-op via `optional: true` in the chart's envFrom block. When the
// operator has customised RenderAppResource so the two names coincide, only
// one entry is emitted.
//
// cluster=="" omits the cluster-scope tail (env unbound).
func envFromLists(project, app, envName, envType, cluster, appEnvESOName, appEnvESOConfigName string) ([]string, []string) {
	envvarsKey := envType
	if envvarsKey == "" {
		envvarsKey = envName
	}
	appEnvK8sName := secrets.AppEnvSecretName(project, app, envName)
	appEnvK8sConfigName := secrets.AppConfigName(project, app, envName)

	cms := []string{
		"suparship-envvars-org",
		"suparship-envvars-env-" + envvarsKey,
		"suparship-envvars-project-" + project,
		"suparship-envvars-app-" + project + "-" + app,
		appEnvESOConfigName,
	}
	if appEnvK8sConfigName != appEnvESOConfigName {
		cms = append(cms, appEnvK8sConfigName)
	}

	secs := []string{
		"suparship-secrets-org",
		"suparship-secrets-envtype-" + envvarsKey,
		"suparship-secrets-project-" + project,
		"suparship-secrets-app-" + project + "-" + app,
		appEnvESOName,
	}
	if appEnvK8sName != appEnvESOName {
		secs = append(secs, appEnvK8sName)
	}

	if cluster != "" {
		cms = append(cms, "suparship-envvars-cluster-"+cluster)
		secs = append(secs, "suparship-secrets-cluster-"+cluster)
	}
	return cms, secs
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
