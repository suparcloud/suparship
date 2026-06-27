package helmvalues

import (
	"fmt"
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

// AppSpec.Values keys that carry well-known overrides exposed by the
// built-in templates' inputs. Mirrored here so the mapper can plumb
// them through to the canonical helmvalues schema (charts read
// `components.<name>.{image,port,healthCheck.path}`); without this
// translation the template inputs would be set in suparship's spec but
// silently dropped on their way to Helm.
const (
	imageRepositoryKey = "image_repository"
	imageTagKey        = "image_tag"
	portKey            = "port"
	healthPathKey      = "health_path"
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
//
//   - app.env   ← envName
//
//   - For each ComponentSpec in app.Spec.Components (processed in name order):
//
//   - enabled   ← ComponentSpec.Enabled (a preview renders the same enabled
//     components as its base env)
//
//   - image     ← AppSpec.Values["image_repository"] / ["image_tag"]
//     (shared across all components in MVP; per-component overrides future)
//
//   - replicas  ← ComponentSpec.Replicas → envOverride.Replicas → default
//
//   - ingress   ← ResolveRoutingProfile(orgProfiles, envProfiles,
//     ComponentSpec.ExposeMode); only set on the routing component
//
//   - env       ← ComponentSpec.Config merged with envOverride.Config
//     (override wins on key conflict)
//
//   - resources ← ComponentSpec.SizePreset → envOverride.SizePreset
//     (omitted entirely when no preset is set)
//
//   - routing.host      ← domain.GenerateURLWithDomain(app.Name, envName, envType, baseDomain)
//
//   - routing.component ← first exposed component (alphabetical; falls back
//     to first web component, then to first component overall)
//
// Environment overrides are taken from app.Spec.EnvironmentDefaults[envName].
// For preview environments the reserved EnvironmentDefaults[PreviewOverrideKey]
// ("preview") band is used instead (the preview name is never an override key).
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
//
// Routing profiles default to nil. Components that target a non-disabled
// ExposeMode without a configured profile will produce no Ingress; callers
// that want strict validation should use MapToHelmValuesForEnv with real
// profiles after running domain.ValidateExposeModes.
func MapToHelmValuesWithDomain(app *domain.App, envName string, envType domain.AppEnvironmentType, baseDomain string) HelmValues {
	return MapToHelmValuesForEnv(app, envName, envType, baseDomain, "", "", "", nil, nil, nil, nil, nil)
}

// MapToHelmValuesForEnv is the canonical mapper. The envFrom lists reference
// the single per-app objects the platform publisher materializes — one Secret
// (<app>-secrets) and one ConfigMap (<app>-config) — into which all scopes
// (global/env/cluster) are merged, so values.yaml always matches the published
// resources regardless of backend.
//
// cluster and namespace are accepted for caller compatibility but no longer
// affect the (deterministic, single-name) envFrom lists.
//
// orgProfiles and envProfiles drive the per-component IngressValues output.
// envProfiles entries override orgProfiles by mode name (sparse). When the
// resolved routing component has ExposeMode==Disabled (or empty) no Ingress
// is emitted, regardless of the profiles configured.
func MapToHelmValuesForEnv(
	app *domain.App,
	envName string,
	envType domain.AppEnvironmentType,
	baseDomain, namespace, cluster string,
	orgName string,
	orgProfiles, envProfiles, clusterProfiles domain.RoutingProfiles,
	orgAddonProfiles, envAddonProfiles domain.AddonProfiles,
) HelmValues {
	if baseDomain == "" {
		baseDomain = "localhost"
	}
	if orgName == "" {
		orgName = "default"
	}

	// Preview envs apply the reserved "preview" band; their per-preview name
	// (e.g. "pr-42") is never an EnvironmentDefaults key.
	overrideKey := envName
	if envType == domain.AppEnvPreview {
		overrideKey = domain.PreviewOverrideKey
	}
	envOverride := app.Spec.EnvironmentDefaults[overrideKey] // zero value if absent
	// Per-cluster overrides (deployMode "all"): fold the cluster's override on
	// top of the env override so its replicas/size/config/values win for this
	// cluster only. cluster=="" (single-cluster / active mode) is a no-op.
	if cluster != "" {
		if co, ok := envOverride.ClusterOverrides[cluster]; ok {
			envOverride = applyClusterOverride(envOverride, co)
		}
	}

	imageRepo, imageTag := extractImage(app.Spec.Values)
	port := extractPort(app.Spec.Values)
	healthPath := extractHealthPath(app.Spec.Values)

	routingComponent := resolveRoutingComponent(app.Spec.Components)
	components := buildComponents(app.Spec.Components, envType, envOverride, imageRepo, imageTag, port, healthPath, routingComponent, orgProfiles, envProfiles, clusterProfiles)

	// The app's URL uses the routing component's resolved profile base domain
	// when set (cluster → env → org), so a per-cluster baseDomain yields a
	// cluster-specific host; otherwise the caller-supplied baseDomain (the
	// cluster default or env default) is used.
	effectiveBase := baseDomain
	for _, c := range app.Spec.Components {
		if c.Name != routingComponent {
			continue
		}
		if prof, err := domain.ResolveRoutingProfile(orgProfiles, envProfiles, clusterProfiles, c.ExposeMode); err == nil && prof.BaseDomain != "" {
			effectiveBase = prof.BaseDomain
		}
		break
	}
	routingHost := stripScheme(domain.GenerateURLWithDomain(app.Name, envName, envType, effectiveBase))

	// Resource names are deterministic so values.yaml lines up with the K8s
	// resources the publisher creates: the per-app ConfigMap and the three
	// per-scope ExternalSecret targets (<app>-global/-env/-cluster).
	cms, secs := envFromLists(app.ProjectName, app.Name, envName, cluster)

	// Addon connection Secrets append to the consumer's envFrom hierarchy
	// after the standard scopes. Order is alphabetical by addon name so
	// the merge result is deterministic; rely on the chart-side
	// "later wins" envFrom semantics so addon vars override the
	// generic env hierarchy when they collide.
	addonSecs, claims := buildAddonBindings(app, envAddonProfiles, orgAddonProfiles)
	secs = append(secs, addonSecs...)

	// Platform metadata block: identity + resolved routing context. Ingress
	// class/issuer come from the routing component's resolved profile (already
	// computed into its ComponentValues.Ingress above).
	platform := PlatformValues{
		Org:         orgName,
		Project:     app.ProjectName,
		App:         app.Name,
		Env:         envName,
		EnvType:     string(envType),
		Cluster:     cluster,
		Namespace:   namespace,
		BaseDomain:  effectiveBase,
		RoutingHost: routingHost,
		ImageTag:    imageTag,
	}
	if rc := components[routingComponent]; rc != nil && rc.Ingress != nil {
		platform.IngressClassName = rc.Ingress.ClassName
		platform.ClusterIssuer = rc.Ingress.ClusterIssuer
	}

	return HelmValues{
		App: AppContext{
			Name: app.Name,
			Env:  envName,
		},
		Platform:   platform,
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
		ServiceClaims: claims,
	}
}

// addonSecretName returns the deterministic name of the connection
// Secret an addon wrapper produces and the consumer app envFroms. The
// pattern keeps the addon's contributing scope visible in the Secret
// name itself ("foo-addon-cache-conn") so cluster operators can grep
// for it without having to know the binding mechanism.
func addonSecretName(appName, addonName string) string {
	return fmt.Sprintf("%s-addon-%s-conn", appName, addonName)
}

// buildAddonBindings resolves each addon claim against the org/env
// AddonProfiles catalog and returns:
//
//   - the list of connection-Secret names to append to the consumer's
//     suparship.envFromSecrets[] (sorted by addon name).
//   - the per-claim ServiceClaimValues, exposed in HelmValues for
//     observability + future per-component scoping. Implicit fan-out
//     today: every addon binds to every component, so Component is
//     left empty in each entry.
//
// Claims whose type cannot be resolved are skipped silently so an
// app save doesn't fail at render time when the org catalog hasn't
// caught up with a new addon type — domain-level Validate enforces
// the invariant earlier in the lifecycle.
func buildAddonBindings(app *domain.App, envAddons, orgAddons domain.AddonProfiles) ([]string, []ServiceClaimValues) {
	addons := app.Spec.Addons
	if len(addons) == 0 {
		return nil, nil
	}
	// Sort by name for determinism.
	sorted := make([]domain.AddonSpec, len(addons))
	copy(sorted, addons)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	secs := make([]string, 0, len(sorted))
	claims := make([]ServiceClaimValues, 0, len(sorted))
	for _, a := range sorted {
		if _, err := domain.ResolveAddonProfile(orgAddons, envAddons, a.Type); err != nil {
			// Unresolvable claim — skip rather than fail. App-level
			// Validate is the gate; the mapper is best-effort.
			continue
		}
		name := addonSecretName(app.Name, a.Name)
		secs = append(secs, name)
		claims = append(claims, ServiceClaimValues{
			Addon:      a.Name,
			SecretName: name,
			Type:       a.Type,
		})
	}
	return secs, claims
}

// envFromLists returns the names the chart should envFrom for this app-env,
// backend-agnostic. ESO materializes three per-scope Secrets in the app
// namespace — <app>-global, <app>-env, <app>-cluster — each merging the
// org-admin shared item and the app's own item for that scope.
//
// One ConfigMap (<app>-config) and one Secret (<app>-secrets) per app-env. The
// platform merges all scopes (global/env/cluster) into each, so the chart
// envFroms exactly these two names. Both are optional:true so the pod starts
// before ESO populates the Secret.
func envFromLists(_ /*project*/, app, _ /*envName*/, _ /*cluster*/ string) ([]string, []string) {
	cms := []string{secrets.AppConfigMapName(app)}
	secs := []string{secrets.AppSecretName(app)}
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

// extractPort pulls the container port from an AppSpec Values map.
// Returns 0 when absent — callers treat 0 as "let chart default win".
// YAML decoders commonly produce float64 for plain numbers, so accept
// both.
func extractPort(values map[string]any) int32 {
	switch v := values[portKey].(type) {
	case int:
		return int32(v)
	case int64:
		return int32(v)
	case float64:
		return int32(v)
	}
	return 0
}

// extractHealthPath pulls the liveness/readiness probe HTTP path from
// an AppSpec Values map. Empty string = "let chart default win" so
// every existing app keeps using "/healthz" (or whatever the chart
// declares) until an operator opts in.
func extractHealthPath(values map[string]any) string {
	if s, ok := values[healthPathKey].(string); ok {
		return s
	}
	return ""
}

// buildComponents constructs the component map in sorted-name order.
// port and healthPath are app-level inputs that we apply only to the
// routingComponent (the externally-exposed one) — multi-component apps
// would otherwise inherit a port that's only meaningful for one of them.
// Empty/zero values mean "leave unset; chart's own default applies."
//
// orgProfiles and envProfiles are forwarded verbatim to buildComponentValues
// so each component's Ingress field reflects the resolved RoutingProfile
// for its EffectiveExposeMode. Only the routing component gets an Ingress —
// non-routing components never own an external entry point in MVP.
func buildComponents(
	specs []domain.ComponentSpec,
	envType domain.AppEnvironmentType,
	envOverride domain.EnvironmentOverride,
	imageRepo, imageTag string,
	port int32,
	healthPath string,
	routingComponent string,
	orgProfiles, envProfiles, clusterProfiles domain.RoutingProfiles,
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
		var p int32
		var hp string
		isRouting := c.Name == routingComponent
		if isRouting {
			p, hp = port, healthPath
		}
		components[c.Name] = buildComponentValues(c, envType, envOverride, imageRepo, imageTag, p, hp, isRouting, orgProfiles, envProfiles, clusterProfiles)
	}
	return components
}

// buildComponentValues resolves a single ComponentSpec into ComponentValues,
// applying env-level overrides where applicable.
//
// isRouting indicates whether this component is the chosen routing component
// (only it gets an Ingress). orgProfiles and envProfiles drive the resolved
// IngressValues; when both are nil the legacy fallback synthesises
// IngressValues{ClassName: "nginx"} from Expose=true so charts that gate on
// .Ingress instead of .Expose render identically against pre-RoutingProfile
// AppSpecs.
func buildComponentValues(
	c domain.ComponentSpec,
	envType domain.AppEnvironmentType,
	envOverride domain.EnvironmentOverride,
	imageRepo, imageTag string,
	port int32,
	healthPath string,
	isRouting bool,
	orgProfiles, envProfiles, clusterProfiles domain.RoutingProfiles,
) *ComponentValues {
	enabled := c.Enabled

	replicas := resolveReplicas(c.Replicas, envOverride.Replicas, envType)

	// Per-component, per-env override layered on top of the app-level spec.
	co := envOverride.Components[c.Name]

	// env: component Config + env-wide Config + per-(env,component) override.
	env := mergeConfig(mergeConfig(c.Config, envOverride.Config), co.Env)

	sizePreset := string(c.SizePreset)
	if envOverride.SizePreset != "" {
		sizePreset = string(envOverride.SizePreset)
	}

	// Raw resources: per-env override wins over app-level; both win over size.
	rawRes := c.Resources
	if co.Resources != nil {
		rawRes = co.Resources
	}

	// envFrom name lists: per-env override replaces app-level when set.
	secrets := pickStrings(co.EnvFromSecrets, c.EnvFromSecrets)
	configMaps := pickStrings(co.EnvFromConfigMaps, c.EnvFromConfigMaps)

	// Scaling: per-env override wins over app-level.
	scaling := c.Scaling
	if co.Scaling != nil {
		scaling = co.Scaling
	}

	cv := &ComponentValues{
		Enabled: enabled,
		Image: ImageValues{
			Repository: imageRepo,
			Tag:        imageTag,
		},
		Replicas: replicas,
	}
	if len(env) > 0 {
		cv.Env = env
	}
	switch {
	case rawRes != nil && (len(rawRes.Requests) > 0 || len(rawRes.Limits) > 0):
		cv.Resources = &ResourceValues{Requests: rawRes.Requests, Limits: rawRes.Limits}
	case sizePreset != "":
		cv.Resources = &ResourceValues{Size: sizePreset}
	}
	if ef := buildEnvFrom(configMaps, secrets); len(ef) > 0 {
		cv.EnvFrom = ef
	}
	if scaling != nil && (len(scaling.Triggers) > 0 || scaling.MinReplicas != nil || scaling.MaxReplicas != nil) {
		cv.Autoscaling = &ComponentAutoscaling{
			Triggers:        scaling.Triggers,
			MinReplicaCount: scaling.MinReplicas,
			MaxReplicaCount: scaling.MaxReplicas,
		}
	}
	if port > 0 {
		cv.Port = port
	}
	if healthPath != "" {
		cv.HealthCheck = &HealthCheckValues{Path: healthPath}
	}
	if isRouting {
		cv.Ingress = resolveIngress(c, orgProfiles, envProfiles, clusterProfiles)
	}
	return cv
}

// pickStrings returns override when non-empty, else base (per-env replaces
// app-level for list-valued fields).
func pickStrings(override, base []string) []string {
	if len(override) > 0 {
		return override
	}
	return base
}

// buildEnvFrom converts configmap-name then secret-name lists into the chart's
// envFrom entries (all optional so pods start before sources exist).
func buildEnvFrom(configMaps, secrets []string) []EnvFromSource {
	out := make([]EnvFromSource, 0, len(configMaps)+len(secrets))
	for _, n := range configMaps {
		out = append(out, EnvFromSource{ConfigMapRef: &EnvFromRef{Name: n, Optional: true}})
	}
	for _, n := range secrets {
		out = append(out, EnvFromSource{SecretRef: &EnvFromRef{Name: n, Optional: true}})
	}
	return out
}

// resolveIngress turns a component's exposure intent into the IngressValues
// that templates consume. Resolves c.ExposeMode against the org/env profile
// maps via domain.ResolveRoutingProfile and emits className + clusterIssuer
// from the resolved profile.
//
// Returns nil for disabled modes, missing profiles, and resolution errors —
// the mapper stays a pure derivation, so a misconfigured profile drops the
// ingress silently rather than blocking chart render. Validation is the
// caller's job (domain.ValidateExposeModes runs in the app-save handler).
func resolveIngress(c domain.ComponentSpec, orgProfiles, envProfiles, clusterProfiles domain.RoutingProfiles) *IngressValues {
	mode := c.ExposeMode
	if mode == domain.ExposeDisabled || mode == "" {
		return nil
	}
	profile, err := domain.ResolveRoutingProfile(orgProfiles, envProfiles, clusterProfiles, mode)
	if err != nil {
		return nil
	}
	if profile.IngressClassName == "" {
		return nil
	}
	return &IngressValues{
		ClassName:     profile.IngressClassName,
		ClusterIssuer: profile.ClusterIssuer,
	}
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
// applyClusterOverride returns a copy of the env override with the per-cluster
// override folded on top: non-zero scalar fields win, maps are shallow-merged
// (cluster keys overwrite env keys). EnvConfig is left to the env layer (per-
// cluster env vars/secrets flow through the separate cluster-scope mechanism).
func applyClusterOverride(env domain.EnvironmentOverride, co domain.ClusterValueOverride) domain.EnvironmentOverride {
	if co.Replicas != 0 {
		env.Replicas = co.Replicas
	}
	if co.SizePreset != "" {
		env.SizePreset = co.SizePreset
	}
	env.Config = mergeConfig(env.Config, co.Config)
	env.Values = mergeValues(env.Values, co.Values)
	return env
}

// mergeValues shallow-merges two template-input maps; override keys win.
func mergeValues(base, override map[string]any) map[string]any {
	if len(base) == 0 && len(override) == 0 {
		return nil
	}
	merged := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged
}

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
//  1. First component with ExposeMode == ExposeExternal — public face wins
//     over internal so a mixed app routes its public component.
//  2. First component with ExposeMode == ExposeInternal.
//  3. First component where Type == ComponentWeb. Used when every component
//     is disabled — the routing host still goes into values.yaml but no
//     ingress is emitted because resolveIngress returns nil for disabled.
//  4. First component alphabetically (ultimate fallback).
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
		if c.ExposeMode == domain.ExposeExternal {
			return c.Name
		}
	}
	for _, c := range sorted {
		if c.ExposeMode == domain.ExposeInternal {
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

// MapAddonToHelmValuesForEnv returns the values shape passed to an
// addon wrapper chart's Helm release for one (app, env, addon) tuple.
//
// Publisher calls this once per addon claim per env and writes the
// result under <app>/<env>/addons/<name>/values.yaml; the existing
// ApplicationSet generator picks it up as a parallel Application.
//
// Defaults from AddonProfile are merged with per-app overrides
// (AppSpec.Addons[].Values wins on conflict). Returns an error when
// the addon's type cannot be resolved through the org/env profile
// catalog.
func MapAddonToHelmValuesForEnv(
	app *domain.App,
	addon domain.AddonSpec,
	envName string,
	envType domain.AppEnvironmentType,
	cluster string,
	orgName string,
	orgAddons, envAddons domain.AddonProfiles,
) (AddonInstanceValues, error) {
	if orgName == "" {
		orgName = "default"
	}
	profile, err := domain.ResolveAddonProfile(orgAddons, envAddons, addon.Type)
	if err != nil {
		return AddonInstanceValues{}, fmt.Errorf("addon %q: %w", addon.Name, err)
	}

	// envFrom hierarchy is shared with the consumer app — the wrapper
	// can opt into it but isn't required to.
	cms, secs := envFromLists(app.ProjectName, app.Name, envName, cluster)

	return AddonInstanceValues{
		App: AppContext{
			Name: app.Name,
			Env:  envName,
		},
		Addon: AddonValues{
			Enabled:    true,
			Type:       addon.Type,
			Provider:   profile.Provider,
			Size:       addon.Size,
			Version:    addon.Version,
			SecretName: addonSecretName(app.Name, addon.Name),
		},
		Suparship: SuparshipValues{
			EnvFromConfigMaps: cms,
			EnvFromSecrets:    secs,
		},
	}, nil
}
