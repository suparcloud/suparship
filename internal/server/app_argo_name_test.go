package server

import (
	"context"
	"net/http"
	"testing"
)

// With the default ArgoAppName pattern ({projectApp}), the project prefix is folded
// into the app name — so "demo-svc" and "svc" in project "demo" both resolve to the
// ArgoCD Application name "demo-svc-...". Creating/renaming the second must be
// rejected to keep Application names unique in the shared argocd namespace.
func TestCreateApp_ArgoNameCollisionRejected(t *testing.T) {
	mux, ah, appStore, _ := newTestAppCreateMux()
	cookie := sessionCookieFor(ah, "alice", "org_admin")
	img := map[string]any{"image": "ghcr.io/org/app:v1"}

	// "demo-svc" carries the project prefix → Argo name folds to "demo-svc".
	if rec := postCreateAppJSON(mux, cookie, "demo", createAppRequest{
		Name: "demo-svc", Template: "web-service", Values: img,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create demo-svc: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	// "svc" folds to the SAME Argo name "demo-svc" → conflict.
	rec := postCreateAppJSON(mux, cookie, "demo", createAppRequest{
		Name: "svc", Template: "web-service", Values: img,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("create svc: expected 409 (folds to demo-svc), got %d: %s", rec.Code, rec.Body.String())
	}
	if _, err := appStore.GetApp(context.Background(), "demo", "svc"); err == nil {
		t.Error("colliding app must not be persisted")
	}

	// A non-colliding name is accepted.
	if rec := postCreateAppJSON(mux, cookie, "demo", createAppRequest{
		Name: "other", Template: "web-service", Values: img,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create other: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}
