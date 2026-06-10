package kube

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// appWithHistory builds an ArgoCD Application CR with a one-entry sync history.
func appWithHistory(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": name, "namespace": "argocd"},
		"status": map[string]any{
			"history": []any{
				map[string]any{
					"id":         int64(1),
					"revision":   "164770c",
					"deployedAt": "2026-06-10T00:07:44Z",
					"source":     map[string]any{"repoURL": "http://gitea/gitops.git", "path": "charts/web/latest"},
				},
			},
		},
	}}
}

// The ArgoCD Application is named {project}-{app}-{env} (gitops.ApplicationName).
// The reader must include the project prefix or it queries a nonexistent
// Application and silently returns no history.
func TestGetAppDeploymentHistory_UsesProjectPrefixedName(t *testing.T) {
	r := newStuckReader(t, appWithHistory("test-web-staging"))

	hist, err := r.GetAppDeploymentHistory(context.Background(), "test", "web", "staging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hist) != 1 {
		t.Fatalf("expected 1 history entry for test-web-staging, got %d", len(hist))
	}
	if hist[0].Revision != "164770c" {
		t.Errorf("revision = %q, want 164770c", hist[0].Revision)
	}

	// The old project-less name ({app}-{env}) must NOT resolve — proving the
	// project prefix is what's being matched, not an incidental substring.
	none, err := r.GetAppDeploymentHistory(context.Background(), "", "web", "staging")
	if err != nil {
		t.Fatalf("unexpected error for project-less lookup: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("project-less name should not resolve to test-web-staging, got %d entries", len(none))
	}
}
