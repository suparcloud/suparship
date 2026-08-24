package helmvalues

import (
	"reflect"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func webApp(name string, components ...domain.ComponentSpec) *domain.App {
	return &domain.App{
		Name:        name,
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template:   domain.AppTemplateRef{Name: "web-service", Version: "1.0.0"},
			Components: components,
		},
	}
}

func webComponent(name string) domain.ComponentSpec {
	return domain.ComponentSpec{
		Name:       name,
		Type:       domain.ComponentWeb,
		Enabled:    true,
		ExposeMode: domain.ExposeExternal,
	}
}

// ── platform block ────────────────────────────────────────────────────────────

func TestMapPlatformValuesForEnv_PlatformBlock(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	orgProfiles := domain.RoutingProfiles{
		string(domain.ExposeExternal): {IngressClassName: "nginx", ClusterIssuer: "letsencrypt"},
	}
	p := MapPlatformValuesForEnv(app, "prod", domain.AppEnvProd,
		"acme.com", "hello-prod", "prod-eks", "acme",
		orgProfiles, nil, nil)

	if p.Org != "acme" || p.Project != "demo" || p.App != "hello" {
		t.Errorf("identity = %+v, want org=acme project=demo app=hello", p)
	}
	if p.Env != "prod" || p.EnvType != "prod" || p.Cluster != "prod-eks" || p.Namespace != "hello-prod" {
		t.Errorf("env/cluster/ns = %+v", p)
	}
	if p.BaseDomain != "acme.com" {
		t.Errorf("BaseDomain = %q, want acme.com", p.BaseDomain)
	}
	if p.RoutingHost == "" || !strings.Contains(p.RoutingHost, "acme.com") {
		t.Errorf("RoutingHost = %q, want a host on acme.com", p.RoutingHost)
	}
	if p.IngressClassName != "nginx" || p.ClusterIssuer != "letsencrypt" {
		t.Errorf("ingress = %q/%q, want nginx/letsencrypt", p.IngressClassName, p.ClusterIssuer)
	}
	if p.ConfigMapName != "hello-config" || p.SecretName != "hello-secrets" {
		t.Errorf("env object names = %q/%q, want hello-config/hello-secrets", p.ConfigMapName, p.SecretName)
	}
	// The mapper never resolves an image tag — previews set it on the publisher.
	if p.ImageTag != "" {
		t.Errorf("ImageTag = %q, want empty (publisher-owned)", p.ImageTag)
	}
}

func TestMapPlatformValuesForEnv_Deterministic(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	a := MapPlatformValuesForEnv(app, "prod", domain.AppEnvProd, "acme.com", "hello-prod", "c1", "acme", nil, nil, nil)
	b := MapPlatformValuesForEnv(app, "prod", domain.AppEnvProd, "acme.com", "hello-prod", "c1", "acme", nil, nil, nil)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("MapPlatformValuesForEnv is not deterministic:\n%+v\n%+v", a, b)
	}
}

