package gitops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

var testGateways = map[domain.ExposeMode]domain.GatewayRef{
	domain.ExposeExternal: {Name: "edge", Namespace: "gateways", SectionName: "https"},
}

func stackRoutes() []domain.RouteSpec {
	return []domain.RouteSpec{{
		Name:      "web",
		Hostnames: []string{"voiceai-livekit((platform.previewSuffix)).acme.com"},
		Rules: []domain.RouteRule{
			{PathPrefix: "/", Backend: domain.RouteBackend{App: "cloud", Component: "web", Port: 80}},
			{PathPrefix: "/sh", Backend: domain.RouteBackend{App: "sh", Component: "web", Port: 80}},
		},
	}}
}

func TestBuildHTTPRoutes_OwnerRulesOnly(t *testing.T) {
	// sh's platform dir gets only the /sh rule, local namespace, no backend ns.
	routes, err := gitops.BuildHTTPRoutes(gitops.RouteRenderInput{
		Owner:       "sh",
		Routes:      domain.ExpandStackRoutes(stackRoutes(), "sh"),
		Env:         "staging",
		Interpolate: func(s string) string { return strings.ReplaceAll(s, "((platform.previewSuffix))", "") },
		Gateways:    testGateways,
		LocalNS:     "demo-stack-staging",
		BackendNS:   func(app string) (string, bool) { return "demo-stack-staging", true },
		Labels:      map[string]string{"suparship.io/app": "sh"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(routes))
	}
	r := routes[0]
	if r.Metadata.Name != "sh-web" || r.Metadata.Namespace != "demo-stack-staging" || r.Metadata.Labels["suparship.io/route"] != "web" {
		t.Errorf("metadata = %+v", r.Metadata)
	}
	if r.Spec.Hostnames[0] != "voiceai-livekit.acme.com" || r.Spec.ParentRefs[0].Name != "edge" || r.Spec.ParentRefs[0].SectionName != "https" {
		t.Errorf("spec = %+v", r.Spec)
	}
	if len(r.Spec.Rules) != 1 || r.Spec.Rules[0].Matches[0].Path.Value != "/sh" {
		t.Fatalf("rules = %+v", r.Spec.Rules)
	}
	if ref := r.Spec.Rules[0].BackendRefs[0]; ref.Name != "sh-web" || ref.Port != 80 || ref.Namespace != "" {
		t.Errorf("backendRef = %+v, want sh-web:80 local", ref)
	}
}

func TestBuildHTTPRoutes_BackendSwitchAndCrossAppBackend(t *testing.T) {
	// The owner's own backend resolves to the routed preview's namespace; a
	// rule forwarding to another app resolves to that app's namespace.
	app := []domain.RouteSpec{{Name: "api", Hostnames: []string{"api.acme.com"}, Rules: []domain.RouteRule{
		{PathPrefix: "/", Backend: domain.RouteBackend{Port: 8080}},
		{PathPrefix: "/auth", Backend: domain.RouteBackend{App: "auth", Port: 80}},
		{PathPrefix: "/gone", Backend: domain.RouteBackend{App: "missing", Port: 80}},
	}}}
	ns := map[string]string{"hello": "demo-hello-preview-pr-42", "auth": "demo-auth-staging"}
	routes, err := gitops.BuildHTTPRoutes(gitops.RouteRenderInput{
		Owner: "hello", Routes: app, Gateways: testGateways, LocalNS: "demo-hello-staging",
		BackendNS: func(a string) (string, bool) { n, ok := ns[a]; return n, ok },
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	rules := routes[0].Spec.Rules
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2 (undeployed backend dropped): %+v", len(rules), rules)
	}
	if rules[0].BackendRefs[0].Name != "hello" || rules[0].BackendRefs[0].Namespace != "demo-hello-preview-pr-42" {
		t.Errorf("switched backend = %+v", rules[0].BackendRefs[0])
	}
	if rules[1].BackendRefs[0].Name != "auth" || rules[1].BackendRefs[0].Namespace != "demo-auth-staging" {
		t.Errorf("cross-app backend = %+v", rules[1].BackendRefs[0])
	}
}

func TestBuildHTTPRoutes_CompositePreviewMergesSiblings(t *testing.T) {
	// sh's preview: its own /sh rule locally, cloud's / rule cross-namespace,
	// merged into ONE HTTPRoute on the preview host.
	interp := func(s string) string { return strings.ReplaceAll(s, "((platform.previewSuffix))", "-pr-42") }
	routes, err := gitops.BuildHTTPRoutes(gitops.RouteRenderInput{
		Owner:       "sh",
		Routes:      domain.ExpandStackRoutes(stackRoutes(), "sh"),
		Env:         "pr-42",
		Interpolate: interp,
		Gateways:    testGateways,
		LocalNS:     "demo-sh-preview-pr-42",
		BackendNS:   func(a string) (string, bool) { return "demo-sh-preview-pr-42", a == "sh" },
		Siblings:    []gitops.SiblingRoutes{{App: "cloud", Namespace: "demo-stack-staging", Routes: domain.ExpandStackRoutes(stackRoutes(), "cloud")}},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1 merged: %+v", len(routes), routes)
	}
	r := routes[0]
	if r.Spec.Hostnames[0] != "voiceai-livekit-pr-42.acme.com" {
		t.Errorf("host = %q", r.Spec.Hostnames[0])
	}
	if len(r.Spec.Rules) != 2 {
		t.Fatalf("rules = %+v", r.Spec.Rules)
	}
	own, sib := r.Spec.Rules[0], r.Spec.Rules[1]
	if own.Matches[0].Path.Value != "/sh" || own.BackendRefs[0].Namespace != "" {
		t.Errorf("own rule = %+v, want /sh local", own)
	}
	if sib.Matches[0].Path.Value != "/" || sib.BackendRefs[0].Name != "cloud-web" || sib.BackendRefs[0].Namespace != "demo-stack-staging" {
		t.Errorf("sibling rule = %+v, want / → cloud-web in demo-stack-staging", sib)
	}
}

func TestBuildHTTPRoutes_Errors(t *testing.T) {
	base := gitops.RouteRenderInput{Owner: "hello", LocalNS: "ns", Gateways: testGateways,
		Routes: []domain.RouteSpec{{Rules: []domain.RouteRule{{PathPrefix: "/", Backend: domain.RouteBackend{Port: 80}}}}}}
	// Unresolved token (no external base domain).
	if _, err := gitops.BuildHTTPRoutes(base); err == nil || !strings.Contains(err.Error(), "unresolved token") {
		t.Errorf("unresolved token: err = %v", err)
	}
	// Missing gateway for the tier.
	noGW := base
	noGW.Gateways = nil
	noGW.Interpolate = func(string) string { return "hello.acme.com" }
	if _, err := gitops.BuildHTTPRoutes(noGW); err == nil || !strings.Contains(err.Error(), "neither a Gateway nor an IngressClass") {
		t.Errorf("missing gateway: err = %v", err)
	}
}

func TestBuildReferenceGrants(t *testing.T) {
	grants := gitops.BuildReferenceGrants("demo-cloud-staging", []string{"demo-sh-preview-pr-42", "demo-cloud-staging", "demo-sh-preview-pr-42", "", "demo-sh-preview-pr-7"}, map[string]string{"k": "v"})
	if len(grants) != 2 {
		t.Fatalf("grants = %d, want 2 (deduped, local skipped): %+v", len(grants), grants)
	}
	g := grants[0]
	if g.Metadata.Namespace != "demo-cloud-staging" || g.Spec.From[0].Namespace != "demo-sh-preview-pr-42" || g.Spec.From[0].Kind != "HTTPRoute" || g.Spec.To[0].Kind != "Service" {
		t.Errorf("grant = %+v", g)
	}
	if g.Metadata.Labels["k"] != "v" {
		t.Errorf("labels = %v", g.Metadata.Labels)
	}
}

func readYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return m
}

// TestPublish_PlatformRoutesLandInAppResources proves the render path through
// the publisher: a stable env writes route-*.yaml (+ referencegrant-*.yaml)
// next to the env ConfigMap; a republish without routes prunes them; a preview
// writes the composite route and the backend-switch grant.
func TestPublish_PlatformRoutesLandInAppResources(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)
	profiles := domain.RoutingProfiles{"external": {IngressClassName: "eg", BaseDomain: "acme.com", Gateway: &domain.GatewayRef{Name: "edge", Namespace: "gateways"}}}
	p.SetRoutingProfilesForTest(profiles)
	app := &domain.App{Name: "sh", ProjectName: "demo", Spec: domain.AppSpec{
		Template: domain.AppTemplateRef{Name: "voiceai-livekit-agent"},
		Stack:    "voiceai-livekit",
	}}
	env := gitops.AppPublishEnv{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "acme.com",
		Namespace: "demo-stack-staging", RoutingProfiles: profiles,
		Routes: gitops.RouteInputs{
			Routes:              domain.ExpandStackRoutes(stackRoutes(), "sh"),
			PlatformRouted:      true,
			BackendNamespaces:   map[string]string{"sh": "demo-stack-staging"},
			GrantFromNamespaces: []string{"demo-cloud-preview-pr-9"},
		},
	}
	if err := p.PublishAppFilesForTest(dir, app, []gitops.AppPublishEnv{env}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	resDir := filepath.Join(dir, "_app-resources", "staging", "demo", "sh")
	route := readYAML(t, filepath.Join(resDir, "route-sh-web.yaml"))
	spec, _ := route["spec"].(map[string]any)
	hosts, _ := spec["hostnames"].([]any)
	if len(hosts) != 1 || hosts[0] != "voiceai-livekit.acme.com" {
		t.Errorf("stable route hostnames = %v", hosts)
	}
	if _, err := os.Stat(filepath.Join(resDir, "referencegrant-allow-routes-from-demo-cloud-preview-pr-9.yaml")); err != nil {
		t.Errorf("expected a ReferenceGrant for the sibling preview: %v", err)
	}

	// Republish with no routes → pruned.
	env.Routes = gitops.RouteInputs{}
	if err := p.PublishAppFilesForTest(dir, app, []gitops.AppPublishEnv{env}); err != nil {
		t.Fatalf("republish: %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(resDir, "route-*.yaml")); len(m) != 0 {
		t.Errorf("routes should be pruned, found %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(resDir, "referencegrant-*.yaml")); len(m) != 0 {
		t.Errorf("grants should be pruned, found %v", m)
	}

	// Preview: composite route on the preview host + backend-switch grant.
	spec2 := gitops.PreviewPublishSpec{
		PreviewName: "pr-42", BaseEnv: "staging", ClusterServer: "https://kubernetes.default.svc",
		Namespace: "demo-sh-preview-pr-42", BaseDomain: "acme.com", ImageTag: "pr-42-abc",
		Routes: gitops.RouteInputs{
			Routes:              domain.ExpandStackRoutes(stackRoutes(), "sh"),
			PlatformRouted:      true,
			Siblings:            []gitops.SiblingRoutes{{App: "cloud", Namespace: "demo-stack-staging", Routes: domain.ExpandStackRoutes(stackRoutes(), "cloud")}},
			GrantFromNamespaces: []string{"demo-stack-staging"},
		},
	}
	if err := p.PublishPreviewForTest(dir, app, spec2); err != nil {
		t.Fatalf("publish preview: %v", err)
	}
	pDir := filepath.Join(dir, "_app-resources", "previews", "staging", "demo", "pr-42", "sh")
	proute := readYAML(t, filepath.Join(pDir, "route-sh-web.yaml"))
	pspec, _ := proute["spec"].(map[string]any)
	phosts, _ := pspec["hostnames"].([]any)
	if len(phosts) != 1 || phosts[0] != "voiceai-livekit-pr-42.acme.com" {
		t.Errorf("preview route hostnames = %v", phosts)
	}
	rules, _ := pspec["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("preview rules = %v, want own + sibling", rules)
	}
	if _, err := os.Stat(filepath.Join(pDir, "referencegrant-allow-routes-from-demo-stack-staging.yaml")); err != nil {
		t.Errorf("expected the backend-switch ReferenceGrant in the preview dir: %v", err)
	}
}

var testIngressTiers = map[domain.ExposeMode]gitops.IngressTierRef{
	domain.ExposeExternal: {ClassName: "nginx", ClusterIssuer: "letsencrypt"},
}

func TestBuildRoutes_IngressTier(t *testing.T) {
	// sh's stable dir on an ingress-only tier: an Ingress with its own path,
	// TLS from the issuer, no HTTPRoute, no shim (local backend).
	objs, err := gitops.BuildRoutes(gitops.RouteRenderInput{
		Owner:        "sh",
		Routes:       domain.ExpandStackRoutes(stackRoutes(), "sh"),
		Interpolate:  func(s string) string { return strings.ReplaceAll(s, "((platform.previewSuffix))", "") },
		IngressTiers: testIngressTiers,
		LocalNS:      "demo-stack-staging",
		BackendNS:    func(string) (string, bool) { return "demo-stack-staging", true },
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(objs.HTTPRoutes) != 0 || len(objs.Services) != 0 || len(objs.Ingresses) != 1 {
		t.Fatalf("objects = %+v", objs)
	}
	ing := objs.Ingresses[0]
	if ing.Metadata.Name != "sh-web" || ing.Spec.IngressClassName != "nginx" {
		t.Errorf("ingress = %+v", ing)
	}
	if ing.Metadata.Annotations["cert-manager.io/cluster-issuer"] != "letsencrypt" || len(ing.Spec.TLS) != 1 || ing.Spec.TLS[0].SecretName != "sh-web-tls" {
		t.Errorf("tls = %+v annotations = %v", ing.Spec.TLS, ing.Metadata.Annotations)
	}
	rule := ing.Spec.Rules[0]
	if rule.Host != "voiceai-livekit.acme.com" || len(rule.HTTP.Paths) != 1 || rule.HTTP.Paths[0].Path != "/sh" || rule.HTTP.Paths[0].PathType != "Prefix" {
		t.Errorf("rule = %+v", rule)
	}
	if b := rule.HTTP.Paths[0].Backend.Service; b.Name != "sh-web" || b.Port.Number != 80 {
		t.Errorf("backend = %+v", b)
	}
}

func TestBuildRoutes_IngressCrossNamespaceUsesExternalNameShim(t *testing.T) {
	// Composite preview on an ingress tier: the sibling's rule is bridged by an
	// ExternalName Service in the preview namespace; both paths on one Ingress.
	interp := func(s string) string { return strings.ReplaceAll(s, "((platform.previewSuffix))", "-pr-42") }
	objs, err := gitops.BuildRoutes(gitops.RouteRenderInput{
		Owner:        "sh",
		Routes:       domain.ExpandStackRoutes(stackRoutes(), "sh"),
		Interpolate:  interp,
		IngressTiers: testIngressTiers,
		LocalNS:      "demo-sh-preview-pr-42",
		BackendNS:    func(a string) (string, bool) { return "demo-sh-preview-pr-42", a == "sh" },
		Siblings:     []gitops.SiblingRoutes{{App: "cloud", Namespace: "demo-stack-staging", Routes: domain.ExpandStackRoutes(stackRoutes(), "cloud")}},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(objs.Ingresses) != 1 || len(objs.Services) != 1 {
		t.Fatalf("objects = %+v", objs)
	}
	paths := objs.Ingresses[0].Spec.Rules[0].HTTP.Paths
	if len(paths) != 2 || paths[0].Backend.Service.Name != "sh-web" || paths[1].Backend.Service.Name != "route-cloud-cloud-web" {
		t.Errorf("paths = %+v", paths)
	}
	shim := objs.Services[0]
	if shim.Metadata.Name != "route-cloud-cloud-web" || shim.Spec.Type != "ExternalName" || shim.Spec.ExternalName != "cloud-web.demo-stack-staging.svc.cluster.local" || shim.Spec.Ports[0].Port != 80 {
		t.Errorf("shim = %+v", shim)
	}
	if objs.Ingresses[0].Spec.Rules[0].Host != "voiceai-livekit-pr-42.acme.com" {
		t.Errorf("host = %q", objs.Ingresses[0].Spec.Rules[0].Host)
	}
}

func TestPublish_IngressTierWritesIngressAndShimsNoGrants(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)
	profiles := domain.RoutingProfiles{"external": {IngressClassName: "nginx", BaseDomain: "localhost"}}
	p.SetRoutingProfilesForTest(profiles)
	app := &domain.App{Name: "sh", ProjectName: "demo", Spec: domain.AppSpec{Template: domain.AppTemplateRef{Name: "voiceai-livekit-agent"}}}
	env := gitops.AppPublishEnv{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Namespace: "demo-sh-staging", RoutingProfiles: profiles,
		Routes: gitops.RouteInputs{
			Routes:              domain.ExpandStackRoutes(stackRoutes(), "sh"),
			PlatformRouted:      true,
			BackendNamespaces:   map[string]string{"sh": "demo-sh-preview-pr-42"}, // backend switch
			GrantFromNamespaces: []string{"demo-cloud-preview-pr-9"},
		},
	}
	if err := p.PublishAppFilesForTest(dir, app, []gitops.AppPublishEnv{env}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	resDir := filepath.Join(dir, "_app-resources", "staging", "demo", "sh")
	ing := readYAML(t, filepath.Join(resDir, "ingress-sh-web.yaml"))
	if ing["kind"] != "Ingress" {
		t.Errorf("kind = %v", ing["kind"])
	}
	if _, err := os.Stat(filepath.Join(resDir, "service-route-sh-sh-web.yaml")); err != nil {
		t.Errorf("expected an ExternalName shim for the switched backend: %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(resDir, "referencegrant-*.yaml")); len(m) != 0 {
		t.Errorf("an ingress edge must not write ReferenceGrants (no CRD without Gateway API), found %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(resDir, "route-*.yaml")); len(m) != 0 {
		t.Errorf("no HTTPRoute on an ingress edge, found %v", m)
	}
}

// TestPublish_ExplicitIngressKindBesideGateway proves routeKind: ingress wins
// over a configured Gateway — the profile keeps the Gateway (charts' own
// HTTPRoutes still read the tokens) while platform routes render as Ingresses.
func TestPublish_ExplicitIngressKindBesideGateway(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)
	profiles := domain.RoutingProfiles{"external": {
		IngressClassName: "nginx", BaseDomain: "localhost",
		Gateway:   &domain.GatewayRef{Name: "edge", Namespace: "gateways"},
		RouteKind: domain.RouteKindIngress,
	}}
	p.SetRoutingProfilesForTest(profiles)
	app := &domain.App{Name: "sh", ProjectName: "demo", Spec: domain.AppSpec{Template: domain.AppTemplateRef{Name: "voiceai-livekit-agent"}}}
	env := gitops.AppPublishEnv{
		EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost",
		Namespace: "demo-sh-staging", RoutingProfiles: profiles,
		Routes: gitops.RouteInputs{
			Routes:            domain.ExpandStackRoutes(stackRoutes(), "sh"),
			PlatformRouted:    true,
			BackendNamespaces: map[string]string{"sh": "demo-sh-staging"},
		},
	}
	if err := p.PublishAppFilesForTest(dir, app, []gitops.AppPublishEnv{env}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	resDir := filepath.Join(dir, "_app-resources", "staging", "demo", "sh")
	if ing := readYAML(t, filepath.Join(resDir, "ingress-sh-web.yaml")); ing["kind"] != "Ingress" {
		t.Errorf("kind = %v, want Ingress", ing["kind"])
	}
	if m, _ := filepath.Glob(filepath.Join(resDir, "route-*.yaml")); len(m) != 0 {
		t.Errorf("expected no HTTPRoute with routeKind=ingress, got %v", m)
	}
}
