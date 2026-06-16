package platform

import (
	"reflect"
	"testing"

	"github.com/suparcloud/suparship/internal/helmvalues"
)

func testCtx() Context {
	return Context{
		Platform: helmvalues.PlatformValues{
			Org: "acme", Project: "demo", App: "hello",
			Env: "prod", EnvType: "prod", Cluster: "prod-eks",
			Namespace: "hello-prod", BaseDomain: "acme.com",
			RoutingHost: "hello.prod.acme.com", IngressClassName: "nginx",
		},
		Vars: map[string]string{"REGION": "us-east", "LOG_LEVEL": "info"},
	}
}

func TestInterpolate_PlatformAndVars(t *testing.T) {
	c := testCtx()
	cases := map[string]string{
		"https://{platform.routingHost}/api": "https://hello.prod.acme.com/api",
		"{platform.env}-latest":              "prod-latest",
		"region={vars.REGION}":               "region=us-east",
		"{platform.org}/{platform.app}":      "acme/hello",
	}
	for in, want := range cases {
		if got := c.Interpolate(in); got != want {
			t.Errorf("Interpolate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInterpolate_PlatformManagedNames(t *testing.T) {
	c := testCtx() // App: hello
	cases := map[string]string{
		"{platform.configMapName}": "hello-config",
		"{platform.secretName}":    "hello-secrets",
	}
	for in, want := range cases {
		if got := c.Interpolate(in); got != want {
			t.Errorf("Interpolate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInterpolate_PlatformManagedNames_EmptyAppLeftLiteral(t *testing.T) {
	// With no App in context, the token is not a "present" token → left literal.
	c := Context{Platform: helmvalues.PlatformValues{}}
	if got := c.Interpolate("{platform.configMapName}"); got != "{platform.configMapName}" {
		t.Errorf("empty App should leave token literal, got %q", got)
	}
}

func TestPlatformTokens_IncludesManagedNames(t *testing.T) {
	found := map[string]bool{}
	for _, tok := range PlatformTokens() {
		found[tok.Token] = true
	}
	for _, want := range []string{"{platform.configMapName}", "{platform.secretName}"} {
		if !found[want] {
			t.Errorf("PlatformTokens() missing %q", want)
		}
	}
}

func TestInterpolate_LeavesHelmAndUnknownUntouched(t *testing.T) {
	c := testCtx()
	for _, s := range []string{
		"{{ .Release.Name }}",   // Helm passthrough
		"{{ .Values.foo }}-{x}", // Helm + unknown single-brace
		"{bogus}",               // unknown token
		"plain string",          // no braces
	} {
		if got := c.Interpolate(s); got != s {
			t.Errorf("Interpolate(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestInterpolate_AbsentVarLeftLiteral_EmptyFieldEmptied(t *testing.T) {
	c := testCtx()
	// {vars.SECRET} is not in Vars (secrets never are) → left literal, fails loudly.
	if got := c.Interpolate("x={vars.SECRET}"); got != "x={vars.SECRET}" {
		t.Errorf("absent var = %q, want literal left intact", got)
	}
	// A known-but-empty platform field (ClusterIssuer empty here) → empties.
	if got := c.Interpolate("issuer={platform.clusterIssuer}"); got != "issuer=" {
		t.Errorf("empty platform field = %q, want %q", got, "issuer=")
	}
}

func TestInterpolate_OnePassNoRecursion(t *testing.T) {
	// A var whose value itself looks like a token is NOT re-expanded.
	c := Context{
		Platform: helmvalues.PlatformValues{Env: "prod"},
		Vars:     map[string]string{"A": "{platform.env}"},
	}
	if got := c.Interpolate("{vars.A}"); got != "{platform.env}" {
		t.Errorf("one-pass = %q, want literal {platform.env}", got)
	}
}

func TestInterpolateMap(t *testing.T) {
	c := testCtx()
	got := c.InterpolateMap(map[string]string{
		"PUBLIC_URL": "https://{platform.routingHost}",
		"REGION":     "{vars.REGION}",
		"STATIC":     "noop",
	})
	want := map[string]string{
		"PUBLIC_URL": "https://hello.prod.acme.com",
		"REGION":     "us-east",
		"STATIC":     "noop",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("InterpolateMap = %v, want %v", got, want)
	}
	if c.InterpolateMap(nil) != nil {
		t.Error("InterpolateMap(nil) should be nil")
	}
}

func TestInterpolateTree(t *testing.T) {
	c := testCtx()
	in := map[string]any{
		"podAnnotations": map[string]any{"region": "{vars.REGION}"},
		"hosts":          []any{"{platform.routingHost}", "static.example.com"},
		"replicas":       3,
	}
	got := c.InterpolateTree(in).(map[string]any)
	ann := got["podAnnotations"].(map[string]any)
	if ann["region"] != "us-east" {
		t.Errorf("nested map leaf = %v, want us-east", ann["region"])
	}
	hosts := got["hosts"].([]any)
	if hosts[0] != "hello.prod.acme.com" || hosts[1] != "static.example.com" {
		t.Errorf("slice leaves = %v", hosts)
	}
	if got["replicas"] != 3 {
		t.Errorf("non-string leaf changed: %v", got["replicas"])
	}
}
