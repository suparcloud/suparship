package gitops

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/helmvalues"
	"github.com/suparcloud/suparship/internal/platform"
)

// Platform-owned routing: suparship renders Gateway API HTTPRoutes (and the
// ReferenceGrants cross-namespace backends need) into an app's platform
// resources, next to its env ConfigMap and ExternalSecret. See
// domain.RouteSpec for the model. Objects are plain structs marshalled to YAML
// (no gateway-api module dependency), following argocd.go / appset.go.

// HTTPRoute is gateway.networking.k8s.io/v1 HTTPRoute, the subset we render.
type HTTPRoute struct {
	APIVersion string        `json:"apiVersion" yaml:"apiVersion"`
	Kind       string        `json:"kind"       yaml:"kind"`
	Metadata   ObjectMeta    `json:"metadata"   yaml:"metadata"`
	Spec       HTTPRouteSpec `json:"spec"       yaml:"spec"`
}

type HTTPRouteSpec struct {
	ParentRefs []HTTPRouteParentRef `json:"parentRefs"          yaml:"parentRefs"`
	Hostnames  []string             `json:"hostnames,omitempty" yaml:"hostnames,omitempty"`
	Rules      []HTTPRouteRule      `json:"rules"               yaml:"rules"`
}

type HTTPRouteParentRef struct {
	Name        string `json:"name"                  yaml:"name"`
	Namespace   string `json:"namespace,omitempty"   yaml:"namespace,omitempty"`
	SectionName string `json:"sectionName,omitempty" yaml:"sectionName,omitempty"`
}

type HTTPRouteRule struct {
	Matches     []HTTPRouteMatch `json:"matches"     yaml:"matches"`
	BackendRefs []HTTPBackendRef `json:"backendRefs" yaml:"backendRefs"`
}

type HTTPRouteMatch struct {
	Path HTTPPathMatch `json:"path" yaml:"path"`
}

type HTTPPathMatch struct {
	Type  string `json:"type"  yaml:"type"`
	Value string `json:"value" yaml:"value"`
}

