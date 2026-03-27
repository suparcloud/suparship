package runtime

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

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

	var desired int32
	if dep.Spec.Replicas != nil {
		desired = *dep.Spec.Replicas
	}
	info.Replicas = desired
	info.Available = dep.Status.AvailableReplicas
	info.Status = DeploymentStatus(desired, dep.Status.ReadyReplicas, dep.Status.AvailableReplicas)

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

	ingList, err := p.client.NetworkingV1().Ingresses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsForbidden(err) {
		return nil, fmt.Errorf("listing ingresses in %s: %w", namespace, err)
	}
	if ingList != nil {
		for _, ing := range ingList.Items {
			if !ingressReferencesService(ing.Name, serviceName) {
				continue
			}
			for _, rule := range ing.Spec.Rules {
				if rule.Host != "" {
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
					info.IngressURLs = append(info.IngressURLs, scheme+"://"+rule.Host)
				}
			}
		}
	}

	return info, nil
}

// ingressReferencesService checks if an ingress name matches a service by
// exact name or common naming conventions (e.g. "{service}-ingress").
func ingressReferencesService(ingressName, serviceName string) bool {
	return ingressName == serviceName ||
		strings.HasPrefix(ingressName, serviceName+"-")
}
