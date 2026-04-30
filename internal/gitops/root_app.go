package gitops

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	rootAppName = "suparship-apps"

	// rootAppProject is the ArgoCD AppProject the root "App of Apps" is
	// scoped to. Bootstrapping (internal/bootstrap.ReconcileArgoCD)
	// ensures this AppProject exists with the right sourceRepos +
	// destinations to allow the root app + its child ApplicationSets.
	// Using "default" was a long-standing bug — the default project's
	// allow-list locks down repo URLs and produces the cryptic
	// "app is not allowed in project default" error in the ArgoCD UI.
	rootAppProject = "suparship-system"

	labelManagedBy = "suparship.io/managed-by"
	labelRole      = "suparship.io/role"
)

var argoCDAppGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// RootAppConfig holds the parameters needed to build the root ArgoCD Application.
type RootAppConfig struct {
	// ArgoCDRepoURL is the URL ArgoCD uses to clone the gitops repo (in-cluster URL).
	// Falls back to RepoURL when empty.
	ArgoCDRepoURL string
	// RepoURL is the external clone URL.
	RepoURL string
	// Branch is the target revision. Defaults to "HEAD".
	Branch string
	// SubPath mirrors PublisherConfig.SubPath so the root app's
	// source.path points at <SubPath>/_infra (or just _infra at the
	// repo root when SubPath is empty).
	SubPath string
}

// EnsureRootApplication creates or updates the ArgoCD root "App of Apps"
// Application that watches gitops-output/_infra and syncs ApplicationSets
// and AppProjects into the cluster.
//
// Ownership rules:
//   - If the Application does not exist, it is created with suparship ownership labels.
//   - If it exists and is owned by suparship, it is updated (e.g. to change the repo URL).
//   - If it exists but is NOT owned by suparship, it is left untouched and no error is returned.
//     This allows operators who prefer to manage the root app manually.
func EnsureRootApplication(ctx context.Context, dyn dynamic.Interface, cfg RootAppConfig) error {
	repoURL := cfg.ArgoCDRepoURL
	if repoURL == "" {
		repoURL = cfg.RepoURL
	}
	if repoURL == "" {
		return fmt.Errorf("cannot create root application: no repo URL configured")
	}
	branch := cfg.Branch
	if branch == "" {
		branch = "HEAD"
	}

	app := buildRootApp(repoURL, branch, cfg.SubPath)

	existing, err := dyn.Resource(argoCDAppGVR).Namespace(ArgoCDNamespace).Get(ctx, rootAppName, metav1.GetOptions{})
	if err != nil {
		// Application doesn't exist — create it.
		_, createErr := dyn.Resource(argoCDAppGVR).Namespace(ArgoCDNamespace).Create(ctx, app, metav1.CreateOptions{})
		if createErr != nil {
			return fmt.Errorf("create root application: %w", createErr)
		}
		return nil
	}

	// Application exists — check ownership before updating.
	labels := existing.GetLabels()
	if labels == nil || labels[labelManagedBy] != "suparship" {
		// Not ours — leave it alone. The operator manages their own root app.
		return nil
	}

	// Preserve the existing resourceVersion for the update.
	app.SetResourceVersion(existing.GetResourceVersion())
	if _, err := dyn.Resource(argoCDAppGVR).Namespace(ArgoCDNamespace).Update(ctx, app, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update root application: %w", err)
	}
	return nil
}

// buildRootApp constructs the unstructured ArgoCD Application object for the
// root "App of Apps" that watches _infra/ in the gitops repo.
func buildRootApp(repoURL, targetRevision, subPath string) *unstructured.Unstructured {
	obj := map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata": map[string]interface{}{
			"name":      rootAppName,
			"namespace": ArgoCDNamespace,
			"labels": map[string]interface{}{
				labelManagedBy: "suparship",
				labelRole:      "root-app",
			},
			"finalizers": []interface{}{
				"resources-finalizer.argocd.argoproj.io",
			},
		},
		"spec": map[string]interface{}{
			"project": rootAppProject,
			"source": map[string]interface{}{
				"repoURL":        repoURL,
				"targetRevision": targetRevision,
				"path":           joinSubPath(subPath, "_infra"),
				"directory": map[string]interface{}{
					"recurse": true,
				},
			},
			"destination": map[string]interface{}{
				"server":    "https://kubernetes.default.svc",
				"namespace": ArgoCDNamespace,
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
	}

	data, _ := json.Marshal(obj)
	u := &unstructured.Unstructured{}
	_ = json.Unmarshal(data, &u.Object)
	return u
}