// HTTPBackendRef targets a Service; Namespace is set only for a cross-namespace
// backend (which then also needs a ReferenceGrant in that namespace).
type HTTPBackendRef struct {
	Name      string `json:"name"                yaml:"name"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Port      int    `json:"port"                yaml:"port"`
}

// ReferenceGrant is gateway.networking.k8s.io/v1beta1 ReferenceGrant: it lives
// in the BACKEND Service's namespace and allows HTTPRoutes from another
// namespace to target Services here.
type ReferenceGrant struct {
	APIVersion string             `json:"apiVersion" yaml:"apiVersion"`
	Kind       string             `json:"kind"       yaml:"kind"`
	Metadata   ObjectMeta         `json:"metadata"   yaml:"metadata"`
	Spec       ReferenceGrantSpec `json:"spec"       yaml:"spec"`
}

type ReferenceGrantSpec struct {
	From []ReferenceGrantFrom `json:"from" yaml:"from"`
	To   []ReferenceGrantTo   `json:"to"   yaml:"to"`
}

type ReferenceGrantFrom struct {
	Group     string `json:"group"     yaml:"group"`
	Kind      string `json:"kind"      yaml:"kind"`
	Namespace string `json:"namespace" yaml:"namespace"`
}

type ReferenceGrantTo struct {
	Group string `json:"group" yaml:"group"`
	Kind  string `json:"kind"  yaml:"kind"`
}

// Ingress is networking.k8s.io/v1 Ingress, rendered for a tier whose routing
// profile has an IngressClass but no Gateway. Cross-namespace backends are
// bridged with an ExternalName Service shim in the Ingress's namespace (an
// Ingress backend must be local), which ingress-nginx proxies through.
type Ingress struct {
	APIVersion string      `json:"apiVersion" yaml:"apiVersion"`
	Kind       string      `json:"kind"       yaml:"kind"`
	Metadata   ObjectMeta  `json:"metadata"   yaml:"metadata"`
	Spec       IngressSpec `json:"spec"       yaml:"spec"`
}

type IngressSpec struct {
	IngressClassName string        `json:"ingressClassName,omitempty" yaml:"ingressClassName,omitempty"`
	TLS              []IngressTLS  `json:"tls,omitempty"              yaml:"tls,omitempty"`
	Rules            []IngressRule `json:"rules"                      yaml:"rules"`
}

type IngressTLS struct {
	Hosts      []string `json:"hosts"      yaml:"hosts"`
	SecretName string   `json:"secretName" yaml:"secretName"`
}

type IngressRule struct {
	Host string      `json:"host" yaml:"host"`
	HTTP IngressHTTP `json:"http" yaml:"http"`
}

type IngressHTTP struct {
	Paths []IngressPath `json:"paths" yaml:"paths"`
}

type IngressPath struct {
	Path     string         `json:"path"     yaml:"path"`
	PathType string         `json:"pathType" yaml:"pathType"`
	Backend  IngressBackend `json:"backend"  yaml:"backend"`
}

type IngressBackend struct {
	Service IngressServiceBackend `json:"service" yaml:"service"`
}

type IngressServiceBackend struct {
	Name string      `json:"name" yaml:"name"`
	Port IngressPort `json:"port" yaml:"port"`
}

type IngressPort struct {
	Number int `json:"number" yaml:"number"`
}

// ExternalNameService is a v1 Service of type ExternalName: the shim that
// lets an Ingress forward to a Service in another namespace.
type ExternalNameService struct {
	APIVersion string                  `json:"apiVersion" yaml:"apiVersion"`
	Kind       string                  `json:"kind"       yaml:"kind"`
	Metadata   ObjectMeta              `json:"metadata"   yaml:"metadata"`
	Spec       ExternalNameServiceSpec `json:"spec"       yaml:"spec"`
}

type ExternalNameServiceSpec struct {
	Type         string        `json:"type"         yaml:"type"`
	ExternalName string        `json:"externalName" yaml:"externalName"`
	Ports        []ServicePort `json:"ports"        yaml:"ports"`
}

type ServicePort struct {
	Name       string `json:"name"       yaml:"name"`
	Port       int    `json:"port"       yaml:"port"`
	TargetPort int    `json:"targetPort" yaml:"targetPort"`
}

// IngressTierRef is a tier's Ingress rendering context when it has no Gateway.
type IngressTierRef struct {
	ClassName     string
	ClusterIssuer string
}

// RouteObjects is what a route set renders to: HTTPRoutes for gateway tiers,
// Ingresses (+ ExternalName shims for cross-namespace backends) for ingress
// tiers.
type RouteObjects struct {
	HTTPRoutes []HTTPRoute
	Ingresses  []Ingress
	Services   []ExternalNameService
}

const (
	gatewayAPIGroup      = "gateway.networking.k8s.io"
	httpRouteAPIVersion  = gatewayAPIGroup + "/v1"
	refGrantAPIVersion   = gatewayAPIGroup + "/v1beta1"
	labelRoute           = "suparship.io/route"
	routeFilePrefix      = "route-"
	refGrantFilePrefix   = "referencegrant-"
	ingressFilePrefix    = "ingress-"
	shimFilePrefix       = "service-"
	certManagerIssuerKey = "cert-manager.io/cluster-issuer"
	unresolvedTokenMark  = "(("
	routeObjectNameLimit = 63
)

// SiblingRoutes are another app's effective routes rendered INTO this app's
// route set cross-namespace — the composite preview: a single-app preview
// serves the shared hostname in preview form with its own paths locally and
// every other app's paths forwarded to that app's base-env Services.
type SiblingRoutes struct {
	App       string
	Namespace string // the sibling's Service namespace in the base env
	Routes    []domain.RouteSpec
}

// RouteRenderInput is everything BuildHTTPRoutes needs, already resolved: the
// publisher stays free of stores and the builder stays pure.
type RouteRenderInput struct {
	// Owner is the app whose platform dir the routes are written into; its
	// effective routes (stack routes already expanded, see
	// domain.ExpandStackRoutes) are Routes.
	Owner  string
	Routes []domain.RouteSpec
	// Env is the environment (or preview) name, for labels.
	Env string
	// Interpolate resolves ((platform.*)) tokens in hostnames for this env or
	// preview (a platform.Context built from the mapper's values).
	Interpolate func(string) string
	// Gateways is the parentRef per tier, from the resolved routing profiles.
	// A tier present here renders HTTPRoutes.
	Gateways map[domain.ExposeMode]domain.GatewayRef
	// IngressTiers is the Ingress context per tier WITHOUT a Gateway: such a
	// tier renders an Ingress (+ ExternalName shims for cross-namespace
	// backends) instead.
	IngressTiers map[domain.ExposeMode]IngressTierRef
	// LocalNS is the namespace the HTTPRoute is applied into (the owner's).
	LocalNS string
	// BackendNS resolves a backend app to the namespace its Service lives in
	// for this render; !ok drops the rule (app not deployed here). The backend
	// switch is expressed here: the owner's own namespace becomes the routed
	// preview's while route-to-preview is active.
	BackendNS func(app string) (ns string, ok bool)
	// Siblings are other apps' routes merged in cross-namespace (previews only).
	Siblings []SiblingRoutes
	// Labels are stamped on every object (identity + managed-by).
	Labels map[string]string
}

// BuildHTTPRoutes renders the owner's routes (plus any sibling routes merged
// by hostname) as HTTPRoutes only — the gateway-tier view of BuildRoutes.
func BuildHTTPRoutes(in RouteRenderInput) ([]HTTPRoute, error) {
	out, err := BuildRoutes(in)
	if err != nil {
		return nil, err
	}
	return out.HTTPRoutes, nil
}

// resolvedRule is one rendered rule before it is shaped into an HTTPRoute
// rule or an Ingress path.
type resolvedRule struct {
	prefix    string
	service   string
	port      int
	namespace string // "" = local
	app       string // backend app (for shim naming)
}

// BuildRoutes renders the owner's routes (plus any sibling routes merged by
// hostname): a route whose tier has a Gateway becomes an HTTPRoute, one whose
// tier only has an IngressClass becomes an Ingress with ExternalName shims for
// any cross-namespace backend. A route with no resolvable rule is omitted.
func BuildRoutes(in RouteRenderInput) (RouteObjects, error) {
	if in.Interpolate == nil {
		in.Interpolate = func(s string) string { return s }
	}
	if in.BackendNS == nil {
		in.BackendNS = func(string) (string, bool) { return in.LocalNS, true }
	}
	var out RouteObjects
	routeIndex := map[string]int{}   // tier|hostnames → HTTPRoute position, for sibling merging
	ingressIndex := map[string]int{} // tier|hostnames → Ingress position
	shims := map[string]bool{}

	render := func(app string, routes []domain.RouteSpec, defaultHost string, namePrefix string) error {
		for i, r := range routes {
			tier := r.EffectiveTier()
			gw, hasGW := in.Gateways[tier]
			ing, hasIng := in.IngressTiers[tier]
			if (!hasGW || gw.Name == "") && (!hasIng || ing.ClassName == "") {
				return fmt.Errorf("route %q: tier %q has neither a Gateway nor an IngressClass in the routing profile", r.EffectiveName(i), tier)
			}
			hosts := make([]string, 0, len(r.Hostnames)+1)
			for _, h := range r.EffectiveHostnames(defaultHost) {
				resolved := in.Interpolate(h)
				if strings.Contains(resolved, unresolvedTokenMark) {
					return fmt.Errorf("route %q: hostname %q has an unresolved token (%q); is the tier's base domain configured?", r.EffectiveName(i), h, resolved)
				}
				hosts = append(hosts, resolved)
			}
			var resolved []resolvedRule
			for _, rule := range r.Rules {
				backendApp := rule.Backend.AppName(app)
				ns, ok := in.BackendNS(backendApp)
				if !ok {
					continue
				}
				rr := resolvedRule{prefix: rule.PathPrefix, service: rule.Backend.ServiceName(app), port: rule.Backend.Port, app: backendApp}
				if ns != in.LocalNS {
					rr.namespace = ns
				}
				resolved = append(resolved, rr)
			}
			if len(resolved) == 0 {
				continue
			}
			key := string(tier) + "|" + strings.Join(hosts, ",")
			name := routeObjectName(namePrefix, r.EffectiveName(i))
			labels := map[string]string{}
			for k, v := range in.Labels {
				labels[k] = v
			}
			labels[labelRoute] = r.EffectiveName(i)

			if hasGW && gw.Name != "" {
				rules := make([]HTTPRouteRule, 0, len(resolved))
				for _, rr := range resolved {
					rules = append(rules, HTTPRouteRule{
						Matches:     []HTTPRouteMatch{{Path: HTTPPathMatch{Type: "PathPrefix", Value: rr.prefix}}},
						BackendRefs: []HTTPBackendRef{{Name: rr.service, Namespace: rr.namespace, Port: rr.port}},
					})
				}
				if pos, merged := routeIndex[key]; merged {
					out.HTTPRoutes[pos].Spec.Rules = append(out.HTTPRoutes[pos].Spec.Rules, rules...)
					continue
				}
				routeIndex[key] = len(out.HTTPRoutes)
				out.HTTPRoutes = append(out.HTTPRoutes, HTTPRoute{
					APIVersion: httpRouteAPIVersion,
					Kind:       "HTTPRoute",
					Metadata:   ObjectMeta{Name: name, Namespace: in.LocalNS, Labels: labels},
					Spec: HTTPRouteSpec{
						ParentRefs: []HTTPRouteParentRef{{Name: gw.Name, Namespace: gw.Namespace, SectionName: gw.SectionName}},
						Hostnames:  hosts,
						Rules:      rules,
					},
				})
				continue
			}

			// Ingress tier: local backends point at the Service; cross-namespace
			// backends go through an ExternalName shim in this namespace.
			paths := make([]IngressPath, 0, len(resolved))
			for _, rr := range resolved {
				svc := rr.service
				if rr.namespace != "" {
					svc = routeObjectName("route-"+rr.app, rr.service)
					if !shims[svc] {
						shims[svc] = true
						out.Services = append(out.Services, ExternalNameService{
							APIVersion: "v1",
							Kind:       "Service",
							Metadata:   ObjectMeta{Name: svc, Namespace: in.LocalNS, Labels: labels},
							Spec: ExternalNameServiceSpec{
								Type:         "ExternalName",
								ExternalName: rr.service + "." + rr.namespace + ".svc.cluster.local",
								Ports:        []ServicePort{{Name: "http", Port: rr.port, TargetPort: rr.port}},
							},
						})
					}
				}
				paths = append(paths, IngressPath{
					Path:     rr.prefix,
					PathType: "Prefix",
					Backend:  IngressBackend{Service: IngressServiceBackend{Name: svc, Port: IngressPort{Number: rr.port}}},
				})
			}
			if pos, merged := ingressIndex[key]; merged {
				for ri := range out.Ingresses[pos].Spec.Rules {
					out.Ingresses[pos].Spec.Rules[ri].HTTP.Paths = append(out.Ingresses[pos].Spec.Rules[ri].HTTP.Paths, paths...)
				}
				continue
			}
			ingressIndex[key] = len(out.Ingresses)
			obj := Ingress{
				APIVersion: "networking.k8s.io/v1",
				Kind:       "Ingress",
				Metadata:   ObjectMeta{Name: name, Namespace: in.LocalNS, Labels: labels},
				Spec:       IngressSpec{IngressClassName: ing.ClassName},
			}
			for _, h := range hosts {
				obj.Spec.Rules = append(obj.Spec.Rules, IngressRule{Host: h, HTTP: IngressHTTP{Paths: paths}})
			}
			if ing.ClusterIssuer != "" {
				obj.Metadata.Annotations = map[string]string{certManagerIssuerKey: ing.ClusterIssuer}
				obj.Spec.TLS = []IngressTLS{{Hosts: hosts, SecretName: name + "-tls"}}
			}
			out.Ingresses = append(out.Ingresses, obj)
		}
		return nil
	}

	if err := render(in.Owner, in.Routes, domain.DefaultAppRouteHostname, in.Owner); err != nil {
		return RouteObjects{}, err
	}
	for _, s := range in.Siblings {
		sib := s
		prev := in.BackendNS
		// A sibling's own-app backends live in the sibling's base-env namespace;
		// anything else it forwards to resolves through the caller's map.
		in.BackendNS = func(app string) (string, bool) {
			if app == sib.App {
				return sib.Namespace, true
			}
			return prev(app)
		}
		if err := render(sib.App, sib.Routes, domain.DefaultAppRouteHostname, in.Owner+"-"+sib.App); err != nil {
			return RouteObjects{}, err
		}
		in.BackendNS = prev
	}
	return out, nil
}

// routeObjectName is "{prefix}-{route}", clipped to a DNS label.
func routeObjectName(prefix, route string) string {
	n := prefix + "-" + route
	if len(n) > routeObjectNameLimit {
		n = strings.TrimRight(n[:routeObjectNameLimit], "-")
	}
	return n
}

// BuildReferenceGrants renders one ReferenceGrant per source namespace into
// localNS, allowing HTTPRoutes from that namespace to target Services here.
func BuildReferenceGrants(localNS string, from []string, labels map[string]string) []ReferenceGrant {
	seen := map[string]bool{}
	var srcs []string
	for _, ns := range from {
		if ns == "" || ns == localNS || seen[ns] {
			continue
		}
		seen[ns] = true
		srcs = append(srcs, ns)
	}
	sort.Strings(srcs)
	out := make([]ReferenceGrant, 0, len(srcs))
	for _, ns := range srcs {
		out = append(out, ReferenceGrant{
			APIVersion: refGrantAPIVersion,
			Kind:       "ReferenceGrant",
			Metadata:   ObjectMeta{Name: routeObjectName("allow-routes-from", ns), Namespace: localNS, Labels: labels},
			Spec: ReferenceGrantSpec{
				From: []ReferenceGrantFrom{{Group: gatewayAPIGroup, Kind: "HTTPRoute", Namespace: ns}},
				To:   []ReferenceGrantTo{{Group: "", Kind: "Service"}},
			},
		})
	}
	return out
}

// RoutePlatformFiles is what writePlatformDir writes for routing: nil slices
// prune whatever an earlier publish left.
type RoutePlatformFiles struct {
	Routes    []HTTPRoute
	Ingresses []Ingress
	Services  []ExternalNameService
	Grants    []ReferenceGrant
}

// writePlatformRoutes prunes then writes route-<name>.yaml and
// referencegrant-<ns>.yaml into a platform resources dir — prune-first like the
// per-component projections, so a removed route leaves no orphan.
func (p *Publisher) writePlatformRoutes(resDir string, files RoutePlatformFiles) error {
	for _, pat := range []string{routeFilePrefix + "*.yaml", refGrantFilePrefix + "*.yaml", ingressFilePrefix + "*.yaml", shimFilePrefix + "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join(resDir, pat))
		if err != nil {
			return fmt.Errorf("glob platform routes %s: %w", pat, err)
		}
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				return fmt.Errorf("prune platform route %s: %w", filepath.Base(m), err)
			}
		}
	}
	for _, r := range files.Routes {
		b, err := yaml.Marshal(r)
		if err != nil {
			return fmt.Errorf("marshal HTTPRoute %s: %w", r.Metadata.Name, err)
		}
		if err := p.writeFile(filepath.Join(resDir, routeFilePrefix+r.Metadata.Name+".yaml"), b); err != nil {
			return err
		}
	}
	for _, g := range files.Grants {
		b, err := yaml.Marshal(g)
		if err != nil {
			return fmt.Errorf("marshal ReferenceGrant %s: %w", g.Metadata.Name, err)
		}
		if err := p.writeFile(filepath.Join(resDir, refGrantFilePrefix+g.Metadata.Name+".yaml"), b); err != nil {
			return err
		}
	}
	for _, ing := range files.Ingresses {
		b, err := yaml.Marshal(ing)
		if err != nil {
			return fmt.Errorf("marshal Ingress %s: %w", ing.Metadata.Name, err)
		}
		if err := p.writeFile(filepath.Join(resDir, ingressFilePrefix+ing.Metadata.Name+".yaml"), b); err != nil {
			return err
		}
	}
	for _, svc := range files.Services {
		b, err := yaml.Marshal(svc)
		if err != nil {
			return fmt.Errorf("marshal ExternalName Service %s: %w", svc.Metadata.Name, err)
		}
		if err := p.writeFile(filepath.Join(resDir, shimFilePrefix+svc.Metadata.Name+".yaml"), b); err != nil {
			return err
		}
	}
	return nil
}

// routeLabels are the identity labels stamped on platform route objects. The
// instance label is also set (ArgoCD's label tracking overwrites it with the
// platform Application's name, but non-Argo consumers still get a hint).
func (p *Publisher) routeLabels(app *domain.App, env string) map[string]string {
	labels := p.cfg.Branding.ManagedByLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["app.kubernetes.io/instance"] = app.Name
	labels[labelApp] = app.Name
	labels[labelProject] = app.ProjectName
	labels[labelEnv] = env
	if app.Spec.Stack != "" {
		labels["suparship.io/stack"] = app.Spec.Stack
	}
	return labels
}

// routeTiers resolves each tier's rendering context from the org → env →
// cluster routing profiles: a Gateway parentRef when the profile's effective
// routeKind is httproute (explicit, or auto with a Gateway), else its
// IngressClass (Ingress). Tiers without a profile are absent from both.
func routeTiers(orgProfiles, envProfiles, clusterProfiles domain.RoutingProfiles) (map[domain.ExposeMode]domain.GatewayRef, map[domain.ExposeMode]IngressTierRef) {
	gws := map[domain.ExposeMode]domain.GatewayRef{}
	ings := map[domain.ExposeMode]IngressTierRef{}
	for _, tier := range []domain.ExposeMode{domain.ExposeExternal, domain.ExposeInternal} {
		prof, err := domain.ResolveRoutingProfile(orgProfiles, envProfiles, clusterProfiles, tier)
		if err != nil {
			continue
		}
		if prof.EffectiveRouteKind() == domain.RouteKindHTTPRoute && prof.HasGateway() {
			gws[tier] = *prof.Gateway
			continue
		}
		if prof.IngressClassName != "" {
			ings[tier] = IngressTierRef{ClassName: prof.IngressClassName, ClusterIssuer: prof.ClusterIssuer}
		}
	}
	return gws, ings
}

// backendNSFunc adapts the adapter-supplied namespace map.
func backendNSFunc(m map[string]string) func(string) (string, bool) {
	return func(app string) (string, bool) {
		ns, ok := m[app]
		return ns, ok && ns != ""
	}
}

// routeFilesForEnv renders an app's platform routes for one stable env.
func (p *Publisher) routeFilesForEnv(app *domain.App, env AppPublishEnv, namespace string) (RoutePlatformFiles, error) {
	in := env.Routes
	if len(in.Routes) == 0 && len(in.GrantFromNamespaces) == 0 {
		return RoutePlatformFiles{}, nil
	}
	orgName := p.cfg.OrgName
	if orgName == "" {
		orgName = "default"
	}
	target := activeTarget(env)
	ctx := p.platformVarsContext(withoutHostSwap(app), env, orgName)
	gws, ings := routeTiers(p.cfg.RoutingProfiles, env.RoutingProfiles, target.RoutingProfiles)
	objs, err := BuildRoutes(RouteRenderInput{
		Owner:        app.Name,
		Routes:       in.Routes,
		Env:          env.EnvName,
		Interpolate:  ctx.Interpolate,
		Gateways:     gws,
		IngressTiers: ings,
		LocalNS:      namespace,
		BackendNS:    backendNSFunc(in.BackendNamespaces),
		Labels:       p.routeLabels(app, env.EnvName),
	})
	if err != nil {
		return RoutePlatformFiles{}, err
	}
	return routeFiles(objs, namespace, in.GrantFromNamespaces, gws, p.routeLabels(app, env.EnvName)), nil
}

// routeFiles packs rendered objects plus the ReferenceGrants — only when the
// edge is Gateway API: an Ingress edge bridges namespaces with ExternalName
// shims, and a cluster without Gateway API has no ReferenceGrant CRD.
func routeFiles(objs RouteObjects, namespace string, grantFrom []string, gws map[domain.ExposeMode]domain.GatewayRef, labels map[string]string) RoutePlatformFiles {
	files := RoutePlatformFiles{Routes: objs.HTTPRoutes, Ingresses: objs.Ingresses, Services: objs.Services}
	if len(gws) > 0 {
		files.Grants = BuildReferenceGrants(namespace, grantFrom, labels)
	}
	return files
}

// routeFilesForPreview renders a preview's composite routes (own rules local,
// sibling rules cross-namespace) plus the backend-switch grant.
func (p *Publisher) routeFilesForPreview(app *domain.App, preview PreviewPublishSpec, namespace string) (RoutePlatformFiles, error) {
	in := preview.Routes
	if len(in.Routes) == 0 && len(in.Siblings) == 0 && len(in.GrantFromNamespaces) == 0 {
		return RoutePlatformFiles{}, nil
	}
	orgName := p.cfg.OrgName
	if orgName == "" {
		orgName = "default"
	}
	pv := helmvalues.MapPlatformValuesForEnv(withoutHostSwap(app), preview.PreviewName, domain.AppEnvPreview,
		preview.BaseDomain, namespace, "", orgName, p.cfg.RoutingProfiles, nil, nil)
	ctx := platform.Context{Platform: pv, Vars: preview.EnvVars}
	backendNS := backendNSFunc(in.BackendNamespaces)
	gws, ings := routeTiers(p.cfg.RoutingProfiles, nil, nil)
	objs, err := BuildRoutes(RouteRenderInput{
		Owner:        app.Name,
		Routes:       in.Routes,
		Env:          preview.PreviewName,
		Interpolate:  ctx.Interpolate,
		Gateways:     gws,
		IngressTiers: ings,
		LocalNS:      namespace,
		BackendNS: func(a string) (string, bool) {
			if a == app.Name {
				return namespace, true // the preview's own Services
			}
			return backendNS(a)
		},
		Siblings: in.Siblings,
		Labels:   p.routeLabels(app, preview.PreviewName),
	})
	if err != nil {
		return RoutePlatformFiles{}, err
	}
	return routeFiles(objs, namespace, in.GrantFromNamespaces, gws, p.routeLabels(app, preview.PreviewName)), nil
}

// withoutHostSwap returns a copy of the app with every RoutedToPreview
// cleared, so a platform-routed app's values and env vars never render the
// "-origin"/plain routing-name variants: for platform routes, route-to-preview
// is a backend switch, not a hostname change.
func withoutHostSwap(app *domain.App) *domain.App {
	cp := *app
	if len(app.Spec.EnvironmentDefaults) == 0 {
		return &cp
	}
	defs := make(map[string]domain.EnvironmentOverride, len(app.Spec.EnvironmentDefaults))
	for k, v := range app.Spec.EnvironmentDefaults {
		v.RoutedToPreview = ""
		defs[k] = v
	}
	cp.Spec.EnvironmentDefaults = defs
	return &cp
}
