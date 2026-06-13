package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/session"
)

// fakeArgoCDStore adds ArgoCD import support to fakeClusterStore.
type fakeArgoCDStore struct {
	fakeClusterStore
	candidates []domain.ArgoCDClusterCandidate
	imported   []string
}

func (f *fakeArgoCDStore) ListArgoCDClusters(_ context.Context) ([]domain.ArgoCDClusterCandidate, error) {
	return f.candidates, nil
}

func (f *fakeArgoCDStore) ImportArgoCDCluster(_ context.Context, name string) (*domain.Cluster, error) {
	for _, c := range f.candidates {
		if c.Name != name {
			continue
		}
		if c.AlreadyRegistered {
			return nil, &importError{"cluster is already registered"}
		}
		if !c.Importable {
			return nil, &importError{c.Reason}
		}
		f.imported = append(f.imported, name)
		return &domain.Cluster{Name: name, APIServer: c.Server, Status: "ready"}, nil
	}
	return nil, &importError{"argocd cluster not found"}
}

type importError struct{ msg string }

func (e *importError) Error() string { return e.msg }

// recordingReconciler records that ReconcileSecretStores was invoked.
type recordingReconciler struct{ called chan struct{} }

func (r *recordingReconciler) ReconcileSecretStores(_ context.Context) error {
	select {
	case r.called <- struct{}{}:
	default:
	}
	return nil
}

func newImportMux(t *testing.T, store domain.ClusterStore, rec SecretStoreReconciler) (*http.ServeMux, *authHandler) {
	t.Helper()
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)
	ch := &clusterHandler{
		store:           store,
		auth:            ah,
		orgStore:        &staticOrgProvider{org: testRBACOrg()},
		storeReconciler: rec,
	}
	ch.registerRoutes(mux)
	return mux, ah
}

func TestHandleImport_TokenClusterWiresConnection(t *testing.T) {
	store := &fakeArgoCDStore{candidates: []domain.ArgoCDClusterCandidate{
		{Name: "tok", Server: "https://tok:6443", AuthType: "token", Importable: true},
	}}
	rec := &recordingReconciler{called: make(chan struct{}, 1)}
	mux, ah := newImportMux(t, store, rec)

	body, _ := json.Marshal(importClustersRequest{Names: []string{"tok"}})
	req := httptest.NewRequest("POST", "/api/v1/clusters/import", strings.NewReader(string(body)))
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec2.Code, rec2.Body.String())
	}
	var resp importClustersResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Imported) != 1 || resp.Imported[0].Name != "tok" {
		t.Errorf("imported = %+v, want [tok]", resp.Imported)
	}
	if len(resp.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none", resp.Skipped)
	}
	if len(store.imported) != 1 {
		t.Errorf("store should have imported 1 cluster, got %d", len(store.imported))
	}

	// The provisioning hook (reconcile secret stores) must fire — this is what
	// wires secret delivery, the whole point of import.
	select {
	case <-rec.called:
	case <-time.After(2 * time.Second):
		t.Error("ReconcileSecretStores was not called — secret-delivery connection not provisioned")
	}
}

func TestHandleImport_SkipsExecAndAlreadyRegistered(t *testing.T) {
	store := &fakeArgoCDStore{candidates: []domain.ArgoCDClusterCandidate{
		{Name: "eks", Server: "https://eks:6443", AuthType: "exec", Importable: false, Reason: "exec / cloud-IAM auth not supported"},
		{Name: "dup", Server: "https://dup:6443", AuthType: "token", Importable: true, AlreadyRegistered: true},
	}}
	rec := &recordingReconciler{called: make(chan struct{}, 1)}
	mux, ah := newImportMux(t, store, rec)

	body, _ := json.Marshal(importClustersRequest{Names: []string{"eks", "dup"}})
	req := httptest.NewRequest("POST", "/api/v1/clusters/import", strings.NewReader(string(body)))
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	var resp importClustersResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Imported) != 0 {
		t.Errorf("imported = %+v, want none", resp.Imported)
	}
	if len(resp.Skipped) != 2 {
		t.Fatalf("skipped = %+v, want 2", resp.Skipped)
	}
	reasons := map[string]string{}
	for _, s := range resp.Skipped {
		reasons[s.Name] = s.Reason
	}
	if !strings.Contains(reasons["eks"], "exec") {
		t.Errorf("eks skip reason = %q, want exec mention", reasons["eks"])
	}
	if !strings.Contains(reasons["dup"], "already registered") {
		t.Errorf("dup skip reason = %q, want already-registered", reasons["dup"])
	}
}

func TestHandleImport_RequiresOrgAdmin(t *testing.T) {
	store := &fakeArgoCDStore{}
	mux, ah := newImportMux(t, store, nil)

	for _, tc := range []struct {
		method, path string
	}{
		{"GET", "/api/v1/clusters/argocd"},
		{"POST", "/api/v1/clusters/import"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		req.AddCookie(sessionCookieFor(ah, "carol", "viewer"))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s as viewer: got %d, want 403", tc.method, tc.path, rr.Code)
		}
	}
}
