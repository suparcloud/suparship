// Package runtime reads live Kubernetes state for suparship services.
//
// The namespace convention is {project}-{environment}, e.g. "myapi-dev".
// A Provider returns runtime information for a service in a given namespace,
// including replica status, container image, and ingress URLs. If the
// service has not been deployed, it returns a RuntimeInfo with Status
// set to StatusNotDeployed.
package runtime

import "context"

// Service status values.
const (
	StatusHealthy     = "healthy"
	StatusDegraded    = "degraded"
	StatusProgressing = "progressing"
	StatusNotDeployed = "not_deployed"
	StatusUnknown     = "unknown"
)

// RuntimeInfo describes the live state of a single service.
type RuntimeInfo struct {
	Status       string   `json:"status"`
	Image        string   `json:"image,omitempty"`
	Replicas     int32    `json:"replicas"`
	Available    int32    `json:"available"`
	IngressURLs  []string `json:"ingressUrls"`
	Namespace    string   `json:"namespace"`
	LastDeployed string   `json:"lastDeployed,omitempty"`
}

// Provider reads runtime state from the cluster.
type Provider interface {
	GetServiceRuntime(ctx context.Context, namespace, serviceName string) (*RuntimeInfo, error)
}

// Namespace returns the conventional namespace for a project environment.
func Namespace(project, environment string) string {
	return project + "-" + environment
}

// DeploymentStatus derives a status string from replica counts.
func DeploymentStatus(desired, ready, available int32) string {
	if desired == 0 {
		return StatusNotDeployed
	}
	if available == desired && ready == desired {
		return StatusHealthy
	}
	if available > 0 {
		return StatusDegraded
	}
	return StatusProgressing
}
