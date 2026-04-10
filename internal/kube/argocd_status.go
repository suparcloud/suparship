package kube

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/domain"
)

// argoCDAppGVR is the GroupVersionResource for ArgoCD Application CRs.
var argoCDAppGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// ArgoCDStatusReader reads sync and health status from ArgoCD Application CRs
// on the tooling cluster. It does not need direct access to workload clusters.
//
// The Application CR name convention is "{appName}-{envName}" (as generated
// by BuildArgoAppSet). Use Get to retrieve the status for a specific app/env.
type ArgoCDStatusReader struct {
	dynamic   dynamic.Interface
	namespace string // ArgoCD namespace, e.g. "argocd"
}

// NewArgoCDStatusReader returns an ArgoCDStatusReader using the tooling cluster
// client. It discovers Application CRs in argoCDNamespace (default "argocd").
// Deprecated: prefer NewArgoCDStatusReaderFromDynamic with a pre-built dynamic client.
func NewArgoCDStatusReader(_ kubernetes.Interface, argoCDNamespace string) (*ArgoCDStatusReader, error) {
	// Cannot extract rest.Config from kubernetes.Interface without access to the
	// underlying concrete type or the original rest.Config. Callers should use
	// NewArgoCDStatusReaderFromDynamic and build a dynamic.Interface themselves.
	return nil, fmt.Errorf("NewArgoCDStatusReader: use NewArgoCDStatusReaderFromDynamic instead")
}

// GetAppStatus reads the ArgoCD Application status for one app/env combination.
// The Application name is derived as "{appName}-{envName}" (Model B convention).
// Returns domain.StatusUnknown when the Application CR is not found.
func (r *ArgoCDStatusReader) GetAppStatus(ctx context.Context, appName, envName string) (*domain.AppRuntimeStatus, error) {
	appCRName := appName + "-" + envName
	raw, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).Get(ctx, appCRName, metav1.GetOptions{})
	if err != nil {
		return &domain.AppRuntimeStatus{Phase: domain.StatusUnknown}, nil //nolint:nilerr // not-found is expected before first deploy
	}

	statusRaw, ok := raw.Object["status"].(map[string]any)
	if !ok {
		return &domain.AppRuntimeStatus{Phase: domain.StatusUnknown}, nil
	}

	return parseArgoCDStatus(statusRaw), nil
}

// ListProjectAppStatuses returns the runtime status of all Applications for a
// given ArgoCD project. The map key is the Application name (e.g. "hello-staging").
func (r *ArgoCDStatusReader) ListProjectAppStatuses(ctx context.Context, projectName string) (map[string]*domain.AppRuntimeStatus, error) {
	list, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "suparship.io/project=" + projectName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing argocd apps for project %q: %w", projectName, err)
	}

	result := make(map[string]*domain.AppRuntimeStatus, len(list.Items))
	for _, item := range list.Items {
		name, _, _ := unstructuredString(item.Object, "metadata", "name")
		statusRaw, _ := item.Object["status"].(map[string]any)
		result[name] = parseArgoCDStatus(statusRaw)
	}
	return result, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// parseArgoCDStatus maps ArgoCD sync/health status to domain.AppRuntimeStatus.
//
// ArgoCD status fields:
//
//	status.sync.status   — "Synced" | "OutOfSync" | "Unknown"
//	status.health.status — "Healthy" | "Degraded" | "Progressing" | "Suspended" | "Unknown" | "Missing"
func parseArgoCDStatus(status map[string]any) *domain.AppRuntimeStatus {
	if status == nil {
		return &domain.AppRuntimeStatus{Phase: domain.StatusUnknown}
	}

	syncStatus, _, _ := unstructuredString(status, "sync", "status")
	healthStatus, _, _ := unstructuredString(status, "health", "status")

	phase := argoCDPhase(syncStatus, healthStatus)

	// Extract replica counts from summary.resources if available.
	var replicas, available int32
	if summary, ok := status["summary"].(map[string]any); ok {
		replicas = int32(jsonNum(summary["images"]))
	}
	_ = replicas

	// Replica counts come from workload resources inside the Application;
	// ArgoCD does not expose them directly in the Application status in all versions.
	// For now we return phase only; a future iteration can walk status.resources.
	_ = available

	return &domain.AppRuntimeStatus{Phase: phase}
}

// argoCDPhase maps ArgoCD sync + health status strings to a domain.Status* constant.
func argoCDPhase(sync, health string) string {
	switch health {
	case "Healthy":
		if sync == "Synced" {
			return domain.StatusHealthy
		}
		return domain.StatusProgressing
	case "Degraded":
		return domain.StatusDegraded
	case "Progressing":
		return domain.StatusProgressing
	case "Missing":
		return domain.StatusNotDeployed
	default:
		return domain.StatusUnknown
	}
}


// NewArgoCDStatusReaderFromDynamic creates an ArgoCDStatusReader directly
// from a pre-built dynamic.Interface. This is the preferred constructor when
// the caller already has a rest.Config or a dynamic client.
func NewArgoCDStatusReaderFromDynamic(dyn dynamic.Interface, argoCDNamespace string) *ArgoCDStatusReader {
	if argoCDNamespace == "" {
		argoCDNamespace = argoCDNS
	}
	return &ArgoCDStatusReader{dynamic: dyn, namespace: argoCDNamespace}
}

// unstructuredString traverses a nested map path and returns the string value.
func unstructuredString(obj map[string]any, keys ...string) (string, bool, error) {
	cur := obj
	for i, k := range keys {
		if i == len(keys)-1 {
			v, ok := cur[k]
			if !ok {
				return "", false, nil
			}
			s, ok := v.(string)
			return s, ok, nil
		}
		next, ok := cur[k].(map[string]any)
		if !ok {
			return "", false, nil
		}
		cur = next
	}
	return "", false, nil
}

// jsonNum converts a json.Number or float64 value to int32.
func jsonNum(v any) int32 {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int32(i)
	case float64:
		return int32(n)
	case int:
		return int32(n)
	case int64:
		return int32(n)
	}
	return 0
}
