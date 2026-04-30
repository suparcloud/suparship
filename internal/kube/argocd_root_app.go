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

// applicationGVR is the GroupVersionResource for ArgoCD Application CRDs.
var applicationGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// rootAppName is the canonical name of the suparship root "App of Apps".
const rootAppName = "suparship-apps"

// normalizeRootSubPath strips whitespace + slashes and treats "."/"./"
// as the empty (repo-root) form. Mirrors gitops.normalizeSubPath; kept
// in this package to avoid pulling internal/gitops into the kube
// dependency graph.
func normalizeRootSubPath(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '/') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '/') {
		s = s[:len(s)-1]
	}
	if s == "" || s == "." {
		return ""
	}
	return s
}

// EnsureRootArgoApp creates the suparship-apps ArgoCD Application (the root
// "App of Apps") if it does not already exist.
//
// The Application is derived entirely from suparship's live configuration:
//   - repoURL is the ArgoCD-facing gitops repo URL (argoCDRepoURL from ConfigMap)
//   - branch is the target Git revision (branch from ConfigMap, defaults to "main")
//   - argoCDNS is the namespace where ArgoCD is installed (default "argocd")
//
// This replaces the manual `kubectl apply -f config/gitops/root-app.yaml` step
// that was previously required after initial cluster bootstrap.
//
// Behaviour:
//   - Idempotent: if the Application already exists it is left unchanged.
//   - Non-fatal: if the Application CRD is not registered (ArgoCD not installed)
//     or the dynamic client is nil, a warning is logged and nil is returned.
//   - Create-only: an existing Application is never modified, preserving any
//     live sync state, custom annotations, or manual overrides.
func EnsureRootArgoApp(ctx context.Context, dyn dynamic.Interface, repoURL, branch, argoCDNS, subPath string) error {
	if dyn == nil {
		slog.Warn("argocd: dynamic client not available — skipping suparship-apps root Application check")
		return nil
	}
	if repoURL == "" {
		// GitOps repo not configured yet — skip silently.
		return nil
	}
	if branch == "" {
		branch = "main"
	}
	if argoCDNS == "" {
		argoCDNS = "argocd"
	}

	client := dyn.Resource(applicationGVR).Namespace(argoCDNS)

	_, err := client.Get(ctx, rootAppName, metav1.GetOptions{})
	if err == nil {
		slog.Debug("argocd: suparship-apps root Application already exists")
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking suparship-apps Application: %w", err)
	}

	obj := buildRootArgoApp(repoURL, branch, argoCDNS, subPath)
	_, createErr := client.Create(ctx, obj, metav1.CreateOptions{})
	if createErr == nil {
		slog.Info("argocd: created suparship-apps root Application",
			"repoURL", repoURL,
			"branch", branch,
			"namespace", argoCDNS,
		)
		return nil
	}
	if apierrors.IsAlreadyExists(createErr) {
		slog.Debug("argocd: suparship-apps root Application already exists (concurrent create)")
		return nil
	}
	if apierrors.IsNotFound(createErr) {
		slog.Warn("argocd: Application CRD not found — install ArgoCD before running suparship in production",
			"namespace", argoCDNS)
		return nil
	}
	return fmt.Errorf("creating suparship-apps Application: %w", createErr)
}

// RootArgoAppExists returns true when the suparship-apps Application exists in
// argoCDNS. It returns false (not an error) when ArgoCD is not installed or
// the dynamic client is nil.
func RootArgoAppExists(ctx context.Context, dyn dynamic.Interface, argoCDNS string) (bool, error) {
	if dyn == nil {
		return false, nil
	}
	if argoCDNS == "" {
		argoCDNS = "argocd"
	}
	_, err := dyn.Resource(applicationGVR).Namespace(argoCDNS).Get(ctx, rootAppName, metav1.GetOptions{})
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("checking suparship-apps Application: %w", err)
}

// buildRootArgoApp returns the unstructured Application manifest for the
// suparship root "App of Apps". The spec mirrors config/gitops/root-app.yaml
// but substitutes live values from suparship's ConfigMap instead of requiring
// a manual kubectl apply with placeholder substitution.
func buildRootArgoApp(repoURL, branch, argoCDNS, subPath string) *unstructured.Unstructured {
	infraPath := "_infra"
	if s := normalizeRootSubPath(subPath); s != "" {
		infraPath = s + "/_infra"
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata": map[string]interface{}{
				"name":      rootAppName,
				"namespace": argoCDNS,
				"labels": map[string]interface{}{
					"suparship.io/managed-by": "suparship",
					"suparship.io/role":       "root-app",
				},
				// Cascade-delete child ApplicationSets when this root app is deleted.
				"finalizers": []interface{}{
					"resources-finalizer.argocd.argoproj.io",
				},
			},
			"spec": map[string]interface{}{
				"project": systemProjectName, // suparship-system
				"source": map[string]interface{}{
					"repoURL":        repoURL,
					"targetRevision": branch,
					// _infra/ contains AppProjects, ApplicationSets, and Kargo CRs.
					// Per-app data (app.yaml, values.yaml) lives in env-specific paths
					// discovered by the ApplicationSet Git File generator — not synced here.
					"path": infraPath,
					"directory": map[string]interface{}{
						"recurse": true,
					},
				},
				"destination": map[string]interface{}{
					// Apply ArgoCD objects (AppProject, ApplicationSet) into the
					// argocd namespace on the tooling cluster. ApplicationSets then
					// target workload clusters via their own destination.server fields.
					"server":    "https://kubernetes.default.svc",
					"namespace": argoCDNS,
				},
				"syncPolicy": map[string]interface{}{
					"automated": map[string]interface{}{
						"prune":    true,
						"selfHeal": true,
					},
					"syncOptions": []interface{}{
						"CreateNamespace=true",
					},
				},
			},
		},
	}
}
