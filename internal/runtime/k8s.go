package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// httpRouteGVR is the Gateway-API HTTPRoute resource, listed via the dynamic
// client (unstructured) so we don't depend on the gateway-api module. Discovery
// no-ops gracefully when the CRD is absent or access is forbidden.
var httpRouteGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}

// instanceLabel is the standard label ArgoCD (and Helm) stamp on every resource
// of a release, set to the ArgoCD Application name ({project}-{app}-{env}). It
// lets us discover an app's workloads without knowing the chart's own naming —
// essential for BYO/passthrough charts whose Deployment names follow their own
// fullname template, not the app name.
const instanceLabel = "app.kubernetes.io/instance"

// statusRank ranks phases by severity for worst-of aggregation across the
// Deployments of a multi-workload app: the app is only "healthy" when every
// workload is. Mirrors the server's per-cluster aggregation ranking.
var statusRank = map[string]int{
	StatusHealthy:     0,
	StatusProgressing: 1,
	StatusUnknown:     2,
	StatusNotDeployed: 3,
	StatusDegraded:    4,
}

// K8sProvider reads runtime state from Kubernetes workloads (Deployments,
// StatefulSets, DaemonSets), Ingresses, and Gateway-API HTTPRoutes.
type K8sProvider struct {
	client kubernetes.Interface
	// dyn lists Gateway-API HTTPRoutes (a CRD, not in the typed clientset).
	// Optional: nil disables HTTPRoute endpoint discovery (Ingress still works).
	dyn dynamic.Interface
	// secure reports whether generated HTTPRoute endpoint URLs use https.
	// Read live per call (the org setting is editable at runtime); nil means
	// true. Ingress URLs are unaffected — their scheme is derived from the
	// live spec.tls.
	secure func() bool
}

// NewK8sProvider creates a Kubernetes-backed runtime provider. dyn may be nil to
// disable Gateway-API HTTPRoute endpoint discovery.
func NewK8sProvider(client kubernetes.Interface, dyn dynamic.Interface) *K8sProvider {
	return &K8sProvider{client: client, dyn: dyn}
}

// SetSecureEndpointsFunc installs a live getter for the org's secure-endpoints
// flag, consulted on every HTTPRoute URL build. Nil (or never calling this)
// keeps the https default.
func (p *K8sProvider) SetSecureEndpointsFunc(f func() bool) { p.secure = f }

func (p *K8sProvider) secureEndpoints() bool { return p.secure == nil || p.secure() }

func (p *K8sProvider) GetServiceRuntime(ctx context.Context, namespace, serviceName string) (*RuntimeInfo, error) {
	info := &RuntimeInfo{
		Status:      StatusNotDeployed,
		IngressURLs: []string{},
		Namespace:   namespace,
	}

	// A workload named serviceName may be a Deployment, StatefulSet, or DaemonSet
	// (databases and caches typically ship as StatefulSets). Try each kind so the
	// name-based fallback isn't blind to non-Deployment apps.
	w, found, err := p.getNamedWorkload(ctx, namespace, serviceName)
	if err != nil {
		return nil, err
	}
	if found {
		info.Replicas = w.desired
		info.Available = w.available
		info.Image = w.image
		info.LastDeployed = w.lastDeployed
		info.Status = DeploymentStatus(w.desired, w.ready, w.available)
	}

	ingList, err := p.client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("listing ingresses in %s: %w", namespace, err)
	}
	if ingList != nil {
		for i := range ingList.Items {
			if ingressReferencesService(ingList.Items[i].Name, serviceName) {
				info.IngressURLs = append(info.IngressURLs, ingressHostURLs(&ingList.Items[i], p.secureEndpoints())...)
			}
		}
	}

	// Gateway-API HTTPRoutes in this namespace whose backendRef targets the
	// service contribute their host URLs too (e.g. a co-located "routes" app).
	for _, rt := range p.listHTTPRoutes(ctx, namespace, "") {
		if httpRouteReferencesService(rt, serviceName) {
			info.IngressURLs = append(info.IngressURLs, httpRouteHostURLs(rt, p.secureEndpoints())...)
		}
	}
	info.IngressURLs = dedupeStrings(info.IngressURLs)

	return info, nil
}

