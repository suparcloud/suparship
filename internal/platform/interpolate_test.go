package platform

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"

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

func TestInterpolate_PerTierRoutingTokens(t *testing.T) {
	c := Context{Platform: helmvalues.PlatformValues{
		App:                        "tts",
		InternalBaseDomain:         "aws.example.com",
		ExternalBaseDomain:         "ext.example.com",
		InternalIngressClassName:   "nginx-internal",
		ExternalClusterIssuer:      "letsencrypt",
		InternalGatewayName:        "envoy-internal",
		InternalGatewayNamespace:   "envoy-gateway-system",
		InternalGatewaySectionName: "https",
	}}
	cases := map[string]string{
		"((platform.app)).internal.((platform.internalBaseDomain))": "tts.internal.aws.example.com",
		"((platform.app)).((platform.externalBaseDomain))":          "tts.ext.example.com",
		"class=((platform.internalIngressClassName))":               "class=nginx-internal",
		"issuer=((platform.externalClusterIssuer))":                 "issuer=letsencrypt",
		"((platform.internalGatewayName))/((platform.internalGatewayNamespace))/((platform.internalGatewaySectionName))": "envoy-internal/envoy-gateway-system/https",
	}
	for in, want := range cases {
		if got := c.Interpolate(in); got != want {
			t.Errorf("Interpolate(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInterpolate_PlatformAndVars(t *testing.T) {
	c := testCtx()
	cases := map[string]string{
		"https://[[platform.routingHost]]/api": "https://hello.prod.acme.com/api",
		"[[platform.env]]-latest":              "prod-latest",
		"region=[[vars.REGION]]":               "region=us-east",
		"[[platform.org]]/[[platform.app]]":      "acme/hello",
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
		"[[platform.configMapName]]": "hello-config",
		"[[platform.secretName]]":    "hello-secrets",
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
	if got := c.Interpolate("[[platform.configMapName]]"); got != "[[platform.configMapName]]" {
		t.Errorf("empty App should leave token literal, got %q", got)
	}
}

func TestInterpolate_ImageTag(t *testing.T) {
	c := Context{Platform: helmvalues.PlatformValues{App: "hello", ImageTag: "pr-712-3ac32cb7"}}
	if got := c.Interpolate("[[platform.imageTag]]"); got != "pr-712-3ac32cb7" {
		t.Errorf("Interpolate([[platform.imageTag]]) = %q, want pr-712-3ac32cb7", got)
	}
	// Works as a tag in a repo:tag string (the sidecar wiring use case).
	if got := c.Interpolate("acr.io/app:[[platform.imageTag]]"); got != "acr.io/app:pr-712-3ac32cb7" {
		t.Errorf("got %q, want acr.io/app:pr-712-3ac32cb7", got)
	}
}

func TestInterpolate_ImageTagEmptyResolvesToEmpty(t *testing.T) {
	// No resolved tag → token resolves to "" (never left literal): a literal
	// "[[platform.imageTag]]" in metadata.labels / image refs is an invalid value
	// k8s rejects. The chart defaults an empty tag to .Chart.AppVersion.
	c := Context{Platform: helmvalues.PlatformValues{App: "hello"}}
	if got := c.Interpolate("[[platform.imageTag]]"); got != "" {
		t.Errorf("empty ImageTag should resolve to empty, got %q", got)
	}
	// And it must not survive embedded in a larger string either.
	if got := c.Interpolate("acr.io/app:[[platform.imageTag]]"); got != "acr.io/app:" {
		t.Errorf("embedded empty ImageTag = %q, want acr.io/app:", got)
	}
}

func TestInterpolate_PreviewName(t *testing.T) {
	c := Context{Platform: helmvalues.PlatformValues{App: "hello", PreviewName: "pr-42"}}
	if got := c.Interpolate("[[platform.app]]-[[platform.previewName]]"); got != "hello-pr-42" {
		t.Errorf("got %q, want hello-pr-42", got)
	}
	// Stable env: no preview name → token left literal.
	stable := Context{Platform: helmvalues.PlatformValues{App: "hello"}}
	if got := stable.Interpolate("[[platform.previewName]]"); got != "[[platform.previewName]]" {
		t.Errorf("stable previewName should be literal, got %q", got)
	}
}

func TestInterpolate_ResolvedPlatformNamesOverrideDefault(t *testing.T) {
	// Shared-namespace preview: the publisher sets suffixed ConfigMap/Secret
	// names, which must win over the {app}-config / {app}-secrets default.
	c := Context{Platform: helmvalues.PlatformValues{
		App:           "hello",
		ConfigMapName: "hello-pr-42-config",
		SecretName:    "hello-pr-42-secrets",
	}}
	if got := c.Interpolate("[[platform.configMapName]]"); got != "hello-pr-42-config" {
		t.Errorf("configMapName = %q, want hello-pr-42-config", got)
	}
	if got := c.Interpolate("[[platform.secretName]]"); got != "hello-pr-42-secrets" {
		t.Errorf("secretName = %q, want hello-pr-42-secrets", got)
	}
	// Unset → fall back to the {app}-* convention.
	d := Context{Platform: helmvalues.PlatformValues{App: "hello"}}
	if got := d.Interpolate("[[platform.configMapName]]"); got != "hello-config" {
		t.Errorf("default configMapName = %q, want hello-config", got)
	}
}

func TestPlatformTokens_IncludesManagedNames(t *testing.T) {
	found := map[string]bool{}
	for _, tok := range PlatformTokens() {
		found[tok.Token] = true
	}
	for _, want := range []string{"((platform.configMapName))", "((platform.secretName))", "((platform.imageTag))", "((platform.previewName))"} {
		if !found[want] {
			t.Errorf("PlatformTokens() missing %q", want)
		}
	}
}

func TestInterpolate_LeavesHelmAndUnknownUntouched(t *testing.T) {
	c := testCtx()
	for _, s := range []string{
		"{{ .Release.Name }}",    // Helm passthrough
		"{{ .Values.foo }}-{x}",  // Helm + bare single brace
		`{"auths":{"{{ .host }}":{"username":"{{ .username }}"}}}`, // External Secrets Go template + JSON braces
		"[[bogus]]",              // unknown token (right delimiter, no platform./vars. namespace)
		"[[platform.nope]]",      // unknown platform field
		"{platform.app}",         // old single-brace syntax is no longer a token
		"plain string",           // no delimiters
	} {
		if got := c.Interpolate(s); got != s {
			t.Errorf("Interpolate(%q) = %q, want unchanged", s, got)
		}
	}
}

func TestInterpolate_AbsentVarLeftLiteral_EmptyFieldEmptied(t *testing.T) {
	c := testCtx()
	// [[vars.SECRET]] is not in Vars (secrets never are) → left literal, fails loudly.
	if got := c.Interpolate("x=[[vars.SECRET]]"); got != "x=[[vars.SECRET]]" {
		t.Errorf("absent var = %q, want literal left intact", got)
	}
	// A known-but-empty platform field (ClusterIssuer empty here) → empties.
	if got := c.Interpolate("issuer=[[platform.clusterIssuer]]"); got != "issuer=" {
		t.Errorf("empty platform field = %q, want %q", got, "issuer=")
	}
}

func TestInterpolate_OnePassNoRecursion(t *testing.T) {
	// A var whose value itself looks like a token is NOT re-expanded.
	c := Context{
		Platform: helmvalues.PlatformValues{Env: "prod"},
		Vars:     map[string]string{"A": "[[platform.env]]"},
	}
	if got := c.Interpolate("[[vars.A]]"); got != "[[platform.env]]" {
		t.Errorf("one-pass = %q, want literal [[platform.env]]", got)
	}
}

func TestInterpolateMap(t *testing.T) {
	c := testCtx()
	got := c.InterpolateMap(map[string]string{
		"PUBLIC_URL": "https://[[platform.routingHost]]",
		"REGION":     "[[vars.REGION]]",
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

func TestInterpolate_DoubleParenDelimiter(t *testing.T) {
	c := testCtx()
	cases := map[string]string{
		"https://((platform.routingHost))/api": "https://hello.prod.acme.com/api",
		"((platform.env))-latest":              "prod-latest",
		"region=((vars.REGION))":               "region=us-east",
		"((platform.org))/((platform.app))":    "acme/hello",
	}
	for in, want := range cases {
		if got := c.Interpolate(in); got != want {
			t.Errorf("Interpolate(%q) = %q, want %q", in, got, want)
		}
	}
}

// Both delimiters resolve to the same value (legacy [[ ]] kept as an alias).
func TestInterpolate_BothDelimitersResolve(t *testing.T) {
	c := testCtx()
	if a, b := c.Interpolate("((platform.env))"), c.Interpolate("[[platform.env]]"); a != "prod" || b != "prod" {
		t.Errorf("((platform.env))=%q [[platform.env]]=%q, both want prod", a, b)
	}
}

func TestHasToken(t *testing.T) {
	yes := []string{"tag: ((platform.imageTag))", "x=((vars.REGION))", `"[[platform.env]]"`, "a [[vars.X]] b"}
	no := []string{"plain", "{{ .Release.Name }}", "arr: [[1, 2], [3]]", "call: (foo bar)"}
	for _, s := range yes {
		if !HasToken(s) {
			t.Errorf("HasToken(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if HasToken(s) {
			t.Errorf("HasToken(%q) = true, want false", s)
		}
	}
}

// The regression this whole change fixes: an unquoted ((token)) at the start of a
// YAML value parses as a plain string and survives to interpolation, whereas the
// legacy [[token]] was mangled into a nested list by the YAML parser first.
func TestInterpolateTree_UnquotedDoubleParenSurvivesYAML(t *testing.T) {
	c := Context{Platform: helmvalues.PlatformValues{
		App: "hello", ImageTag: "pr-712-abc", RoutingHost: "hello.prod.acme.com",
	}}

	var tree map[string]any
	if err := yaml.Unmarshal([]byte("image:\n  tag: ((platform.imageTag))\nhost: ((platform.routingHost))\n"), &tree); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Unquoted (( )) parsed as a plain string, not a list.
	if _, ok := tree["host"].(string); !ok {
		t.Fatalf("host parsed as %T, want string — (( )) must not be a flow sequence", tree["host"])
	}

	out := c.InterpolateTree(tree).(map[string]any)
	if got := out["host"]; got != "hello.prod.acme.com" {
		t.Errorf("host = %v, want resolved value", got)
	}
	if got := out["image"].(map[string]any)["tag"]; got != "pr-712-abc" {
		t.Errorf("image.tag = %v, want pr-712-abc", got)
	}

	// Contrast: the legacy [[ ]] form IS mangled into a nested list at parse time
	// (a list-in-a-list), so the token string no longer exists to interpolate —
	// exactly the bug (( )) replaces.
	var legacy map[string]any
	_ = yaml.Unmarshal([]byte("tag: [[platform.imageTag]]\n"), &legacy)
	if _, isList := legacy["tag"].([]any); !isList {
		t.Errorf("expected legacy unquoted [[ ]] to parse as a nested list (the bug), got %T", legacy["tag"])
	}
}

func TestInterpolateTree(t *testing.T) {
	c := testCtx()
	in := map[string]any{
		"podAnnotations": map[string]any{"region": "[[vars.REGION]]"},
		"hosts":          []any{"[[platform.routingHost]]", "static.example.com"},
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