// TestMapPlatformValuesForEnv_PerTierRoutingTokens pins the per-tier routing
// context exposed as ((platform.{internal,external}*)). It is the regression
// guard for the reported bug: a per-cluster base-domain override must reach the
// tier base-domain tokens even when the tier's own RoutingProfile.baseDomain is
// blank — the tier base domain falls back profile → (env, cluster) base. It also
// covers cluster→env→org precedence for ingress class / issuer / gateway.
func TestMapPlatformValuesForEnv_PerTierRoutingTokens(t *testing.T) {
	app := webApp("tts", webComponent("web"))
	org := domain.RoutingProfiles{
		string(domain.ExposeInternal): {IngressClassName: "nginx-internal", ClusterIssuer: "internal-ca"},
		string(domain.ExposeExternal): {IngressClassName: "nginx", ClusterIssuer: "letsencrypt"},
	}
	// Cluster overrides only the internal tier (different ingress + issuer + a
	// gateway). Neither profile sets baseDomain, so both tier base domains must
	// fall back to the passed (cluster) base domain "aws.example.com".
	cluster := domain.RoutingProfiles{
		string(domain.ExposeInternal): {
			IngressClassName: "nginx-internal-aws",
			ClusterIssuer:    "letsencrypt-aws",
			Gateway:          &domain.GatewayRef{Name: "envoy-internal", Namespace: "envoy-gateway-system", SectionName: "https"},
		},
	}
	p := MapPlatformValuesForEnv(app, "staging", domain.AppEnvStaging,
		"aws.example.com", "tts-staging", "eks-aws", "acme",
		org, nil, cluster)

	// Bug fix: cluster base-domain override reaches BOTH tier base-domain tokens.
	if p.InternalBaseDomain != "aws.example.com" || p.ExternalBaseDomain != "aws.example.com" {
		t.Errorf("tier base domains = %q/%q, want both aws.example.com", p.InternalBaseDomain, p.ExternalBaseDomain)
	}
	// Cluster wins for internal; org fills external (no cluster override).
	if p.InternalIngressClassName != "nginx-internal-aws" || p.InternalClusterIssuer != "letsencrypt-aws" {
		t.Errorf("internal tier = %q/%q, want nginx-internal-aws/letsencrypt-aws (cluster)", p.InternalIngressClassName, p.InternalClusterIssuer)
	}
	if p.ExternalIngressClassName != "nginx" || p.ExternalClusterIssuer != "letsencrypt" {
		t.Errorf("external tier = %q/%q, want nginx/letsencrypt (org)", p.ExternalIngressClassName, p.ExternalClusterIssuer)
	}
	// Gateway resolved from the cluster internal profile; external tier has none.
	if p.InternalGatewayName != "envoy-internal" || p.InternalGatewayNamespace != "envoy-gateway-system" || p.InternalGatewaySectionName != "https" {
		t.Errorf("internal gateway = %q/%q/%q, want envoy-internal/envoy-gateway-system/https",
			p.InternalGatewayName, p.InternalGatewayNamespace, p.InternalGatewaySectionName)
	}
	if p.ExternalGatewayName != "" || p.ExternalGatewayNamespace != "" || p.ExternalGatewaySectionName != "" {
		t.Errorf("external gateway should be empty, got %q/%q/%q",
			p.ExternalGatewayName, p.ExternalGatewayNamespace, p.ExternalGatewaySectionName)
	}
}

// TestMapPlatformValuesForEnv_TierBaseDomainProfileWins verifies a profile's own
// baseDomain still wins over the (env, cluster) base for that tier, and that the
// two tiers can resolve to different domains.
func TestMapPlatformValuesForEnv_TierBaseDomainProfileWins(t *testing.T) {
	app := webApp("tts", webComponent("web"))
	org := domain.RoutingProfiles{
		string(domain.ExposeInternal): {IngressClassName: "nginx-internal", BaseDomain: "svc.internal.acme"},
		string(domain.ExposeExternal): {IngressClassName: "nginx"},
	}
	p := MapPlatformValuesForEnv(app, "prod", domain.AppEnvProd,
		"acme.com", "tts-prod", "", "acme",
		org, nil, nil)
	if p.InternalBaseDomain != "svc.internal.acme" {
		t.Errorf("internal base = %q, want svc.internal.acme (profile baseDomain wins)", p.InternalBaseDomain)
	}
	if p.ExternalBaseDomain != "acme.com" {
		t.Errorf("external base = %q, want acme.com (fallback to env base)", p.ExternalBaseDomain)
	}
}

func TestMapPlatformValuesForEnv_Preview(t *testing.T) {
	app := webApp("hello", webComponent("web"))
	p := MapPlatformValuesForEnv(app, "pr-42", domain.AppEnvPreview,
		"acme.com", "hello-pr-42", "", "acme", nil, nil, nil)

	if p.EnvType != "preview" {
		t.Errorf("EnvType = %q, want preview", p.EnvType)
	}
	// Preview host has the {env}.{app}.preview.{domain} shape.
	if !strings.Contains(p.RoutingHost, "preview.acme.com") {
		t.Errorf("preview RoutingHost = %q, want *.preview.acme.com", p.RoutingHost)
	}
	if p.Cluster != "" {
		t.Errorf("Cluster = %q, want empty (active mode)", p.Cluster)
	}
}

// ── ingress resolution ────────────────────────────────────────────────────────

func TestMapPlatformValuesForEnv_DisabledExposeYieldsNoIngress(t *testing.T) {
	c := webComponent("web")
	c.ExposeMode = domain.ExposeDisabled
	app := webApp("hello", c)
	org := domain.RoutingProfiles{
		string(domain.ExposeExternal): {IngressClassName: "nginx"},
	}
	p := MapPlatformValuesForEnv(app, "staging", domain.AppEnvStaging, "localhost", "", "", "", org, nil, nil)
	if p.IngressClassName != "" || p.ClusterIssuer != "" {
		t.Errorf("disabled expose should yield empty ingress tokens, got %q/%q", p.IngressClassName, p.ClusterIssuer)
	}
}

