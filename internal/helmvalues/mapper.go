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
	return MapToHelmValuesForEnv(app, envName, envType, baseDomain, "", "", "", nil, nil, nil)
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

	// The app's base config + secret travel via the platform contract
	// (platform.configMapName / platform.secretName) — the ONLY names a chart
	// needs. suparship renders the objects behind them. The publisher overrides
	// these per component for a curated / opt-out component (its own projection /
	// "" for no secrets).

	// Platform metadata block: identity + resolved routing context. Ingress
	// class/issuer come from the routing component's resolved profile (already
	// computed into its ComponentValues.Ingress above).
	platform := PlatformValues{
		Org:           orgName,
		Project:       app.ProjectName,
		App:           app.Name,
		Env:           envName,
		EnvType:       string(envType),
		Cluster:       cluster,
		Namespace:     namespace,
		BaseDomain:    effectiveBase,
		RoutingHost:   routingHost,
		ImageTag:      imageTag,
		ConfigMapName: secrets.AppConfigMapName(app.Name),
		SecretName:    secrets.AppSecretName(app.Name),
	}
	if rc := components[routingComponent]; rc != nil && rc.Ingress != nil {
		platform.IngressClassName = rc.Ingress.ClassName
		platform.ClusterIssuer = rc.Ingress.ClusterIssuer
	}

	// Per-tier routing context: resolve the "internal" and "external" profiles
	// directly (cluster → env → org) and expose them, independent of the app's
	// single routing component. A tier's base domain falls back to the profile's
	// own baseDomain, then to the (env, cluster) base domain passed in — so a
	// per-cluster base-domain override reaches ((platform.{tier}BaseDomain)) even
	// when the tier's profile leaves baseDomain blank. A tier with no profile
	// configured leaves its fields empty.
	resolveTier := func(mode domain.ExposeMode) (dom, class, issuer, gwName, gwNS, gwSection string) {
		prof, err := domain.ResolveRoutingProfile(orgProfiles, envProfiles, clusterProfiles, mode)
		if err != nil {
			return "", "", "", "", "", ""
		}
		dom = prof.BaseDomain
		if dom == "" {
			dom = baseDomain
		}
		class = prof.IngressClassName
		issuer = prof.ClusterIssuer
		if prof.Gateway != nil {
			gwName = prof.Gateway.Name
			gwNS = prof.Gateway.Namespace
			gwSection = prof.Gateway.SectionName
		}
		return dom, class, issuer, gwName, gwNS, gwSection
	}
	platform.InternalBaseDomain, platform.InternalIngressClassName, platform.InternalClusterIssuer,
		platform.InternalGatewayName, platform.InternalGatewayNamespace, platform.InternalGatewaySectionName = resolveTier(domain.ExposeInternal)
	platform.ExternalBaseDomain, platform.ExternalIngressClassName, platform.ExternalClusterIssuer,
		platform.ExternalGatewayName, platform.ExternalGatewayNamespace, platform.ExternalGatewaySectionName = resolveTier(domain.ExposeExternal)

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
		// App config/secret are the platform contract (platform.configMapName /
		// secretName); per-component EnvFrom extras are applied per component.
		Suparship: SuparshipValues{},
	}
}

// MapComponentHelmValuesForEnv renders the values document for a SINGLE
// component of a composed app — the per-component values.yaml each component's
// own chart consumes as its one multi-source Application source.
//
// It is a single-component projection of MapToHelmValuesForEnv: it shallow-copies
// the app with Spec.Components narrowed to just this component, then delegates.
// This reuses all of MapToHelmValuesForEnv's routing/envFrom/platform-token
// derivation unchanged. The app-level platform contract (<app>-config/<app>-secrets
// via platform.configMapName/secretName) is shared across every component by
// default, so all of a composed app's workloads see the same env/secret surface in
// the one namespace unless a component curates its own (InheritAppVars:false).
//
// Two projections make the canonical values line up with an off-the-shelf
// component chart:
//
//   - componentKey renames the projected component to the key the chart reads
//     (a template's chart is authored against a fixed key — web-service reads
//     components.web — declared as the template's first component name). Pass the
//     component's own name when a chart reads components.<its-own-name>.
//   - app.name is set to "{app}-{component}" so the chart's fullname helper names
//     this component's resources "{app}-{component}-…", distinct from its
//     siblings sharing the one namespace (fullname derives from .Values.app.name).
func MapComponentHelmValuesForEnv(
	app *domain.App,
	comp domain.ComponentSpec,
	componentKey string,
	envName string,
	envType domain.AppEnvironmentType,
	baseDomain, namespace, cluster string,
	orgName string,
	orgProfiles, envProfiles, clusterProfiles domain.RoutingProfiles,
) HelmValues {
	instanceName := app.Name + "-" + comp.Name
	if componentKey == "" {
		componentKey = comp.Name
	}
	comp.Name = componentKey // emit under the chart's canonical component key

	projected := *app
	projected.Spec.Components = []domain.ComponentSpec{comp}
	hv := MapToHelmValuesForEnv(
		&projected, envName, envType, baseDomain, namespace, cluster, orgName,
		orgProfiles, envProfiles, clusterProfiles,
	)
	hv.App.Name = instanceName

	// Per-component routing host: each component gets its OWN host derived from
	// {app}-{component} (not the bare app name), so multiple exposed components in
	// one composed app (api + frontend) resolve to distinct hostnames instead of
	// colliding. Uses the resolved base domain MapToHelmValuesForEnv already baked
	// into platform.BaseDomain (cluster → env → org tier precedence). The host is
	// only consumed by the chart when the component is exposed; overriding it
	// unconditionally keeps each component's values self-consistent.
	host := stripScheme(domain.GenerateURLWithDomain(instanceName, envName, envType, hv.Platform.BaseDomain))
	hv.Routing.Host = host
	hv.Platform.RoutingHost = host
	return hv
}

// envFromLists returns the names the chart should envFrom for this app-env,
// backend-agnostic. ESO materializes three per-scope Secrets in the app
// namespace — <app>-global, <app>-env, <app>-cluster — each merging the
// org-admin shared item and the app's own item for that scope.
//
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
