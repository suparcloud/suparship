package kube

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnvAppsSettled reports whether every generated (non-platform) ArgoCD
// Application for an app's env has reconciled AFTER since, is Synced, and has
// no sync operation running — i.e. the change committed at `since` has been
// applied to the cluster. It also returns the Applications' names so the caller
// can nudge a refresh (ArgoCD's git poll is ~3 minutes). No Application at all
// counts as settled: there is nothing to wait for.
//
// Used to sequence the host swap: ingress-nginx's admission webhook rejects an
// Ingress that claims a host+path another Ingress still holds, so the side
// giving a hostname up must be admitted before the other side claims it.
func (r *ArgoCDStatusReader) EnvAppsSettled(ctx context.Context, projectName, appName, envName string, since time.Time) (bool, []string, error) {
	if r == nil || r.dynamic == nil {
		return true, nil, nil
	}
	sel := "suparship.io/project=" + projectName + ",suparship.io/app=" + appName + ",suparship.io/env=" + envName
	list, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return false, nil, fmt.Errorf("listing argocd apps for %s/%s env %s: %w", projectName, appName, envName, err)
	}
	settled := true
	var names []string
	for i := range list.Items {
		item := &list.Items[i]
		if strings.HasSuffix(item.GetName(), "-platform") {
			continue
		}
		names = append(names, item.GetName())
		status, _ := item.Object["status"].(map[string]any)
		syncStatus, _, _ := unstructuredString(status, "sync", "status")
		opPhase, _, _ := unstructuredString(status, "operationState", "phase")
		reconciledAt, _, _ := unstructuredString(status, "reconciledAt")
		ts, terr := time.Parse(time.RFC3339, reconciledAt)
		if terr != nil || !ts.After(since) || syncStatus != "Synced" || opPhase == "Running" {
			settled = false
		}
	}
	return settled, names, nil
}
