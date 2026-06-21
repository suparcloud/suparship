package kube

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// appWithHistory builds an ArgoCD Application CR with a one-entry sync history,
// labelled with suparship identity so the label-based history lookup finds it.
// name is the (configurable, per-cluster) Application name.
func appWithHistory(name, project, app, env, revision string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": name, "namespace": "argocd"},
		"status": map[string]any{
			"history": []any{
				map[string]any{
					"id":         int64(1),
					"revision":   revision,
					"deployedAt": "2026-06-10T00:07:44Z",
					"source":     map[string]any{"repoURL": "http://gitea/gitops.git", "path": "charts/web/latest"},
				},
			},
		},
	}}
	u.SetLabels(map[string]string{
		"suparship.io/project": project,
		"suparship.io/app":     app,
		"suparship.io/env":     env,
	})
	return u
}

// The Application name is configurable and per-cluster, so history is looked up
// by suparship identity labels (project/app/env), not by reconstructing the
// name. The platform companion shares those labels and must be skipped so
// history comes from the chart Application.
func TestGetAppDeploymentHistory_ByLabelsSkipsPlatform(t *testing.T) {
	// Platform listed first: without the -platform skip it would be picked.
	platform := appWithHistory("test-web-staging-eastus-platform", "test", "web", "staging", "PLATFORM")
	chart := appWithHistory("test-web-staging-eastus", "test", "web", "staging", "164770c")
	r := newStuckReader(t, platform, chart)

	hist, err := r.GetAppDeploymentHistory(context.Background(), "test", "web", "staging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hist) != 1 || hist[0].Revision != "164770c" {
		t.Fatalf("expected chart history (164770c), got %+v", hist)
	}

	// A different env must not resolve to the staging Application.
	none, err := r.GetAppDeploymentHistory(context.Background(), "test", "web", "prod")
	if err != nil {
		t.Fatalf("unexpected error for env=prod: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("env=prod should not match the staging app, got %d entries", len(none))
	}
}
