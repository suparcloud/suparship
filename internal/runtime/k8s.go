package runtime

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
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

// K8sProvider reads runtime state from Kubernetes Deployments and Ingresses.
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

	dep, err := p.client.AppsV1().Deployments(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return info, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading deployment %s/%s: %w", namespace, serviceName, err)
	}
	applyDeployment(info, dep)
	info.Status = DeploymentStatus(info.Replicas, dep.Status.ReadyReplicas, info.Available)

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

// GetAppRuntime aggregates runtime state across every Deployment ArgoCD tracks
// for one app-env (selected by instanceLabel == instance) in namespace. This is
// the app-native query: it works regardless of the chart's Deployment naming,
// so BYO/passthrough charts (whose Deployments are named by their own fullname
// template, not the app name) report real replica counts and health instead of
// a perpetual 0/0 "not deployed".
//
// Aggregation is worst-of phase + summed replicas + union of ingress URLs.
// When no labelled workloads are found — label tracking disabled, a non-ArgoCD
// install, or a canonical app predating instance labels — it falls back to the
// name-based GetServiceRuntime(namespace, fallbackName) so existing single-
// Deployment apps keep working.
func (p *K8sProvider) GetAppRuntime(ctx context.Context, namespace, instance, fallbackName string) (*RuntimeInfo, error) {
	selector := instanceLabel + "=" + instance
	deps, err := p.client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil && !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("listing deployments in %s: %w", namespace, err)
	}
	if deps == nil || len(deps.Items) == 0 {
		return p.GetServiceRuntime(ctx, namespace, fallbackName)
	}

	// Worst-of phase across the running workloads, ignoring any that are scaled
	// to zero (KEDA idle / manually stopped). A Deployment we listed exists, so
	// desired==0 means "deployed but idle", not "not deployed" — counting it in
	// the worst-of would drag a partly-idle app to not_deployed. If EVERY
	// workload is idle the app reports StatusIdle.
	info := &RuntimeInfo{Status: StatusHealthy, IngressURLs: []string{}, Namespace: namespace}
	worst := StatusHealthy
	allIdle := true
	for i := range deps.Items {
		dep := &deps.Items[i]
		var sub RuntimeInfo
		applyDeployment(&sub, dep)
		info.Replicas += sub.Replicas
		info.Available += sub.Available
		if info.Image == "" {
			info.Image = sub.Image
		}
		if sub.LastDeployed > info.LastDeployed {
			info.LastDeployed = sub.LastDeployed
		}
		if sub.Replicas == 0 {
			continue // scaled to zero — idle, doesn't affect health
		}
		allIdle = false
		phase := DeploymentStatus(sub.Replicas, dep.Status.ReadyReplicas, sub.Available)
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

// applyDeployment fills the replica/image/lastDeployed fields of info from a
// Deployment. It does NOT set Status (callers derive that, since aggregation
// needs the per-workload phase before combining).
func applyDeployment(info *RuntimeInfo, dep *appsv1.Deployment) {
	var desired int32
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	info.Replicas = desired
	info.Available = dep.Status.AvailableReplicas
	if len(dep.Spec.Template.Spec.Containers) > 0 {
		info.Image = dep.Spec.Template.Spec.Containers[0].Image
	}
	if ct := dep.CreationTimestamp; !ct.IsZero() {
		info.LastDeployed = ct.UTC().Format("2006-01-02T15:04:05Z")
	}
	for _, cond := range dep.Status.Conditions {
		if cond.Type == "Progressing" && cond.LastUpdateTime.After(dep.CreationTimestamp.Time) {
			info.LastDeployed = cond.LastUpdateTime.UTC().Format("2006-01-02T15:04:05Z")
			break
		}
	}
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
