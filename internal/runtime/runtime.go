// Package runtime reads live Kubernetes state for suparship workloads (apps
// and their components).
//
// The namespace convention is {project}-{environment}, e.g. "myapi-staging".
// A Provider returns runtime information for a workload in a given namespace,
// including replica status, container image, and ingress URLs. If the workload
// has not been deployed, it returns a RuntimeInfo with Status set to
// StatusNotDeployed.
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

// RuntimeInfo describes the live state of a single workload (service or app
// component) in a given namespace.
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
//
// GetServiceRuntime is named for the legacy service model. It is reused by
// the app-oriented preview and inventory code paths because the cluster query
// is identical — a Deployment named after the workload in a given namespace.
// The method signature is stable; "service" in the parameter name should be
// read as "workload name" during the migration period.
//
// Deprecated (GetServiceRuntime): once app-native runtime queries are
// implemented, this interface method will be superseded by one that uses
// app/component coordinates. Callers outside internal/server/inventory.go
// and internal/server/previews.go should prefer the app-scoped APIs.
// See docs/migration-app-model.md for the transition guide.
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
