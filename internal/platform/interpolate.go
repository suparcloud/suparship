// Package platform interpolates suparShip platform metadata into user-supplied
// app configuration values (template inputs, raw Helm values, env-var values).
//
// Tokens are namespaced double-parenthesis: ((platform.<field>)) for
// identity/routing metadata and ((vars.<NAME>)) for non-secret platform env
// vars. The (( )) delimiter is deliberately YAML/JSON-safe: '(' is not a YAML
// indicator character, so an unquoted value that IS a token — key: ((platform.x))
// — parses as a plain string, not (as [[ ]] did) a nested flow sequence. It also
// never collides with Go/Helm text templates ({{ ... }}), shell (${ }), or the
// JSON/YAML braces a raw values.yaml carries through to the chart (e.g. External
// Secrets templates).
//
// The legacy [[ ]] delimiter is still recognized as an alias so existing configs
// keep resolving, but it must be quoted in YAML ("[[platform.x]]") to survive
// parsing; new configs should use (( )). Only this closed set of *present* tokens
// is replaced — any other delimiters, including {{ ... }} passed through to the
// chart, unknown ((foo)), and ((vars.X)) for a var that doesn't exist (secrets
// are never present), are left untouched (a literal leftover fails loudly rather
// than silently emptying). Resolution is a single pass: ((vars.X)) resolves to
// X's value as it was before interpolation, never re-expanding tokens that appear
// inside a substituted value.
package platform

import (
	"strings"

	"github.com/suparcloud/suparship/internal/helmvalues"
	"github.com/suparcloud/suparship/internal/secrets"
)

// Context is the interpolation context for one (app, env, cluster): the platform
// metadata block plus the resolved non-secret env vars exposed as [[vars.NAME]].
type Context struct {
	Platform helmvalues.PlatformValues
	// Vars are the merged non-secret env vars (org→env→project→app→appEnv→cluster).
	Vars map[string]string
}

// Interpolate replaces known ((platform.*)) / ((vars.*)) tokens (and their legacy
// [[ ]] equivalents) in s. Strings with neither '(' nor '[' are returned
// unchanged (fast path).
func (c Context) Interpolate(s string) string {
	if !strings.Contains(s, "(") && !strings.Contains(s, "[") {
		return s
	}
	return c.replacer().Replace(s)
}

// HasToken reports whether s contains any platform/vars token in either the
// preferred (( )) or legacy [[ ]] delimiter. Callers use it to gate the
// interpolation round-trip (parse → replace → re-marshal) so token-free values
// skip the work entirely.
func HasToken(s string) bool {
	return strings.Contains(s, "((platform.") || strings.Contains(s, "((vars.") ||
		strings.Contains(s, "[[platform.") || strings.Contains(s, "[[vars.")
}

// InterpolateMap returns a new map with every value interpolated. Keys are never
// rewritten. A nil map returns nil.
func (c Context) InterpolateMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	r := c.replacer()
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = r.Replace(v)
	}
	return out
}

// InterpolateTree deep-copies v (a value decoded from YAML/JSON: maps, slices,
// scalars) and interpolates every string leaf. Map keys are left untouched.
func (c Context) InterpolateTree(v any) any {
	r := c.replacer()
	return interpolateTree(v, r)
}

func interpolateTree(v any, r *strings.Replacer) any {
	switch t := v.(type) {
	case string:
		return r.Replace(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = interpolateTree(val, r)
		}
		return out
	case map[any]any: // gopkg.in/yaml.v3 may decode nested maps this way
		out := make(map[any]any, len(t))
		for k, val := range t {
			out[k] = interpolateTree(val, r)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = interpolateTree(val, r)
		}
		return out
	default:
		return v
	}
}

func (c Context) replacer() *strings.Replacer {
	p := c.Platform
	// (name, value) pairs; each name is exposed under both delimiters below.
	named := []string{
		"platform.org", p.Org,
		"platform.project", p.Project,
		"platform.app", p.App,
		"platform.env", p.Env,
		"platform.envType", p.EnvType,
		"platform.cluster", p.Cluster,
		"platform.namespace", p.Namespace,
		"platform.baseDomain", p.BaseDomain,
		"platform.routingHost", p.RoutingHost,
		"platform.ingressClassName", p.IngressClassName,
		"platform.clusterIssuer", p.ClusterIssuer,
		// Per-tier routing (internal/external), resolved cluster→env→org.
		"platform.internalBaseDomain", p.InternalBaseDomain,
		"platform.externalBaseDomain", p.ExternalBaseDomain,
		"platform.internalIngressClassName", p.InternalIngressClassName,
		"platform.externalIngressClassName", p.ExternalIngressClassName,
		"platform.internalClusterIssuer", p.InternalClusterIssuer,
		"platform.externalClusterIssuer", p.ExternalClusterIssuer,
		"platform.internalGatewayName", p.InternalGatewayName,
		"platform.internalGatewayNamespace", p.InternalGatewayNamespace,
		"platform.internalGatewaySectionName", p.InternalGatewaySectionName,
		"platform.externalGatewayName", p.ExternalGatewayName,
		"platform.externalGatewayNamespace", p.ExternalGatewayNamespace,
		"platform.externalGatewaySectionName", p.ExternalGatewaySectionName,
	}
	// Platform-managed per-app ConfigMap/Secret names, derived from the app name
	// (single source of truth: secrets.AppConfigMapName/AppSecretName). Exposed so
	// BYO/passthrough charts can wire envFrom to the platform-managed env without
	// hardcoding. Guarded on App so an empty context doesn't emit "-config".
	if p.App != "" {
		// Prefer the explicitly resolved names (set for shared-namespace previews,
		// where they carry the preview-name suffix); fall back to the {app}-config
		// / {app}-secrets convention.
		cmName := p.ConfigMapName
		if cmName == "" {
			cmName = secrets.AppConfigMapName(p.App)
		}
		secName := p.SecretName
		if secName == "" {
			secName = secrets.AppSecretName(p.App)
		}
		named = append(named,
			"platform.configMapName", cmName,
			"platform.secretName", secName,
		)
	}
	// ((platform.imageTag)) is ALWAYS replaced — to the resolved tag, or to "" when
	// none is known (a stable app whose tag is owned by external CD and not yet
	// promoted). It must never survive as a literal: charts stamp the tag into
	// metadata.labels (app.kubernetes.io/version) and image refs, and "((...))" is
	// an invalid label value (k8s rejects the whole apply). An empty tag is safe —
	// the canonical chart defaults it to .Chart.AppVersion (see suparship-common
	// _labels.tpl), and CD overwrites the real tag on its first promotion.
	named = append(named, "platform.imageTag", p.ImageTag)
	// Preview name — exposed only for previews so a shared-namespace preview can
	// suffix workload resource names per PR. Left literal for stable envs.
	if p.PreviewName != "" {
		named = append(named, "platform.previewName", p.PreviewName)
	}
	for k, v := range c.Vars {
		named = append(named, "vars."+k, v)
	}
	// Expand each name into both the preferred (( )) delimiter and the legacy
	// [[ ]] alias so old and new configs both resolve in a single pass.
	pairs := make([]string, 0, len(named)*2)
	for i := 0; i < len(named); i += 2 {
		name, val := named[i], named[i+1]
		pairs = append(pairs, "(("+name+"))", val, "[["+name+"]]", val)
	}
	return strings.NewReplacer(pairs...)
}

