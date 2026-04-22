package kube

import (
	"context"
	"fmt"
	"log/slog"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// appProjectGVR is the GroupVersionResource for ArgoCD AppProject CRDs.
var appProjectGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "appprojects",
}

// systemProjectName is the name of the privileged ArgoCD AppProject that
// suparship uses for all its infrastructure Applications.
const systemProjectName = "suparship-system"

// EnsureArgoCDSystemProject creates the suparship-system ArgoCD AppProject in
// argoCDNS if it does not already exist. It is fully idempotent: if the
// project is already present (created by Helm, a previous call, or by hand)
// it returns nil immediately without modifying the existing object.
//
// Non-fatal cases — the function logs a warning and returns nil:
//   - ArgoCD is not installed (AppProject CRD not registered on the cluster)
//   - The dynamic client is nil
//
// A non-nil error is returned only for unexpected Kubernetes API failures
// (e.g. permission denied, network error) that the caller should surface.
func EnsureArgoCDSystemProject(ctx context.Context, dyn dynamic.Interface, argoCDNS string) error {
	if dyn == nil {
		slog.Warn("argocd: dynamic client not available — skipping suparship-system AppProject check")
		return nil
	}
	if argoCDNS == "" {
		argoCDNS = "argocd"
	}

	client := dyn.Resource(appProjectGVR).Namespace(argoCDNS)

	// Check whether the project already exists.
	_, err := client.Get(ctx, systemProjectName, metav1.GetOptions{})
	if err == nil {
		slog.Debug("argocd: suparship-system AppProject already exists")
		return nil
	}

	if !apierrors.IsNotFound(err) {
		// Unexpected error (e.g. RBAC denied listing in argocd ns).
		return fmt.Errorf("checking suparship-system AppProject: %w", err)
	}

	// Not found — try to create it. If the CRD itself is not registered
	// (ArgoCD not installed), the Create call will also return NotFound;
	// we treat that as a non-fatal warning rather than a hard error.
	obj := buildSystemAppProject(argoCDNS)
	_, createErr := client.Create(ctx, obj, metav1.CreateOptions{})
	if createErr == nil {
		slog.Info("argocd: created suparship-system AppProject", "namespace", argoCDNS)
		return nil
	}
	if apierrors.IsAlreadyExists(createErr) {
		// Another process created it between our Get and Create — that's fine.
		slog.Debug("argocd: suparship-system AppProject already exists (concurrent create)")
		return nil
	}
	if apierrors.IsNotFound(createErr) {
		// The AppProject CRD itself is not registered: ArgoCD is not installed.
		slog.Warn("argocd: AppProject CRD not found — install ArgoCD before running suparship in production",
			"namespace", argoCDNS)
		return nil
	}

	return fmt.Errorf("creating suparship-system AppProject: %w", createErr)
}

// ArgoCDSystemProjectExists returns true when the suparship-system AppProject
// is present in argoCDNS. It returns false (not an error) when ArgoCD is not
// installed or the dynamic client is nil.
func ArgoCDSystemProjectExists(ctx context.Context, dyn dynamic.Interface, argoCDNS string) (bool, error) {
	if dyn == nil {
		return false, nil
	}
	if argoCDNS == "" {
		argoCDNS = "argocd"
	}

	_, err := dyn.Resource(appProjectGVR).Namespace(argoCDNS).Get(ctx, systemProjectName, metav1.GetOptions{})
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("checking suparship-system AppProject: %w", err)
}

// buildSystemAppProject returns the unstructured AppProject manifest for
// suparship-system. The spec is intentionally permissive: suparship needs
// to manage infrastructure resources (AppProject, ApplicationSet, Kargo CRs,
// SealedSecrets, ClusterSecretStores) across all registered clusters.
func buildSystemAppProject(namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "AppProject",
			"metadata": map[string]interface{}{
				"name":      systemProjectName,
				"namespace": namespace,
				"annotations": map[string]interface{}{
					// sync-wave -2: applied before gitops-generated AppProjects (-1)
					// and Applications (0) so the project always exists first.
					"argocd.argoproj.io/sync-wave": "-2",
				},
				"labels": map[string]interface{}{
					"suparship.io/managed-by": "suparship",
					"suparship.io/role":       "system-project",
				},
			},
			"spec": map[string]interface{}{
				"description": "Privileged ArgoCD project for suparship infrastructure resources",
				"sourceRepos": []interface{}{"*"},
				"destinations": []interface{}{
					map[string]interface{}{
						"server":    "*",
						"namespace": "*",
					},
				},
				"clusterResourceWhitelist": []interface{}{
					map[string]interface{}{"group": "*", "kind": "*"},
				},
				"namespaceResourceWhitelist": []interface{}{
					map[string]interface{}{"group": "*", "kind": "*"},
				},
			},
		},
	}
}
