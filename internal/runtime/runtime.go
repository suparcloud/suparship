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
	// StatusIdle is a deployed workload intentionally scaled to zero replicas
	// (e.g. KEDA scale-to-zero off-hours). It exists in the cluster — so it is
	// NOT "not deployed" — but has nothing running, so it must not be reported
	// as "healthy" either, nor drag a multi-workload app to "not deployed".
	StatusIdle = "idle"
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

// DeploymentStatus derives a status string from replica counts alone. Counts
// cannot tell a rolling update (new pod not ready yet, old one still serving)
// from a broken workload, so partial availability is PROGRESSING, never
// degraded — the same call ArgoCD makes. Degraded needs the workload's
// conditions: see WorkloadStatus.
func DeploymentStatus(desired, ready, available int32) string {
	return WorkloadStatus(WorkloadHealth{Desired: desired, Ready: ready, Available: available, Updated: desired, Total: desired})
}

// WorkloadHealth is the rollout-aware view of a Deployment / StatefulSet /
// DaemonSet the status derivation needs. Zero values are safe: a caller with
// only counts gets the count-based answer.
type WorkloadHealth struct {
	Desired   int32
	Ready     int32
	Available int32
	// Updated is the number of pods on the current template revision
	// (Deployment updatedReplicas, StatefulSet updatedReplicas, DaemonSet
	// updatedNumberScheduled). Total is every pod the controller owns,
	// including old-revision ones still terminating (Deployment/StatefulSet
	// status.replicas, DaemonSet currentNumberScheduled).
	Updated int32
	Total   int32
	// GenerationLag is true while status.observedGeneration trails
	// metadata.generation — the controller has not yet acted on the spec.
	GenerationLag bool
	// DegradedReason is set when the controller itself gave up: a Deployment's
	// Progressing=False/ProgressDeadlineExceeded or ReplicaFailure=True
	// condition. That, not partial availability, is what "degraded" means.
	DegradedReason string
}

// WorkloadStatus mirrors ArgoCD's built-in health rules for the three workload
// kinds so suparship and ArgoCD agree about the same object:
//
//   - desired 0                              → not deployed (idle)
//   - controller gave up (deadline/failure)  → degraded
//   - spec not yet observed                  → progressing
//   - rollout in flight (updated < desired,
//     old pods lingering, not all available)  → progressing
//   - everything ready and available         → healthy
//
// Partial availability on its own is a rollout in flight, not a failure:
// Kubernetes surfaces a stuck rollout as ProgressDeadlineExceeded after
// progressDeadlineSeconds, and that is when this turns degraded.
func WorkloadStatus(h WorkloadHealth) string {
	if h.Desired == 0 {
		return StatusNotDeployed
	}
	if h.DegradedReason != "" {
		return StatusDegraded
	}
	if h.GenerationLag {
		return StatusProgressing
	}
	// Rollout counters are only meaningful once the controller has scaled the
	// current revision (Updated > 0). Before that — a counts-only caller, or a
	// rollout the controller has not started acting on, which GenerationLag
	// already covers — fall through to the availability checks.
	if h.Updated > 0 {
		if h.Updated < h.Desired || h.Total > h.Updated {
			return StatusProgressing
		}
	}
	if h.Available < h.Desired || h.Ready < h.Desired {
		return StatusProgressing
	}
	return StatusHealthy
}