// GetAppRuntime aggregates runtime state across every workload ArgoCD tracks for
// one app-env (selected by instanceLabel == instance) in namespace — Deployments,
// StatefulSets, and DaemonSets alike, so an app that ships as a StatefulSet (e.g.
// valkey/redis/postgres) reports real health instead of a perpetual 0/0 "not
// deployed". This is the app-native query: it works regardless of the chart's
// workload naming, so BYO/passthrough charts (whose workloads are named by their
// own fullname template, not the app name) report real replica counts.
//
// Aggregation is worst-of phase + summed replicas + union of ingress/HTTPRoute
// URLs. A resource-only app that ships no workloads — e.g. an httproute/ingress
// app — but owns labelled routing resources is reported Healthy (it's deployed;
// there are simply no pods to count). When nothing is found by label at all it
// falls back to the name-based GetServiceRuntime(namespace, fallbackName) so
// apps published before instance labels keep working.
func (p *K8sProvider) GetAppRuntime(ctx context.Context, namespace, instance, fallbackName string) (*RuntimeInfo, error) {
	selector := instanceLabel + "=" + instance

	// Workload and routing discovery are independent reads; run them concurrently
	// (and each fans out its own LISTs in parallel) so one app-env's live status
	// is ~1 wall-clock round-trip instead of 5 serial ones. This dominates page
	// latency on a cold cache, multiplied by every app×env×cluster.
	var (
		workloads  []workload
		routeURLs  []string
		hasRouting bool
		wErr, rErr error
		wg         sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		workloads, wErr = p.listLabelledWorkloads(ctx, namespace, selector)
	}()
	go func() {
		defer wg.Done()
		routeURLs, hasRouting, rErr = p.labelledRoutes(ctx, namespace, selector)
	}()
	wg.Wait()
	if wErr != nil {
		return nil, wErr
	}
	if rErr != nil {
		return nil, rErr
	}

	if len(workloads) == 0 {
		if hasRouting {
			// Resource-only app: deployed, no pods. Healthy with no replicas.
			return &RuntimeInfo{Status: StatusHealthy, IngressURLs: routeURLs, Namespace: namespace}, nil
		}
		// Nothing labelled — name-based fallback (pre-label / single-workload apps).
		return p.GetServiceRuntime(ctx, namespace, fallbackName)
	}

	// Worst-of phase across the running workloads, ignoring any that are scaled
	// to zero (KEDA idle / manually stopped). A workload we listed exists, so
	// desired==0 means "deployed but idle", not "not deployed" — counting it in
	// the worst-of would drag a partly-idle app to not_deployed. If EVERY
	// workload is idle the app reports StatusIdle.
	info := &RuntimeInfo{Status: StatusHealthy, IngressURLs: []string{}, Namespace: namespace}
	worst := StatusHealthy
	allIdle := true
	for _, w := range workloads {
		info.Replicas += w.desired
		info.Available += w.available
		if info.Image == "" {
			info.Image = w.image
		}
		if w.lastDeployed > info.LastDeployed {
			info.LastDeployed = w.lastDeployed
		}
		if w.desired == 0 {
			continue // scaled to zero — idle, doesn't affect health
		}
		allIdle = false
		phase := DeploymentStatus(w.desired, w.ready, w.available)
		if statusRank[phase] >= statusRank[worst] {
			worst = phase
		}
	}
	if allIdle {
		info.Status = StatusIdle
	} else {
		info.Status = worst
	}

	info.IngressURLs = dedupeStrings(append(info.IngressURLs, routeURLs...))
	return info, nil
}

