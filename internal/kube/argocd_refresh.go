package kube

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
)

// argoCDAppSetGVR is the GroupVersionResource for ArgoCD ApplicationSet CRs.
var argoCDAppSetGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applicationsets",
}

const (
	// argoRefreshAnnotation, set on an Application, makes ArgoCD reconcile it
	// immediately (re-check git) instead of waiting for the poll interval. Under
	// automated sync it then syncs the new commit on its own.
	argoRefreshAnnotation = "argocd.argoproj.io/refresh"
	argoRefreshNormal     = "normal"
	// appSetRefreshAnnotation, set on an ApplicationSet, makes the appset
	// controller re-run its git generator immediately — so Applications for new
	// apps/envs/previews are created without waiting for the poll interval.
	appSetRefreshAnnotation = "argocd.argoproj.io/application-set-refresh"

	// Label keys on suparship-generated Applications (mirrors internal/gitops).
	labelAppKey     = "suparship.io/app"
	labelProjectKey = "suparship.io/project"
)

// RefreshApps annotates every Application labeled for the given project and one
// of appNames with the ArgoCD refresh annotation, so ArgoCD reconciles them
// immediately (and, under automated sync, syncs the freshly committed change).
// It is best-effort: it attempts every match and returns the first patch error,
// if any — a failure just falls back to ArgoCD's normal poll. A nil reader, no
// dynamic client, or empty inputs is a no-op.
func (r *ArgoCDStatusReader) RefreshApps(ctx context.Context, project string, appNames []string) error {
	if r == nil || r.dynamic == nil || project == "" || len(appNames) == 0 {
		return nil
	}
	// Select both the chart Application and the platform Application for each
	// app/env (all carry the app+project labels).
	sel := fmt.Sprintf("%s=%s,%s in (%s)", labelProjectKey, project, labelAppKey, strings.Join(appNames, ","))
	list, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return fmt.Errorf("list applications for refresh: %w", err)
	}
	patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, argoRefreshAnnotation, argoRefreshNormal))
	var firstErr error
	for i := range list.Items {
		name := list.Items[i].GetName()
		if _, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).Patch(
			ctx, name, apitypes.MergePatchType, patch, metav1.PatchOptions{},
		); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("refresh application %q: %w", name, err)
		}
	}
	return firstErr
}

// RefreshAppSets annotates the named ApplicationSets with the appset-refresh
// annotation so their git generators re-scan immediately — used when a publish
// creates new gitops paths (previews, new apps/envs) whose Applications don't
// exist yet. Best-effort: returns the first error but attempts all.
func (r *ArgoCDStatusReader) RefreshAppSets(ctx context.Context, appSetNames []string) error {
	if r == nil || r.dynamic == nil || len(appSetNames) == 0 {
		return nil
	}
	patch := []byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, appSetRefreshAnnotation, "true"))
	var firstErr error
	for _, name := range appSetNames {
		if _, err := r.dynamic.Resource(argoCDAppSetGVR).Namespace(r.namespace).Patch(
			ctx, name, apitypes.MergePatchType, patch, metav1.PatchOptions{},
		); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("refresh applicationset %q: %w", name, err)
		}
	}
	return firstErr
}
