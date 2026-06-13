// Package platform interpolates suparShip platform metadata into user-supplied
// app configuration values (template inputs, raw Helm values, env-var values).
//
// Tokens are namespaced single-brace: {platform.<field>} for identity/routing
// metadata and {vars.<NAME>} for non-secret platform env vars. Only this closed
// set of *present* tokens is replaced — any other braces, including Helm's
// {{ ... }} that a raw values.yaml passes through to the chart, unknown {foo},
// and {vars.X} for a var that doesn't exist (secrets are never present), are left
// untouched (a literal leftover fails loudly rather than silently emptying).
// Resolution is a single pass: {vars.X} resolves to X's value as it was before
// interpolation, never re-expanding tokens that appear inside a substituted value.
package platform

import (
	"strings"

	"github.com/suparcloud/suparship/internal/helmvalues"
)

// Context is the interpolation context for one (app, env, cluster): the platform
// metadata block plus the resolved non-secret env vars exposed as {vars.NAME}.
type Context struct {
	Platform helmvalues.PlatformValues
	// Vars are the merged non-secret env vars (org→env→project→app→appEnv→cluster).
	Vars map[string]string
}

// Interpolate replaces known {platform.*} and {vars.*} tokens in s. Strings with
// no '{' are returned unchanged (fast path).
func (c Context) Interpolate(s string) string {
	if !strings.Contains(s, "{") {
		return s
	}
	return c.replacer().Replace(s)
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
	pairs := []string{
		"{platform.org}", p.Org,
		"{platform.project}", p.Project,
		"{platform.app}", p.App,
		"{platform.env}", p.Env,
		"{platform.envType}", p.EnvType,
		"{platform.cluster}", p.Cluster,
		"{platform.namespace}", p.Namespace,
		"{platform.baseDomain}", p.BaseDomain,
		"{platform.routingHost}", p.RoutingHost,
		"{platform.ingressClassName}", p.IngressClassName,
		"{platform.clusterIssuer}", p.ClusterIssuer,
	}
	for k, v := range c.Vars {
		pairs = append(pairs, "{vars."+k+"}", v)
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

// PlatformTokens returns the static catalog of {platform.*} tokens, grouped into
// "Identity" and "Routing" for the variable picker. {vars.*} tokens are dynamic
// (built from the org/env/project/cluster env-var keys) and added by the caller.
func PlatformTokens() []TokenInfo {
	return []TokenInfo{
		{Token: "{platform.org}", Label: "Org", Group: "Identity"},
		{Token: "{platform.project}", Label: "Project", Group: "Identity"},
		{Token: "{platform.app}", Label: "App", Group: "Identity"},
		{Token: "{platform.env}", Label: "Environment", Group: "Identity"},
		{Token: "{platform.envType}", Label: "Environment type", Group: "Identity", Description: "staging | prod | preview"},
		{Token: "{platform.cluster}", Label: "Cluster", Group: "Identity"},
		{Token: "{platform.namespace}", Label: "Namespace", Group: "Identity"},
		{Token: "{platform.baseDomain}", Label: "Base domain", Group: "Routing"},
		{Token: "{platform.routingHost}", Label: "Routing host", Group: "Routing", Description: "external host, no scheme"},
		{Token: "{platform.ingressClassName}", Label: "Ingress class", Group: "Routing"},
		{Token: "{platform.clusterIssuer}", Label: "Cluster issuer", Group: "Routing"},
	}
}
