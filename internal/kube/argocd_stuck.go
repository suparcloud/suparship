package kube

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
)

// StuckApplication describes an ArgoCD Application stuck in Terminating: its
// deletion was requested but a finalizer can't complete, so it lingers
// indefinitely. The recurring cause on suparShip is a cascade-delete finalizer
// that can't resolve the Application's AppProject (removed too early), but any
// blocked finalizer produces the same symptom.
type StuckApplication struct {
	// Name is the Application CR name.
	Name string `json:"name"`
	// Project is its spec.project (the AppProject it references).
	Project string `json:"project,omitempty"`
	// DeletionTimestamp is when deletion was requested (RFC 3339).
	DeletionTimestamp string `json:"deletionTimestamp,omitempty"`
	// Finalizers are the finalizers still blocking removal.
	Finalizers []string `json:"finalizers,omitempty"`
	// Reason is a plain-language explanation (e.g. missing AppProject).
	Reason string `json:"reason"`
}

// argoResourcesFinalizer is ArgoCD's cascade-delete finalizer. Removing it lets
// the Application object be garbage-collected; deployed resources are then
// orphaned rather than cascade-deleted (acceptable when the cascade is already
// wedged — the alternative is an Application stuck forever).
const argoResourcesFinalizer = "resources-finalizer.argocd.argoproj.io"

// ListStuckApplications returns Applications in the ArgoCD namespace that are
// stuck Terminating — a non-empty deletionTimestamp plus remaining finalizers.
// It enriches each with a reason, flagging the common case where the referenced
// AppProject no longer exists (which blocks ArgoCD's cascade delete).
func (r *ArgoCDStatusReader) ListStuckApplications(ctx context.Context) ([]StuckApplication, error) {
	list, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing argocd apps: %w", err)
	}

	// Gather existing AppProject names once so we can explain missing-project hangs.
	existingProjects := map[string]bool{}
	if projList, perr := r.dynamic.Resource(appProjectGVR).Namespace(r.namespace).List(ctx, metav1.ListOptions{}); perr == nil {
		for _, p := range projList.Items {
			existingProjects[p.GetName()] = true
		}
	}

	var stuck []StuckApplication
	for _, item := range list.Items {
		delTS := item.GetDeletionTimestamp()
		finalizers := item.GetFinalizers()
		if delTS == nil || len(finalizers) == 0 {
			continue // not terminating, or nothing blocking it
		}
		project, _, _ := unstructuredString(item.Object, "spec", "project")
		reason := "Stuck in Terminating — a finalizer cannot complete."
		if project != "" && !existingProjects[project] {
			reason = fmt.Sprintf("Stuck in Terminating — its AppProject %q no longer exists, so ArgoCD's cascade delete is blocked.", project)
		}
		stuck = append(stuck, StuckApplication{
			Name:              item.GetName(),
			Project:           project,
			DeletionTimestamp: delTS.UTC().Format("2006-01-02T15:04:05Z"),
			Finalizers:        finalizers,
			Reason:            reason,
		})
	}
	return stuck, nil
}

// UnstickApplication removes ArgoCD's resources-finalizer from the named
// Application so a wedged deletion can complete. It only acts when the
// Application is actually Terminating (has a deletionTimestamp) — refusing to
// strip finalizers from a live app — and is a no-op if the app is already gone.
func (r *ArgoCDStatusReader) UnstickApplication(ctx context.Context, name string) error {
	app, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting application %q: %w", name, err)
	}
	if app.GetDeletionTimestamp() == nil {
		return fmt.Errorf("application %q is not being deleted; refusing to remove finalizers from a live app", name)
	}

	// Keep any non-ArgoCD finalizers intact; only drop the cascade one.
	var remaining []string
	for _, f := range app.GetFinalizers() {
		if f != argoResourcesFinalizer {
			remaining = append(remaining, f)
		}
	}

	patch := map[string]any{"metadata": map[string]any{"finalizers": remaining}}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal finalizer patch: %w", err)
	}
	if _, err := r.dynamic.Resource(argoCDAppGVR).Namespace(r.namespace).Patch(
		ctx, name, apitypes.MergePatchType, patchBytes, metav1.PatchOptions{},
	); err != nil {
		return fmt.Errorf("patching application %q finalizers: %w", name, err)
	}
	return nil
}
