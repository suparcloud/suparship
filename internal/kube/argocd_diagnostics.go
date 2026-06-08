package kube

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/suparcloud/suparship/internal/domain"
)

// GetAppDiagnostics reads one ArgoCD Application CR and converts its failure
// signals — status.conditions[] (error/warning types), a non-Healthy
// status.health.message, and a failed status.operationState sync — into
// domain.Diagnostics with plain-language hints. Returns an empty slice (not an
// error) when the Application is absent (expected before the first publish) or
// fully healthy. source labels where the diagnostics came from (e.g. "argocd"
// for the chart app, "argocd-platform" for the ConfigMap/ExternalSecret app).
//
// Why read the Application CR rather than the workload: a failed sync produces
// no workload at all, so replica-count status just reports "not deployed" with
// no reason. ArgoCD's conditions/health carry the actual cause — including
// ExternalSecret "not ready" errors, which ArgoCD's bundled health check rolls
// up into the platform app's health.
func (r *ArgoCDStatusReader) GetAppDiagnostics(ctx context.Context, argoAppName, source string) ([]domain.Diagnostic, error) {
	raw, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).Get(ctx, argoAppName, metav1.GetOptions{})
	if err != nil {
		return nil, nil //nolint:nilerr // absent app == nothing to report
	}
	status, ok := raw.Object["status"].(map[string]any)
	if !ok {
		return nil, nil
	}

	var out []domain.Diagnostic

	// status.conditions[]: ArgoCD reports spec/sync/comparison errors here.
	// Condition types ending in "Error" are hard errors; "Warning" types are
	// advisory. Healthy apps usually have no conditions.
	if conditions, ok := status["conditions"].([]any); ok {
		for _, c := range conditions {
			cond, ok := c.(map[string]any)
			if !ok {
				continue
			}
			ctype, _, _ := unstructuredString(cond, "type")
			msg, _, _ := unstructuredString(cond, "message")
			if msg == "" {
				continue
			}
			level := domain.DiagnosticWarning
			if strings.Contains(ctype, "Error") {
				level = domain.DiagnosticError
			}
			out = append(out, domain.Diagnostic{
				Source: source,
				Level:  level,
				Title:  "ArgoCD: " + condTitle(ctype),
				Detail: msg,
				Hint:   hintForMessage(msg),
			})
		}
	}

	// status.health: a Degraded/Missing/Unknown health with a message is a
	// resource-level problem (e.g. ExternalSecret not ready, CrashLoopBackOff).
	if health, ok := status["health"].(map[string]any); ok {
		hstatus, _, _ := unstructuredString(health, "status")
		hmsg, _, _ := unstructuredString(health, "message")
		if hmsg != "" && hstatus != "" && hstatus != "Healthy" && hstatus != "Progressing" {
			out = append(out, domain.Diagnostic{
				Source: source,
				Level:  domain.DiagnosticError,
				Title:  "ArgoCD health: " + hstatus,
				Detail: hmsg,
				Hint:   hintForMessage(hmsg),
			})
		}
	}

	// status.operationState: a failed sync operation carries its error message.
	if opState, ok := status["operationState"].(map[string]any); ok {
		phase, _, _ := unstructuredString(opState, "phase")
		opMsg, _, _ := unstructuredString(opState, "message")
		if opMsg != "" && (phase == "Failed" || phase == "Error") {
			out = append(out, domain.Diagnostic{
				Source: source,
				Level:  domain.DiagnosticError,
				Title:  "ArgoCD sync " + strings.ToLower(phase),
				Detail: opMsg,
				Hint:   hintForMessage(opMsg),
			})
		}
	}

	return out, nil
}

// condTitle humanizes an ArgoCD condition type for display.
func condTitle(ctype string) string {
	if ctype == "" {
		return "condition"
	}
	return ctype
}

// hintForMessage maps known upstream error substrings to a plain-language fix.
// Returns "" when nothing matches — the raw Detail still tells the full story.
// Patterns mirror the failure classes seen operating suparship; add more as
// new ones surface rather than trying to be exhaustive up front.
func hintForMessage(msg string) string {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "clustersecretstore") && (strings.Contains(m, "not ready") || strings.Contains(m, "invalidproviderconfig")):
		return "The cluster's 1Password ClusterSecretStore isn't ready. Check the Connect Server URL (host, namespace, and port — Connect's API is usually :8080) under Settings → Secrets Backend, and that the cluster's Connect token has access to every vault the store lists."
	case strings.Contains(m, "i/o timeout") || strings.Contains(m, "no such host") || strings.Contains(m, "connection refused"):
		return "ESO can't reach the 1Password Connect server. Verify the Connect Server URL host/port and that Connect is deployed and reachable from this cluster."
	case strings.Contains(m, "secretstore") && strings.Contains(m, "not found"):
		return "The referenced secret store doesn't exist on the cluster yet. Paste the cluster's Connect token under Settings → Secrets Backend → Cluster Connect Tokens to publish it."
	case strings.Contains(m, "appproject") && strings.Contains(m, "not found"):
		return "The app's ArgoCD project is missing. This usually self-heals once the root app syncs; if an Application is stuck Terminating, it's the known finalizer race."
	case strings.Contains(m, "do not match any of the allowed destinations"):
		return "The app's target cluster isn't in its AppProject's allowed destinations. Re-publish the app (Sync to Git) so the AppProject regenerates with the correct destination, then sync the root app."
	case strings.Contains(m, "cluster") && strings.Contains(m, "not found"):
		return "ArgoCD has no cluster registered for this destination. Check the cluster's API server URL for typos/whitespace under Settings → Clusters."
	case strings.Contains(m, "invalidimagename") || strings.Contains(m, "imagepullbackoff") || strings.Contains(m, "errimagepull"):
		return "The pod can't pull its image. Set a real image repository for the app (the default is a placeholder) and confirm the registry/credentials."
	default:
		return ""
	}
}