func TestMapPlatformValuesForEnv_ZeroComponents(t *testing.T) {
	// No components (a plain single-chart BYO app): routing host derives from
	// the app name on the plain base domain, ingress tokens stay empty.
	app := webApp("solo")
	p := MapPlatformValuesForEnv(app, "staging", domain.AppEnvStaging, "acme.com", "solo-staging", "", "", nil, nil, nil)
	if p.RoutingHost == "" || !strings.Contains(p.RoutingHost, "acme.com") {
		t.Errorf("RoutingHost = %q, want a host on acme.com", p.RoutingHost)
	}
	if p.IngressClassName != "" || p.Component != "" {
		t.Errorf("zero-component app: ingressClassName=%q component=%q, want empty", p.IngressClassName, p.Component)
	}
}

// TestMapPlatformValuesForEnv_ClusterRoutingOverride proves per-cluster routing:
// two clusters with different routing profiles yield different routing hosts +
// ingress classes for the same app/env.
func TestMapPlatformValuesForEnv_ClusterRoutingOverride(t *testing.T) {
	app := &domain.App{
		Name:        "web",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Template:   domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{{Name: "web", Type: domain.ComponentWeb, Enabled: true, ExposeMode: domain.ExposeExternal}},
		},
	}
	org := domain.RoutingProfiles{
		string(domain.ExposeExternal): {IngressClassName: "nginx", ClusterIssuer: "le-prod", BaseDomain: "example.com"},
	}
	aks := domain.RoutingProfiles{
		string(domain.ExposeExternal): {IngressClassName: "webapprouting.kubernetes.azure.com", ClusterIssuer: "le-azure", BaseDomain: "azure.example.com"},
	}
	eks := domain.RoutingProfiles{
		string(domain.ExposeExternal): {IngressClassName: "alb", ClusterIssuer: "le-aws", BaseDomain: "aws.example.com"},
	}

	pAKS := MapPlatformValuesForEnv(app, "prod", domain.AppEnvProd, "example.com", "", "aks", "", org, nil, aks)
	pEKS := MapPlatformValuesForEnv(app, "prod", domain.AppEnvProd, "example.com", "", "eks", "", org, nil, eks)

	if pAKS.RoutingHost == pEKS.RoutingHost {
		t.Fatalf("hosts should differ per cluster, both = %q", pAKS.RoutingHost)
	}
	if !strings.Contains(pAKS.RoutingHost, "azure.example.com") {
		t.Errorf("AKS host = %q, want azure.example.com", pAKS.RoutingHost)
	}
	if !strings.Contains(pEKS.RoutingHost, "aws.example.com") {
		t.Errorf("EKS host = %q, want aws.example.com", pEKS.RoutingHost)
	}
	if pAKS.IngressClassName != "webapprouting.kubernetes.azure.com" {
		t.Errorf("AKS ingress class = %q, want webapprouting.kubernetes.azure.com", pAKS.IngressClassName)
	}
	if pEKS.IngressClassName != "alb" {
		t.Errorf("EKS ingress class = %q, want alb", pEKS.IngressClassName)
	}
}

// ── routing component selection ───────────────────────────────────────────────

func TestResolveRoutingComponent_PrefersExternalOverInternal(t *testing.T) {
	// admin (alphabetically first, internal) should NOT win against api
	// (alphabetically later, external). Documents the tier preference.
	admin := domain.ComponentSpec{Name: "admin", Type: domain.ComponentWeb, Enabled: true, ExposeMode: domain.ExposeInternal}
	api := domain.ComponentSpec{Name: "api", Type: domain.ComponentWeb, Enabled: true, ExposeMode: domain.ExposeExternal}
	got := resolveRoutingComponent([]domain.ComponentSpec{admin, api})
	if got != "api" {
		t.Errorf("routing component = %q, want api (external should beat internal)", got)
	}
}

func TestStripScheme(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http://hello.staging.localhost", "hello.staging.localhost"},
		{"https://hello.prod.example.com", "hello.prod.example.com"},
		{"hello.prod.localhost", "hello.prod.localhost"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripScheme(tt.input)
		if got != tt.want {
			t.Errorf("stripScheme(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
