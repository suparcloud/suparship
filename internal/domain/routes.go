package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Platform-owned routing.
//
// A RouteSpec declares an HTTP surface — hostnames on a routing tier, and
// path-prefix rules that forward to an app's Service — and suparship renders
// the Gateway API HTTPRoute (and any ReferenceGrant) for it into the app's
// platform resources, the same way it renders the env ConfigMap and Secret.
// Charts stay routing-agnostic (ingress disabled). Routes live on an app (an
// app-private surface) or on a stack (one host fanning out across members).
//
// Rendering rule: a route is split per backend app — each app's platform dir
// carries only the rules that forward to that app — so in a stable env an
// HTTPRoute and its Service always share a namespace. Route-to-preview is then
// a backend switch (the rule's backendRef namespace becomes the preview's), not
// a hostname change.

// RouteBackend names the Service a rule forwards to.
type RouteBackend struct {
	// App is the backend app. Defaults to the app the routes are declared on;
	// for stack routes it must be a member of the stack.
	App string `json:"app,omitempty" yaml:"app,omitempty"`
	// Component narrows a composed app to one component; the default Service
	// name becomes "{app}-{component}" (what the example charts render).
	Component string `json:"component,omitempty" yaml:"component,omitempty"`
	// Service overrides the Service name when the chart names it differently.
	Service string `json:"service,omitempty" yaml:"service,omitempty"`
	// Port is the Service port to forward to. Required.
	Port int `json:"port" yaml:"port"`
}

// RouteRule forwards one path prefix to a backend.
type RouteRule struct {
	// PathPrefix is the Gateway API PathPrefix match, e.g. "/" or "/api".
	PathPrefix string       `json:"pathPrefix" yaml:"pathPrefix"`
	Backend    RouteBackend `json:"backend" yaml:"backend"`
}

// RouteSpec is one declared HTTP surface.
type RouteSpec struct {
	// Name identifies the route within its owner; defaults to "route-<n>".
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// Hostnames may carry ((platform.*)) tokens; empty means the owner's default
	// (DefaultStackRouteHostname / DefaultAppRouteHostname).
	Hostnames []string `json:"hostnames,omitempty" yaml:"hostnames,omitempty"`
	// Tier is the routing tier whose Gateway the route attaches to: external
	// (default) or internal.
	Tier ExposeMode `json:"tier,omitempty" yaml:"tier,omitempty"`
	// Rules are matched longest-prefix-first by the gateway.
	Rules []RouteRule `json:"rules" yaml:"rules"`
}

// Default hostnames under the external tier's base domain. An app's default
// is its routing name, which already folds in the preview id ("app-pr-42").
// A stack's default is the stack name plus ((platform.previewSuffix)) ("" in
// stable envs, "-pr-42" in previews) — the same shape a shared literal host
// should use: "myhost((platform.previewSuffix)).((platform.externalBaseDomain))".
const (
	DefaultAppRouteHostname   = "((platform.appRoutingName)).((platform.externalBaseDomain))"
	DefaultStackRouteHostname = "((platform.stack))((platform.previewSuffix)).((platform.externalBaseDomain))"
)

// ExpandStackRoutes re-expresses a stack's routes as routes owned by member
// app: the same names/hostnames/tier, holding only the rules whose backend is
// that member. Stacks are optional sugar over app-owned routes, so everything
// downstream (validation, rendering, previews) sees app routes only.
func ExpandStackRoutes(stackRoutes []RouteSpec, member string) []RouteSpec {
	var out []RouteSpec
	for i, r := range stackRoutes {
		var rules []RouteRule
		for _, rule := range r.Rules {
			if rule.Backend.AppName("") == member {
				rules = append(rules, rule)
			}
		}
		if len(rules) == 0 {
			continue
		}
		out = append(out, RouteSpec{
			Name:      r.EffectiveName(i),
			Hostnames: r.EffectiveHostnames(DefaultStackRouteHostname),
			Tier:      r.EffectiveTier(),
			Rules:     rules,
		})
	}
	return out
}

// EffectiveName returns the route's name, defaulting to "route-<i+1>".
func (r RouteSpec) EffectiveName(i int) string {
	if r.Name != "" {
		return r.Name
	}
	return fmt.Sprintf("route-%d", i+1)
}

// EffectiveTier returns the tier, defaulting to external.
func (r RouteSpec) EffectiveTier() ExposeMode {
	if r.Tier == "" {
		return ExposeExternal
	}
	return r.Tier
}

// EffectiveHostnames returns the hostnames, or the given default when none.
func (r RouteSpec) EffectiveHostnames(defaultHost string) []string {
	if len(r.Hostnames) > 0 {
		return r.Hostnames
	}
	return []string{defaultHost}
}

// AppName returns the backend app, defaulting to self.
func (b RouteBackend) AppName(self string) string {
	if b.App != "" {
		return b.App
	}
	return self
}

