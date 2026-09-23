package domain

import (
	"strings"
	"testing"
)

func gwProfiles() RoutingProfiles {
	return RoutingProfiles{
		"external": {IngressClassName: "eg", Gateway: &GatewayRef{Name: "edge", Namespace: "gateways", SectionName: "https"}},
		"internal": {IngressClassName: "eg-int"},
	}
}

func TestRouteSpec_Defaults(t *testing.T) {
	r := RouteSpec{Rules: []RouteRule{{PathPrefix: "/", Backend: RouteBackend{Port: 80}}}}
	if r.EffectiveName(2) != "route-3" || r.EffectiveTier() != ExposeExternal {
		t.Errorf("defaults = (%q, %q)", r.EffectiveName(2), r.EffectiveTier())
	}
	if got := r.EffectiveHostnames(DefaultStackRouteHostname); len(got) != 1 || got[0] != DefaultStackRouteHostname {
		t.Errorf("default hostnames = %v", got)
	}
	b := RouteBackend{Port: 80}
	if b.AppName("hello") != "hello" || b.ServiceName("hello") != "hello" {
		t.Errorf("self backend = (%q, %q)", b.AppName("hello"), b.ServiceName("hello"))
	}
	b = RouteBackend{App: "api", Component: "web", Port: 80}
	if b.ServiceName("hello") != "api-web" {
		t.Errorf("component service = %q, want api-web", b.ServiceName("hello"))
	}
	b = RouteBackend{App: "api", Component: "web", Service: "custom", Port: 80}
	if b.ServiceName("hello") != "custom" {
		t.Errorf("override service = %q, want custom", b.ServiceName("hello"))
	}
	routes := []RouteSpec{{Rules: []RouteRule{{PathPrefix: "/", Backend: RouteBackend{App: "cloud", Port: 80}}, {PathPrefix: "/sh", Backend: RouteBackend{App: "sh", Port: 80}}}}}
	if !RoutesCoverApp(routes, "", "sh") || RoutesCoverApp(routes, "", "routes") {
		t.Error("RoutesCoverApp mismatch")
	}
	if got := RouteBackendApps(routes, ""); strings.Join(got, ",") != "cloud,sh" {
		t.Errorf("backend apps = %v", got)
	}
}

func TestValidateRoutes(t *testing.T) {
	ok := func(rules ...RouteRule) RouteSpec {
		return RouteSpec{Hostnames: []string{"((platform.stackRoutingName)).((platform.externalBaseDomain))"}, Rules: rules}
	}
	rule := func(prefix, app string) RouteRule {
		return RouteRule{PathPrefix: prefix, Backend: RouteBackend{App: app, Port: 80}}
	}
	members := []string{"cloud", "sh"}

	cases := []struct {
		name   string
		routes []RouteSpec
		self   string
		want   string // substring of the error, "" = valid
		prof   RoutingProfiles
	}{
		{"stack route ok", []RouteSpec{ok(rule("/", "cloud"), rule("/sh", "sh"))}, "", "", gwProfiles()},
		{"app route self ok", []RouteSpec{{Rules: []RouteRule{{PathPrefix: "/", Backend: RouteBackend{Port: 8080}}}}}, "hello", "", gwProfiles()},
		{"no profiles skips gateway check", []RouteSpec{ok(rule("/", "cloud"))}, "", "", nil},
		{"ingress-only tier ok", []RouteSpec{{Tier: ExposeInternal, Rules: []RouteRule{rule("/", "cloud")}}}, "", "", gwProfiles()},
		{"tier without profile", []RouteSpec{{Tier: ExposeInternal, Rules: []RouteRule{rule("/", "cloud")}}}, "", "no profile", RoutingProfiles{"external": gwProfiles()["external"]}},
		{"bad tier", []RouteSpec{{Tier: "public", Rules: []RouteRule{rule("/", "cloud")}}}, "", "tier must be", gwProfiles()},
		{"foreign backend", []RouteSpec{ok(rule("/", "telephony"))}, "", "not this app or a member", gwProfiles()},
		{"port zero", []RouteSpec{{Rules: []RouteRule{{PathPrefix: "/", Backend: RouteBackend{App: "cloud"}}}}}, "", "needs a port", gwProfiles()},
		{"duplicate prefix same host", []RouteSpec{ok(rule("/", "cloud"), rule("/", "sh"))}, "", "already routed", gwProfiles()},
		{"duplicate prefix across routes", []RouteSpec{ok(rule("/", "cloud")), ok(rule("/", "sh"))}, "", "already routed", gwProfiles()},
		{"prefix without slash", []RouteSpec{ok(rule("api", "cloud"))}, "", "must start with /", gwProfiles()},
		{"no rules", []RouteSpec{{Hostnames: []string{"a.b"}}}, "", "at least one rule", gwProfiles()},
		{"bad hostname", []RouteSpec{{Hostnames: []string{"bad_host.acme.com"}, Rules: []RouteRule{rule("/", "cloud")}}}, "", "not a valid hostname", gwProfiles()},
		{"wildcard hostname ok", []RouteSpec{{Hostnames: []string{"*.acme.com"}, Rules: []RouteRule{rule("/", "cloud")}}}, "", "", gwProfiles()},
		{"duplicate names", []RouteSpec{{Name: "api", Rules: []RouteRule{rule("/", "cloud")}}, {Name: "api", Hostnames: []string{"x.acme.com"}, Rules: []RouteRule{rule("/", "sh")}}}, "", "duplicate route name", gwProfiles()},
		{"bad service label", []RouteSpec{{Rules: []RouteRule{{PathPrefix: "/", Backend: RouteBackend{App: "cloud", Service: "Bad_Svc", Port: 80}}}}}, "", "must be a DNS label", gwProfiles()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRoutes(tc.routes, tc.self, members, tc.prof, nil)
			if tc.want == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestExpandStackRoutes(t *testing.T) {
	stack := []RouteSpec{{
		Name:  "web",
		Rules: []RouteRule{{PathPrefix: "/", Backend: RouteBackend{App: "cloud", Port: 80}}, {PathPrefix: "/sh", Backend: RouteBackend{App: "sh", Port: 80}}},
	}}
	sh := ExpandStackRoutes(stack, "sh")
	if len(sh) != 1 || len(sh[0].Rules) != 1 || sh[0].Rules[0].PathPrefix != "/sh" || sh[0].Name != "web" {
		t.Fatalf("sh routes = %+v", sh)
	}
	if sh[0].Hostnames[0] != DefaultStackRouteHostname || sh[0].Tier != ExposeExternal {
		t.Errorf("expanded defaults = %v %q", sh[0].Hostnames, sh[0].Tier)
	}
	if got := ExpandStackRoutes(stack, "routes"); got != nil {
		t.Errorf("non-backend member should get no routes, got %+v", got)
	}
}
