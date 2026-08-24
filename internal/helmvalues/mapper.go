package helmvalues

import (
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/secrets"
)

// MapPlatformValuesForEnv builds the app-level platform context for one
// (app, env, cluster). It is a pure function: same inputs always produce the
// same output.
//
// The routing context (RoutingHost, IngressClassName, ClusterIssuer, effective
// BaseDomain) reflects the app's routing component — the alphabetically first
// external, else internal, else web component (resolveRoutingComponent) — with
// its ExposeMode resolved against the routing profiles (cluster → env → org).
// The per-tier Internal*/External* fields resolve both tiers directly,
// independent of any component. ConfigMapName/SecretName carry the app-wide
// convention names; the publisher overrides them per instance (curated
// component projections, preview suffixing).
func MapPlatformValuesForEnv(
	app *domain.App,
	envName string,
	envType domain.AppEnvironmentType,
	baseDomain, namespace, cluster string,
	orgName string,
	orgProfiles, envProfiles, clusterProfiles domain.RoutingProfiles,
) PlatformValues {
	if baseDomain == "" {
		baseDomain = "localhost"
	}
	if orgName == "" {
		orgName = "default"
	}

	routingComponent := resolveRoutingComponent(app.Spec.Components)

	// The app's URL uses the routing component's resolved profile base domain
	// when set (cluster → env → org), so a per-cluster baseDomain yields a
	// cluster-specific host; otherwise the caller-supplied baseDomain (the
	// cluster default or env default) is used.
	effectiveBase := baseDomain
	var routingSpec *domain.ComponentSpec
	for i := range app.Spec.Components {
		if app.Spec.Components[i].Name != routingComponent {
			continue
		}
		routingSpec = &app.Spec.Components[i]
		if prof, err := domain.ResolveRoutingProfile(orgProfiles, envProfiles, clusterProfiles, routingSpec.ExposeMode); err == nil && prof.BaseDomain != "" {
			effectiveBase = prof.BaseDomain
		}
		break
	}
	// secure=false is arbitrary here: stripScheme discards the scheme, charts
	// only ever see the bare host.
	routingHost := stripScheme(domain.GenerateURLWithDomain(app.Name, envName, envType, effectiveBase, false))

	platform := PlatformValues{
		Org:        orgName,
		Project:    app.ProjectName,
		App:        app.Name,
		Env:        envName,
		EnvType:    string(envType),
		Cluster:    cluster,
		Namespace:  namespace,
		BaseDomain: effectiveBase,
		// The app's base config + secret travel via ((platform.configMapName)) /
		// ((platform.secretName)) — the ONLY names a chart needs; suparship
		// renders the objects behind them. The publisher overrides these per
		// component for a curated / opt-out component (its own projection / ""
		// for no secrets) and per preview (name suffixing).
		RoutingHost:   routingHost,
		ConfigMapName: secrets.AppConfigMapName(app.Name),
		SecretName:    secrets.AppSecretName(app.Name),
	}
	// A single-component app IS its component, so ((platform.component)) resolves to
	// that component's name — in its values AND its env vars (this app-level map backs
	// platformVarsContext). A multi-component app-level map has no single component,
	// so Component stays empty (the composed path sets it per component instead).
	if len(app.Spec.Components) == 1 {
		platform.Component = app.Spec.Components[0].Name
	}
	if routingSpec != nil {
		platform.IngressClassName, platform.ClusterIssuer = resolveIngress(*routingSpec, orgProfiles, envProfiles, clusterProfiles)
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

	return platform
}

// MapComponentPlatformValuesForEnv builds the platform context for a SINGLE
// component of a composed app — the context its per-component values overlay
// interpolates against.
//
// It is a single-component projection of MapPlatformValuesForEnv (the app
// narrowed to just this component, so the routing context reflects the
// component's own ExposeMode), with two component-specific overrides:
//
//   - RoutingHost derives from the "{app}-{component}" instance name, so
//     multiple exposed components in one composed app (api + frontend) resolve
//     to distinct hostnames instead of colliding.
//   - Component is the component's user-facing name, for ((platform.component)).
func MapComponentPlatformValuesForEnv(
	app *domain.App,
	comp domain.ComponentSpec,
	envName string,
	envType domain.AppEnvironmentType,
	baseDomain, namespace, cluster string,
	orgName string,
	orgProfiles, envProfiles, clusterProfiles domain.RoutingProfiles,
) PlatformValues {
	projected := *app
	projected.Spec.Components = []domain.ComponentSpec{comp}
	platform := MapPlatformValuesForEnv(
		&projected, envName, envType, baseDomain, namespace, cluster, orgName,
		orgProfiles, envProfiles, clusterProfiles,
	)
	// Per-component routing host, on the resolved base domain (cluster → env →
	// org tier precedence) the app-level mapper already baked into BaseDomain.
	// Only consumed when the component is exposed; overriding unconditionally
	// keeps the context self-consistent.
	instanceName := app.Name + "-" + comp.Name
	platform.RoutingHost = stripScheme(domain.GenerateURLWithDomain(instanceName, envName, envType, platform.BaseDomain, false))
	platform.Component = comp.Name
	return platform
}

// resolveIngress turns a component's exposure intent into the resolved ingress
// class + cert-manager issuer for ((platform.ingressClassName)) /
// ((platform.clusterIssuer)). Resolves c.ExposeMode against the routing
// profile maps via domain.ResolveRoutingProfile.
//
// Returns empty strings for disabled modes, missing profiles, and resolution
// errors — the mapper stays a pure derivation, so a misconfigured profile
// yields empty tokens rather than blocking publish. Validation is the caller's
// job (domain.ValidateExposeModes runs in the app-save handler).
func resolveIngress(c domain.ComponentSpec, orgProfiles, envProfiles, clusterProfiles domain.RoutingProfiles) (className, clusterIssuer string) {
	mode := c.ExposeMode
	if mode == domain.ExposeDisabled || mode == "" {
		return "", ""
	}
	profile, err := domain.ResolveRoutingProfile(orgProfiles, envProfiles, clusterProfiles, mode)
	if err != nil {
		return "", ""
	}
	return profile.IngressClassName, profile.ClusterIssuer
}

// resolveRoutingComponent picks the name of the primary exposed component.
//
// Selection order (all candidates sorted alphabetically for determinism):
//  1. First component with ExposeMode == ExposeExternal — public face wins
//     over internal so a mixed app routes its public component.
//  2. First component with ExposeMode == ExposeInternal.
//  3. First component where Type == ComponentWeb. Used when every component
//     is disabled — the routing host still resolves but the ingress tokens
//     stay empty because resolveIngress returns nothing for disabled.
//  4. First component alphabetically (ultimate fallback).
//
// Returns an empty string when specs is empty — the routing host then derives
// from the app name on the plain (env, cluster) base domain and the ingress
// tokens stay empty.
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
// so that routing hosts contain only the hostname (as expected by Ingress).
func stripScheme(url string) string {
	if after, ok := strings.CutPrefix(url, "https://"); ok {
		return after
	}
	if after, ok := strings.CutPrefix(url, "http://"); ok {
		return after
	}
	return url
}
