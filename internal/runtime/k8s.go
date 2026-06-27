package runtime

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

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
// StatefulSets, DaemonSets) and Ingresses.
type K8sProvider struct {
	client kubernetes.Interface
}

// NewK8sProvider creates a Kubernetes-backed runtime provider.
func NewK8sProvider(client kubernetes.Interface) *K8sProvider {
	return &K8sProvider{client: client}
}

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
				info.IngressURLs = append(info.IngressURLs, ingressHostURLs(&ingList.Items[i])...)
			}
		}
	}

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
// Aggregation is worst-of phase + summed replicas + union of ingress URLs.
// When no labelled workloads are found — label tracking disabled, a non-ArgoCD
// install, or a canonical app predating instance labels — it falls back to the
// name-based GetServiceRuntime(namespace, fallbackName) so existing single-
// workload apps keep working.
func (p *K8sProvider) GetAppRuntime(ctx context.Context, namespace, instance, fallbackName string) (*RuntimeInfo, error) {
	selector := instanceLabel + "=" + instance
	workloads, err := p.listLabelledWorkloads(ctx, namespace, selector)
	if err != nil {
		return nil, err
	}
	if len(workloads) == 0 {
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

	ingList, err := p.client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("listing ingresses in %s: %w", namespace, err)
	}
	if ingList != nil {
		for i := range ingList.Items {
			info.IngressURLs = append(info.IngressURLs, ingressHostURLs(&ingList.Items[i])...)
		}
	}
	return info, nil
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
	var out []workload

	deps, err := p.client.AppsV1().Deployments(namespace).List(ctx, opts)
	if err != nil && !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("listing deployments in %s: %w", namespace, err)
	}
	if deps != nil {
		for i := range deps.Items {
			out = append(out, deploymentWorkload(&deps.Items[i]))
		}
	}

	stss, err := p.client.AppsV1().StatefulSets(namespace).List(ctx, opts)
	if err != nil && !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("listing statefulsets in %s: %w", namespace, err)
	}
	if stss != nil {
		for i := range stss.Items {
			out = append(out, statefulSetWorkload(&stss.Items[i]))
		}
	}

	dss, err := p.client.AppsV1().DaemonSets(namespace).List(ctx, opts)
	if err != nil && !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("listing daemonsets in %s: %w", namespace, err)
	}
	if dss != nil {
		for i := range dss.Items {
			out = append(out, daemonSetWorkload(&dss.Items[i]))
		}
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

// ingressHostURLs returns scheme://host for each rule host of an Ingress. A host
// is https unless some TLS block omits it (matching the prior per-service logic).
func ingressHostURLs(ing *networkingv1.Ingress) []string {
	var urls []string
	for _, rule := range ing.Spec.Rules {
		if rule.Host == "" {
			continue
		}
		scheme := "https"
		for _, tls := range ing.Spec.TLS {
			found := false
			for _, h := range tls.Hosts {
				if h == rule.Host {
					found = true
					break
				}
			}
			if !found {
				scheme = "http"
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