// TokenInfo describes one referenceable variable for the UI picker.
type TokenInfo struct {
	Token       string `json:"token"`
	Label       string `json:"label"`
	Group       string `json:"group"`
	Description string `json:"description,omitempty"`
}

// PlatformTokens returns the static catalog of ((platform.*)) tokens, grouped into
// "Identity" and "Routing" for the variable picker. ((vars.*)) tokens are dynamic
// (built from the org/env/project/cluster env-var keys) and added by the caller.
// The picker inserts these strings verbatim, so they carry the preferred (( ))
// delimiter.
func PlatformTokens() []TokenInfo {
	return []TokenInfo{
		{Token: "((platform.org))", Label: "Org", Group: "Identity"},
		{Token: "((platform.project))", Label: "Project", Group: "Identity"},
		{Token: "((platform.app))", Label: "App", Group: "Identity"},
		{Token: "((platform.env))", Label: "Environment", Group: "Identity"},
		{Token: "((platform.envType))", Label: "Environment type", Group: "Identity", Description: "staging | prod | preview"},
		{Token: "((platform.cluster))", Label: "Cluster", Group: "Identity"},
		{Token: "((platform.namespace))", Label: "Namespace", Group: "Identity"},
		{Token: "((platform.baseDomain))", Label: "Base domain", Group: "Routing", Description: "app's routing-component tier"},
		{Token: "((platform.routingHost))", Label: "Routing host", Group: "Routing", Description: "external host, no scheme"},
		{Token: "((platform.ingressClassName))", Label: "Ingress class", Group: "Routing"},
		{Token: "((platform.clusterIssuer))", Label: "Cluster issuer", Group: "Routing"},
		{Token: "((platform.internalBaseDomain))", Label: "Internal base domain", Group: "Routing", Description: "internal tier, resolved cluster→env→org"},
		{Token: "((platform.externalBaseDomain))", Label: "External base domain", Group: "Routing", Description: "external tier, resolved cluster→env→org"},
		{Token: "((platform.internalIngressClassName))", Label: "Internal ingress class", Group: "Routing"},
		{Token: "((platform.externalIngressClassName))", Label: "External ingress class", Group: "Routing"},
		{Token: "((platform.internalClusterIssuer))", Label: "Internal cluster issuer", Group: "Routing"},
		{Token: "((platform.externalClusterIssuer))", Label: "External cluster issuer", Group: "Routing"},
		{Token: "((platform.internalGatewayName))", Label: "Internal gateway name", Group: "Routing", Description: "Gateway API parentRef (internal Envoy)"},
		{Token: "((platform.internalGatewayNamespace))", Label: "Internal gateway namespace", Group: "Routing"},
		{Token: "((platform.internalGatewaySectionName))", Label: "Internal gateway section", Group: "Routing"},
		{Token: "((platform.externalGatewayName))", Label: "External gateway name", Group: "Routing", Description: "Gateway API parentRef (external Envoy)"},
		{Token: "((platform.externalGatewayNamespace))", Label: "External gateway namespace", Group: "Routing"},
		{Token: "((platform.externalGatewaySectionName))", Label: "External gateway section", Group: "Routing"},
		{Token: "((platform.configMapName))", Label: "Config map name", Group: "Identity", Description: "platform-managed app ConfigMap ({app}-config)"},
		{Token: "((platform.secretName))", Label: "Secret name", Group: "Identity", Description: "platform-managed app Secret ({app}-secrets)"},
		{Token: "((platform.imageTag))", Label: "Image tag", Group: "Identity", Description: "resolved image tag (per-PR tag for previews); pin sidecar/init images to it"},
		{Token: "((platform.previewName))", Label: "Preview name", Group: "Identity", Description: "PR/preview identifier (e.g. pr-42); suffix workload names in a shared preview namespace"},
	}
}
