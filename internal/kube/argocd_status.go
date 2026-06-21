package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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

// CountProjectApplications returns how many ArgoCD Application CRs in the
// ArgoCD namespace reference projectName via spec.project. Used by the
// two-phase project unpublish: the AppProject may only be removed once the
// ApplicationSets have finished pruning the project's generated Applications,
// because cascade deletion of an Application needs its AppProject to exist.
// Matches on spec.project (not labels) so legacy/unlabeled Applications count.
func (r *ArgoCDStatusReader) CountProjectApplications(ctx context.Context, projectName string) (int, error) {
	list, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("listing argocd apps: %w", err)
	}
	count := 0
	for _, item := range list.Items {
		proj, _, _ := unstructuredString(item.Object, "spec", "project")
		if proj == projectName {
			count++
		}
	}
	return count, nil
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

// DeploymentHistoryEntry represents a single ArgoCD sync event from
// status.history on an Application CR.
type DeploymentHistoryEntry struct {
	// ID is the ArgoCD-assigned sequence number for this sync event.
	ID int64
	// Revision is the Git commit SHA that was synced.
	Revision string
	// DeployedAt is the RFC 3339 timestamp when the sync completed.
	DeployedAt string
	// DeployStartedAt is the RFC 3339 timestamp when the sync began (may be empty).
	DeployStartedAt string
	// RepoURL is the source Git repository URL.
	RepoURL string
	// Path is the path within the repository that was synced.
	Path string
	// TargetRevision is the Git ref (branch/tag/commit) tracked by this Application.
	TargetRevision string
}

// GetAppDeploymentHistory reads the sync history of the app's chart ArgoCD
// Application for the given project/app/env. The Application is found by
// suparship identity labels (project/app/env) rather than by reconstructing its
// name, because the name is configurable (org ResourceNaming pattern) and
// per-cluster — labels are the stable key. The platform companion shares these
// labels, so Applications whose name ends in "-platform" are skipped; for a
// multi-cluster env the first matching cluster's Application is used. Returns an
// empty slice (not an error) when none exists or it has no history yet.
func (r *ArgoCDStatusReader) GetAppDeploymentHistory(ctx context.Context, projectName, appName, envName string) ([]DeploymentHistoryEntry, error) {
	sel := "suparship.io/project=" + projectName + ",suparship.io/app=" + appName + ",suparship.io/env=" + envName
	list, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil || list == nil || len(list.Items) == 0 {
		// Not found / no match is expected before the first deploy — empty, not error.
		return []DeploymentHistoryEntry{}, nil //nolint:nilerr
	}
	var obj map[string]any
	for i := range list.Items {
		if strings.HasSuffix(list.Items[i].GetName(), "-platform") {
			continue
		}
		obj = list.Items[i].Object
		break
	}
	if obj == nil {
		return []DeploymentHistoryEntry{}, nil
	}

	statusRaw, ok := obj["status"].(map[string]any)
	if !ok {
		return []DeploymentHistoryEntry{}, nil
	}

	historyRaw, ok := statusRaw["history"].([]any)
	if !ok {
		return []DeploymentHistoryEntry{}, nil
	}

	entries := make([]DeploymentHistoryEntry, 0, len(historyRaw))
	for _, item := range historyRaw {
		h, ok := item.(map[string]any)
		if !ok {
			continue
		}
		entry := DeploymentHistoryEntry{}

		// id is a JSON number
		switch v := h["id"].(type) {
		case json.Number:
			i, _ := v.Int64()
			entry.ID = i
		case float64:
			entry.ID = int64(v)
		case int64:
			entry.ID = v
		}

		entry.DeployedAt, _, _ = unstructuredString(h, "deployedAt")
		entry.DeployStartedAt, _, _ = unstructuredString(h, "deployStartedAt")

		// ArgoCD multi-source Applications (sources/revisions) take precedence
		// over the legacy single-source fields (source/revision).
		//
		// Multi-source layout:
		//   sources[]: array of {repoURL, path/ref, targetRevision, ...}
		//   revisions[]: parallel array of commit SHAs, one per source
		//
		// We prefer the source that has a "path" set (the Helm chart source)
		// over the "ref" source (the values ref), and use its corresponding revision.
		if sources, ok := h["sources"].([]any); len(sources) > 0 && ok {
			// Pick the commit SHA: all sources point to the same repo so all
			// revisions are identical in practice; we take the first non-empty one.
			if revisions, ok := h["revisions"].([]any); ok {
				for _, rv := range revisions {
					if s, ok := rv.(string); ok && s != "" {
						entry.Revision = s
						break
					}
				}
			}
			// Pick the "path" source (Helm chart) for display metadata.
			for _, src := range sources {
				s, ok := src.(map[string]any)
				if !ok {
					continue
				}
				path, _, _ := unstructuredString(s, "path")
				if path == "" {
					continue // skip ref-only sources (values sources)
				}
				entry.RepoURL, _, _ = unstructuredString(s, "repoURL")
				entry.Path = path
				entry.TargetRevision, _, _ = unstructuredString(s, "targetRevision")
				break
			}
		} else {
			// Legacy single-source Application.
			entry.Revision, _, _ = unstructuredString(h, "revision")
			entry.RepoURL, _, _ = unstructuredString(h, "source", "repoURL")
			entry.Path, _, _ = unstructuredString(h, "source", "path")
			entry.TargetRevision, _, _ = unstructuredString(h, "source", "targetRevision")
		}

		entries = append(entries, entry)
	}

	// Return in reverse-chronological order (most recent first).
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}

	return entries, nil
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