// ServiceName returns the Service a rule forwards to: the explicit override,
// else "{app}-{component}", else "{app}".
func (b RouteBackend) ServiceName(self string) string {
	if b.Service != "" {
		return b.Service
	}
	app := b.AppName(self)
	if b.Component != "" {
		return app + "-" + b.Component
	}
	return app
}

// RoutesCoverApp reports whether any rule forwards to app (self resolves an
// empty backend app). Used to decide that an app is platform-routed.
func RoutesCoverApp(routes []RouteSpec, self, app string) bool {
	for _, r := range routes {
		for _, rule := range r.Rules {
			if rule.Backend.AppName(self) == app {
				return true
			}
		}
	}
	return false
}

// RouteBackendApps returns the distinct backend apps of the routes, sorted.
func RouteBackendApps(routes []RouteSpec, self string) []string {
	seen := map[string]bool{}
	for _, r := range routes {
		for _, rule := range r.Rules {
			seen[rule.Backend.AppName(self)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

var routeTokenRE = regexp.MustCompile(`\(\([a-zA-Z0-9_.]+\)\)`)

// validRouteHostname accepts a hostname whose ((tokens)) stand for whole or
// partial labels, plus an optional leading "*." wildcard label.
func validRouteHostname(h string) bool {
	h = strings.TrimPrefix(h, "*.")
	h = routeTokenRE.ReplaceAllString(h, "x")
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// ValidateRoutes checks a route set declared on selfApp (an app) or on a stack
// (selfApp = "" and allowedApps = the members). Profiles may be nil; when any
// are given, every tier must resolve to a routing profile (a Gateway renders
// HTTPRoutes, an IngressClass alone renders Ingresses).
func ValidateRoutes(routes []RouteSpec, selfApp string, allowedApps []string, orgProfiles, envProfiles RoutingProfiles) error {
	hasProfiles := len(orgProfiles) > 0 || len(envProfiles) > 0
	allowed := map[string]bool{}
	if selfApp != "" {
		allowed[selfApp] = true
	}
	for _, a := range allowedApps {
		allowed[a] = true
	}
	names := map[string]bool{}
	hostPrefix := map[string]string{} // "host|prefix" → route name
	for i, r := range routes {
		name := r.EffectiveName(i)
		if names[name] {
			return fmt.Errorf("route %q: duplicate route name", name)
		}
		names[name] = true
		if !IsDNSLabel(name) {
			return fmt.Errorf("route %q: name must be a DNS label", name)
		}
		tier := r.EffectiveTier()
		if tier != ExposeExternal && tier != ExposeInternal {
			return fmt.Errorf("route %q: tier must be external or internal, got %q", name, r.Tier)
		}
		if hasProfiles {
			// The tier renders as an HTTPRoute (profile has a Gateway) or as an
			// Ingress (profile has only an IngressClass) — either way it must
			// resolve to a profile.
			if _, err := ResolveRoutingProfile(orgProfiles, envProfiles, nil, tier); err != nil {
				return fmt.Errorf("route %q: %w", name, err)
			}
		}
		hosts := r.Hostnames
		if len(hosts) == 0 {
			hosts = []string{"(default)"}
		}
		for _, h := range r.Hostnames {
			if !validRouteHostname(h) {
				return fmt.Errorf("route %q: hostname %q is not a valid hostname (tokens like ((platform.appRoutingName)) are allowed)", name, h)
			}
		}
		if len(r.Rules) == 0 {
			return fmt.Errorf("route %q: at least one rule is required", name)
		}
		for _, rule := range r.Rules {
			if !strings.HasPrefix(rule.PathPrefix, "/") {
				return fmt.Errorf("route %q: pathPrefix %q must start with /", name, rule.PathPrefix)
			}
			for _, h := range hosts {
				key := h + "|" + rule.PathPrefix
				if other, dup := hostPrefix[key]; dup {
					return fmt.Errorf("route %q: path %q on host %q is already routed by %q", name, rule.PathPrefix, h, other)
				}
				hostPrefix[key] = name
			}
			b := rule.Backend
			app := b.AppName(selfApp)
			if app == "" {
				return fmt.Errorf("route %q: rule %q has no backend app", name, rule.PathPrefix)
			}
			if !allowed[app] {
				return fmt.Errorf("route %q: backend app %q is not this app or a member of this stack", name, app)
			}
			if b.Port <= 0 || b.Port > 65535 {
				return fmt.Errorf("route %q: backend %q needs a port (1-65535), got %d", name, app, b.Port)
			}
			if b.Component != "" && !IsDNSLabel(b.Component) {
				return fmt.Errorf("route %q: backend component %q must be a DNS label", name, b.Component)
			}
			if b.Service != "" && !IsDNSLabel(b.Service) {
				return fmt.Errorf("route %q: backend service %q must be a DNS label", name, b.Service)
			}
		}
	}
	return nil
}
