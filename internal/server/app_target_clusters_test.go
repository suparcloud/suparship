package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// The test org (testRBACOrg) registers envs "staging" and "prod", each with
// ClusterRefs=["in-cluster"]. These tests exercise the per-app, per-env
// TargetClusters wire field: folding into EnvironmentDefaults and rejecting a
// cluster that is not one of the env's registered ClusterRefs.

func TestCreateApp_TargetClusters_RejectsUnknownCluster(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()

	rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
		Name:     "my-app",
		Template: "web-service",
		Values:   map[string]any{"image": "ghcr.io/org/app:v1"},
		// "other-cluster" is not in staging's ClusterRefs → 422.
		TargetClusters: map[string][]string{"staging": {"other-cluster"}},
	})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for out-of-env cluster, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := appStore.GetApp(context.Background(), "demo", "my-app"); err == nil {
		t.Error("app must not be persisted when validation fails")
	}
}

func TestCreateApp_TargetClusters_AcceptsSentinelAndSubset(t *testing.T) {
	cases := []struct {
		name string
		sel  []string
	}{
		{"sentinel", []string{"*"}},
		{"subset", []string{"in-cluster"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, ah, appStore, _ := newTestAppCreateMux()

			rec := postCreateAppJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), "demo", createAppRequest{
				Name:           "my-app",
				Template:       "web-service",
				Values:         map[string]any{"image": "ghcr.io/org/app:v1"},
				TargetClusters: map[string][]string{"staging": tc.sel},
			})

			if rec.Code != http.StatusCreated {
				t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
			}

			// DTO must echo the selection back.
			var resp createAppResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if got := resp.App.TargetClusters["staging"]; !equalStrings(got, tc.sel) {
				t.Errorf("DTO targetClusters[staging] = %v, want %v", got, tc.sel)
			}

			// Selection must be folded into EnvironmentDefaults on the stored app.
			app, err := appStore.GetApp(context.Background(), "demo", "my-app")
			if err != nil {
				t.Fatalf("expected persisted app: %v", err)
			}
			if got := app.Spec.EnvironmentDefaults["staging"].TargetClusters; !equalStrings(got, tc.sel) {
				t.Errorf("persisted EnvironmentDefaults[staging].TargetClusters = %v, want %v", got, tc.sel)
			}
		})
	}
}

func TestUpdateApp_TargetClusters_FoldAndValidate(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	// Create a plain app first (no target clusters).
	rec := postCreateAppJSON(mux, cookie, "demo", createAppRequest{
		Name:     "my-app",
		Template: "web-service",
		Values:   map[string]any{"image": "ghcr.io/org/app:v1"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup create failed: %d: %s", rec.Code, rec.Body.String())
	}

	// PATCH with an out-of-env cluster is rejected.
	rec = patchAppJSON(mux, cookie, "demo", "my-app",
		updateAppRequest{TargetClusters: map[string][]string{"staging": {"nope"}}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for out-of-env cluster on patch, got %d: %s", rec.Code, rec.Body.String())
	}

	// PATCH with the all-clusters sentinel is accepted and folded.
	rec = patchAppJSON(mux, cookie, "demo", "my-app",
		updateAppRequest{TargetClusters: map[string][]string{"staging": {"*"}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for sentinel patch, got %d: %s", rec.Code, rec.Body.String())
	}
	app, err := appStore.GetApp(context.Background(), "demo", "my-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got := app.Spec.EnvironmentDefaults["staging"].TargetClusters; !equalStrings(got, []string{"*"}) {
		t.Errorf("after sentinel patch, TargetClusters[staging] = %v, want [*]", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