// labelledRoutes returns the endpoint URLs from Ingresses and Gateway-API
// HTTPRoutes carrying the given label selector, plus whether any such routing
// resource exists (used to recognize a workload-less app as deployed). HTTPRoute
// discovery degrades gracefully (see listHTTPRoutes).
func (p *K8sProvider) labelledRoutes(ctx context.Context, namespace, selector string) (urls []string, found bool, err error) {
	// Ingresses (typed) and HTTPRoutes (dynamic) are independent; list them
	// concurrently and combine in a fixed order (ingress URLs first) so output
	// stays deterministic.
	var (
		ingURLs, rtURLs   []string
		ingFound, rtFound bool
		ingErr            error
		wg                sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		ingList, ierr := p.client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if ierr != nil && !apierrors.IsNotFound(ierr) && !apierrors.IsForbidden(ierr) {
			ingErr = fmt.Errorf("listing ingresses in %s: %w", namespace, ierr)
			return
		}
		if ingList != nil {
			for i := range ingList.Items {
				ingFound = true
				ingURLs = append(ingURLs, ingressHostURLs(&ingList.Items[i], p.secureEndpoints())...)
			}
		}
	}()
	go func() {
		defer wg.Done()
		if routes := p.listHTTPRoutes(ctx, namespace, selector); len(routes) > 0 {
			rtFound = true
			for _, rt := range routes {
				rtURLs = append(rtURLs, httpRouteHostURLs(rt, p.secureEndpoints())...)
			}
		}
	}()
	wg.Wait()
	if ingErr != nil {
		return nil, false, ingErr
	}
	return dedupeStrings(append(ingURLs, rtURLs...)), ingFound || rtFound, nil
}

// workload is the subset of a Deployment/StatefulSet/DaemonSet runtime state the
// status derivation needs, normalized across kinds so one aggregation loop in
// GetAppRuntime (and one mapping in GetServiceRuntime) handles all three. It does
// NOT carry a phase: callers derive that via DeploymentStatus, since aggregation
// needs the per-workload phase before combining.
type workload struct {
	desired      int32
	ready        int32
	available    int32
	image        string
	lastDeployed string
}

// listLabelledWorkloads returns every Deployment, StatefulSet, and DaemonSet in
// namespace carrying the given label selector, normalized to workload. A
// per-kind Forbidden error is tolerated (RBAC may scope suparship to a subset of
// kinds) so a readable kind still reports; any other list error is returned.
func (p *K8sProvider) listLabelledWorkloads(ctx context.Context, namespace, selector string) ([]workload, error) {
	opts := metav1.ListOptions{LabelSelector: selector}

	// The three kinds are separate typed endpoints; list them concurrently and
	// merge in a fixed kind order afterwards so the result is deterministic. A
	// per-kind Forbidden error is tolerated (RBAC may scope us to a subset).
	var (
		perKind [3][]workload
		errs    [3]error
		wg      sync.WaitGroup
	)
	list := []func() ([]workload, error){
		func() ([]workload, error) {
			l, err := p.client.AppsV1().Deployments(namespace).List(ctx, opts)
			if err != nil {
				return nil, err
			}
			out := make([]workload, 0, len(l.Items))
			for i := range l.Items {
				out = append(out, deploymentWorkload(&l.Items[i]))
			}
			return out, nil
		},
		func() ([]workload, error) {
			l, err := p.client.AppsV1().StatefulSets(namespace).List(ctx, opts)
			if err != nil {
				return nil, err
			}
			out := make([]workload, 0, len(l.Items))
			for i := range l.Items {
				out = append(out, statefulSetWorkload(&l.Items[i]))
			}
			return out, nil
		},
		func() ([]workload, error) {
			l, err := p.client.AppsV1().DaemonSets(namespace).List(ctx, opts)
			if err != nil {
				return nil, err
			}
			out := make([]workload, 0, len(l.Items))
			for i := range l.Items {
				out = append(out, daemonSetWorkload(&l.Items[i]))
			}
			return out, nil
		},
	}
	wg.Add(len(list))
	for i, fn := range list {
		go func(i int, fn func() ([]workload, error)) {
			defer wg.Done()
			perKind[i], errs[i] = fn()
		}(i, fn)
	}
	wg.Wait()

	kinds := []string{"deployments", "statefulsets", "daemonsets"}
	var out []workload
	for i, err := range errs {
		if err != nil && !apierrors.IsForbidden(err) {
			return nil, fmt.Errorf("listing %s in %s: %w", kinds[i], namespace, err)
		}
		out = append(out, perKind[i]...)
	}
	return out, nil
}

