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
	return MapToHelmValuesForEnv(app, envName, envType, baseDomain, "", "", secrets.ResourceNaming{}, "", "")
}

// MapToHelmValuesForEnv is the canonical mapper. The naming and orgName
// arguments mirror what the publisher uses to render ExternalSecret /
// ConfigMap names in gitops-output, so values.yaml's envFrom lists always
// match the K8s resources the platform-managed publisher actually creates.
//
// backend selects which app-env name appears in the envFrom lists:
//   - secrets.Backend1Password → ESO-materialised target name from
//     RenderAppResource (the only Secret that exists on this backend).
//   - secrets.BackendK8s       → suparship-system-replicated name from
//     AppEnvSecretName / AppConfigName (the only Secret/ConfigMap that
//     exists on this backend).
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
	backend secrets.BackendType,
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
		app.ProjectName, app.Name, envName, cluster,
		naming.RenderAppResource(np),
		naming.RenderAppConfigMap(np),
		backend,
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

// envFromLists returns the names the chart should envFrom for this app-env.
// Both lists collapse to a single entry on ESO-mediated backends because the
// publisher pre-merges all six scopes into one ConfigMap and one Secret — the
// committed YAML in gitops-output is the audit-trail for what the pod sees.
//
// envFromConfigMaps — always one entry: the per-app "{app}-config" ConfigMap
// the publisher writes with org → env-type → project → app → app-env → cluster
// merged in precedence order. There is no chart-side multi-source merge.
//
// envFromSecrets — backend-specific:
//   - 1Password (and any ESO-mediated backend): ESO merges all six scopes into
//     a single K8s Secret named by RenderAppResource (e.g. "{app}-secrets").
//     The chart envFroms only that one Secret.
//   - K8s: there is no ESO collapse. The K8s UpperLevelSecretWriter writes
//     a Secret per scope into suparship-system; Stakater Replicator copies
//     each into the env namespace under the same name. The chart envFroms
//     all six.
//
// cluster=="" omits the cluster-scope tail of the K8s Secret list (env unbound).
func envFromLists(project, app, envName, cluster, appEnvESOName, appEnvESOConfigName string, backend secrets.BackendType) ([]string, []string) {
	cms := []string{appEnvESOConfigName}

	var secs []string
	switch backend {
	case secrets.BackendK8s:
		// One replicated Secret per scope.
		envvarsKey := envName
		secs = []string{
			"suparship-secrets-org",
			"suparship-secrets-envtype-" + envvarsKey,
			"suparship-secrets-project-" + project,
			"suparship-secrets-app-" + project + "-" + app,
			secrets.AppEnvSecretName(project, app, envName),
		}
		if cluster != "" {
			secs = append(secs, "suparship-secrets-cluster-"+cluster)
		}
	default:
		// 1Password / any ESO-mediated backend: ESO collapses all six
		// scopes into a single Secret.
		secs = []string{appEnvESOName}
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