// getNamedWorkload looks up a single workload by exact name, trying Deployment,
// then StatefulSet, then DaemonSet. found is false when no kind has that name.
// A non-NotFound error (RBAC, API down) is returned so the caller doesn't report
// a broken cluster as "not deployed".
func (p *K8sProvider) getNamedWorkload(ctx context.Context, namespace, name string) (workload, bool, error) {
	dep, err := p.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return deploymentWorkload(dep), true, nil
	}
	if !apierrors.IsNotFound(err) {
		return workload{}, false, fmt.Errorf("reading deployment %s/%s: %w", namespace, name, err)
	}

	sts, err := p.client.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return statefulSetWorkload(sts), true, nil
	}
	if !apierrors.IsNotFound(err) {
		return workload{}, false, fmt.Errorf("reading statefulset %s/%s: %w", namespace, name, err)
	}

	ds, err := p.client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return daemonSetWorkload(ds), true, nil
	}
	if !apierrors.IsNotFound(err) {
		return workload{}, false, fmt.Errorf("reading daemonset %s/%s: %w", namespace, name, err)
	}

	return workload{}, false, nil
}

func deploymentWorkload(dep *appsv1.Deployment) workload {
	w := workload{
		ready:        dep.Status.ReadyReplicas,
		available:    dep.Status.AvailableReplicas,
		image:        podImage(dep.Spec.Template),
		lastDeployed: formatTime(dep.CreationTimestamp),
	}
	if dep.Spec.Replicas != nil {
		w.desired = *dep.Spec.Replicas
	}
	// A Deployment's last roll-out is more accurate than its creation time.
	for _, cond := range dep.Status.Conditions {
		if cond.Type == appsv1.DeploymentProgressing && cond.LastUpdateTime.After(dep.CreationTimestamp.Time) {
			w.lastDeployed = formatTime(cond.LastUpdateTime)
			break
		}
	}
	return w
}

func statefulSetWorkload(sts *appsv1.StatefulSet) workload {
	w := workload{
		ready:        sts.Status.ReadyReplicas,
		available:    sts.Status.AvailableReplicas,
		image:        podImage(sts.Spec.Template),
		lastDeployed: formatTime(sts.CreationTimestamp),
	}
	if sts.Spec.Replicas != nil {
		w.desired = *sts.Spec.Replicas
	}
	return w
}

func daemonSetWorkload(ds *appsv1.DaemonSet) workload {
	// A DaemonSet has no spec.replicas: its desired count is one pod per matching
	// node, which the scheduler reports as DesiredNumberScheduled.
	return workload{
		desired:      ds.Status.DesiredNumberScheduled,
		ready:        ds.Status.NumberReady,
		available:    ds.Status.NumberAvailable,
		image:        podImage(ds.Spec.Template),
		lastDeployed: formatTime(ds.CreationTimestamp),
	}
}

// podImage returns the first container image of a pod template, or "".
func podImage(tmpl corev1.PodTemplateSpec) string {
	if len(tmpl.Spec.Containers) > 0 {
		return tmpl.Spec.Containers[0].Image
	}
	return ""
}

// formatTime renders a Kubernetes timestamp as RFC 3339 UTC, or "" if zero.
func formatTime(t metav1.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// ingressHostURLs returns scheme://host for each rule host of an Ingress.
// With TLS blocks present, a host is https iff some block covers it. With no
// spec.tls at all the Ingress itself is scheme-blind — TLS may still terminate
// upstream (LB, CDN) or nowhere (dev) — so secure (the org's secure-endpoints
// setting) decides.
func ingressHostURLs(ing *networkingv1.Ingress, secure bool) []string {
	noTLSScheme := "http"
	if secure {
		noTLSScheme = "https"
	}
	var urls []string
	for _, rule := range ing.Spec.Rules {
		if rule.Host == "" {
			continue
		}
		scheme := noTLSScheme
		if len(ing.Spec.TLS) > 0 {
			scheme = "http"
			for _, tls := range ing.Spec.TLS {
				for _, h := range tls.Hosts {
					if h == rule.Host {
						scheme = "https"
						break
					}
				}
			}
		}
		urls = append(urls, scheme+"://"+rule.Host)
	}
	return urls
}

// ingressReferencesService checks if an ingress name matches a service by
// exact name or common naming conventions (e.g. "{service}-ingress").
func ingressReferencesService(ingressName, serviceName string) bool {
	return ingressName == serviceName ||
		strings.HasPrefix(ingressName, serviceName+"-")
}

// listHTTPRoutes lists Gateway-API HTTPRoutes in namespace (optionally filtered
// by labelSelector). Returns nil — never an error — when the dynamic client is
// absent, the CRD isn't installed (NotFound), or access is forbidden, so
// endpoint discovery degrades gracefully like the Ingress lookups.
func (p *K8sProvider) listHTTPRoutes(ctx context.Context, namespace, labelSelector string) []unstructured.Unstructured {
	if p.dyn == nil {
		return nil
	}
	list, err := p.dyn.Resource(httpRouteGVR).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil // no CRD / forbidden / transient — best-effort
	}
	return list.Items
}

// httpRouteHostURLs returns the endpoint URLs of an HTTPRoute: every spec.hostname
// crossed with each rule's path match (default "/"). The HTTPRoute itself
// carries no scheme (that's a property of the parent Gateway's listeners), so
// secure — the org's secure-endpoints setting — picks https vs http.
func httpRouteHostURLs(rt unstructured.Unstructured, secure bool) []string {
	scheme := "http://"
	if secure {
		scheme = "https://"
	}
	hosts, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "hostnames")
	if len(hosts) == 0 {
		return nil
	}
	rules, _, _ := unstructured.NestedSlice(rt.Object, "spec", "rules")
	paths := httpRoutePaths(rules)
	var urls []string
	for _, h := range hosts {
		if h == "" {
			continue
		}
		for _, path := range paths {
			if path == "/" {
				urls = append(urls, scheme+h)
			} else {
				urls = append(urls, scheme+h+path)
			}
		}
	}
	return urls
}

// httpRoutePaths extracts the distinct path-match values across an HTTPRoute's
// rules. A rule with no path match (or no rules at all) implies "/".
func httpRoutePaths(rules []any) []string {
	seen := map[string]bool{}
	var paths []string
	add := func(p string) {
		if p == "" {
			p = "/"
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	if len(rules) == 0 {
		add("/")
		return paths
	}
	for _, r := range rules {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		matches, _, _ := unstructured.NestedSlice(rm, "matches")
		if len(matches) == 0 {
			add("/")
			continue
		}
		for _, m := range matches {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			val, _, _ := unstructured.NestedString(mm, "path", "value")
			add(val)
		}
	}
	if len(paths) == 0 {
		add("/")
	}
	return paths
}

// httpRouteReferencesService reports whether any rule's backendRef targets a
// Service named serviceName (same-namespace attribution; Gateway-API defaults a
// backendRef's kind to Service when unset).
func httpRouteReferencesService(rt unstructured.Unstructured, serviceName string) bool {
	rules, _, _ := unstructured.NestedSlice(rt.Object, "spec", "rules")
	for _, r := range rules {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		refs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
		for _, ref := range refs {
			refm, ok := ref.(map[string]any)
			if !ok {
				continue
			}
			kind, _, _ := unstructured.NestedString(refm, "kind")
			if kind != "" && kind != "Service" {
				continue
			}
			if name, _, _ := unstructured.NestedString(refm, "name"); name == serviceName {
				return true
			}
		}
	}
	return false
}

// dedupeStrings returns s with duplicates removed, preserving first-seen order.
func dedupeStrings(s []string) []string {
	if len(s) == 0 {
		return s
	}
	seen := make(map[string]bool, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
