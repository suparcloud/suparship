package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeFreightHistorian implements KargoPromoter + kargoFreightHistorian: a
// canned freight history plus a recording PromoteFreight, so rollback tests
// exercise the real handler wiring without a cluster.
type fakeFreightHistorian struct {
	recordingPromoter
	history      []KargoFreightRecord
	historyErr   error
	promoted     []string // freight names PromoteFreight was called with
	promoteErr   error
	promotedEnvs []string
}

func (f *fakeFreightHistorian) StageFreightHistory(_ context.Context, _, _, _ string, _ int) ([]KargoFreightRecord, error) {
	return f.history, f.historyErr
}

func (f *fakeFreightHistorian) PromoteFreight(_ context.Context, _, appName, envName, freightName string) (KargoPromotionResult, error) {
	if f.promoteErr != nil {
		return KargoPromotionResult{}, f.promoteErr
	}
	f.promoted = append(f.promoted, freightName)
	f.promotedEnvs = append(f.promotedEnvs, envName)
	return KargoPromotionResult{Name: appName + "-" + envName + "-1", Stage: appName + "-" + envName, Freight: freightName, Phase: "Pending"}, nil
}

func rollbackHistory() []KargoFreightRecord {
	return []KargoFreightRecord{
		{Name: "fr-current", Current: true, Images: []KargoFreightImage{{RepoURL: "ghcr.io/acme/web", Tag: "v3"}}},
		{Name: "fr-old", Images: []KargoFreightImage{{RepoURL: "ghcr.io/acme/web", Tag: "v2"}}},
		{Name: "fr-mixed", Images: []KargoFreightImage{
			{RepoURL: "ghcr.io/acme/web", Tag: "v1"},
			{RepoURL: "ghcr.io/acme/api", Tag: "z9"},
		}},
	}
}

func postRollbackJSON(mux *http.ServeMux, cookie *http.Cookie, projectName, appName, envName string, body any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(body)
	url := "/api/v1/projects/" + projectName + "/apps/" + appName + "/environments/" + envName + "/rollback"
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Rolling back to a previously-deployed freight places the rollback hold
// (PinnedFrom="rollback", single-tag freight also pins the tag), republishes,
// and re-promotes exactly that freight.
func TestRollbackAppEnv_HoldsAndPromotes(t *testing.T) {
	pub := &recordingPublisher{}
	historian := &fakeFreightHistorian{history: rollbackHistory()}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, pub, historian, nil)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postRollbackJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app", "staging",
		rollbackAppEnvRequest{Freight: "fr-old"})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}

	if len(historian.promoted) != 1 || historian.promoted[0] != "fr-old" {
		t.Fatalf("expected PromoteFreight(fr-old), got %v", historian.promoted)
	}
	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	ov := app.Spec.EnvironmentDefaults["staging"]
	if ov.PinnedFrom != "rollback" {
		t.Errorf("PinnedFrom = %q, want rollback", ov.PinnedFrom)
	}
	if ov.PinnedImageTag != "v2" {
		t.Errorf("PinnedImageTag = %q, want v2 (single distinct tag)", ov.PinnedImageTag)
	}
	if pub.publishAppCalls == 0 && pub.batchAppCalls == 0 {
		t.Error("expected a republish (auto-promotion policy must flip off)")
	}
}

// A freight carrying several distinct tags can't be expressed as one pinned tag:
// the hold is placed with an empty PinnedImageTag (the promotion writes each
// component's own tag; CD-managed preserve keeps them on republish).
func TestRollbackAppEnv_MultiTagFreightLeavesTagEmpty(t *testing.T) {
	pub := &recordingPublisher{}
	historian := &fakeFreightHistorian{history: rollbackHistory()}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, pub, historian, nil)

	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	resp := postRollbackJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app", "staging",
		rollbackAppEnvRequest{Freight: "fr-mixed"})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.Code, resp.Body.String())
	}
	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	ov := app.Spec.EnvironmentDefaults["staging"]
	if ov.PinnedFrom != "rollback" || ov.PinnedImageTag != "" {
		t.Errorf("hold = (%q, %q), want (rollback, \"\")", ov.PinnedFrom, ov.PinnedImageTag)
	}
}

// Rolling back to the currently-running freight or to unknown freight is refused
// with 422, and nothing is promoted or held.
func TestRollbackAppEnv_RefusesCurrentAndUnknown(t *testing.T) {
	for _, freight := range []string{"fr-current", "fr-nope"} {
		pub := &recordingPublisher{}
		historian := &fakeFreightHistorian{history: rollbackHistory()}
		mux, ah, store := newTestAppPromoteMuxWithGate(testProject, pub, historian, nil)

		store.addApp(promoteTestApp(testProject))
		seedFullPromotionChain(store, testProject)

		resp := postRollbackJSON(mux, sessionCookieFor(ah, "alice", "org_admin"), testProject, "my-app", "staging",
			rollbackAppEnvRequest{Freight: freight})
		if resp.Code != http.StatusUnprocessableEntity {
			t.Fatalf("freight %q: expected 422, got %d: %s", freight, resp.Code, resp.Body.String())
		}
		if len(historian.promoted) != 0 {
			t.Errorf("freight %q: nothing should be promoted, got %v", freight, historian.promoted)
		}
		app, _ := store.GetApp(context.Background(), testProject, "my-app")
		if app.Spec.EnvironmentDefaults["staging"].PinnedFrom != "" {
			t.Errorf("freight %q: no hold should be placed", freight)
		}
	}
}

// The candidates endpoint lists the stage's freight history; a promoter without
// the historian capability reports available=false instead of erroring.
func TestRollbackCandidates(t *testing.T) {
	pub := &recordingPublisher{}
	historian := &fakeFreightHistorian{history: rollbackHistory()}
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, pub, historian, nil)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)

	url := "/api/v1/projects/" + testProject + "/apps/my-app/environments/staging/rollback-candidates"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body RollbackCandidatesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Available || len(body.Candidates) != 3 {
		t.Fatalf("expected available with 3 candidates, got %+v", body)
	}
	if !body.Candidates[0].Current || body.Candidates[0].Freight != "fr-current" {
		t.Errorf("first candidate should be the current freight, got %+v", body.Candidates[0])
	}

	// A plain promoter (no historian capability): available=false, 200.
	mux2, ah2, store2 := newTestAppPromoteMuxWithGate(testProject, pub, &recordingPromoter{}, nil)
	store2.addApp(promoteTestApp(testProject))
	req2 := httptest.NewRequest(http.MethodGet, url, nil)
	req2.AddCookie(sessionCookieFor(ah2, "alice", "org_admin"))
	rec2 := httptest.NewRecorder()
	mux2.ServeHTTP(rec2, req2)
	var body2 RollbackCandidatesResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &body2); err != nil || rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 parseable, got %d: %v", rec2.Code, err)
	}
	if body2.Available {
		t.Error("expected available=false without historian capability")
	}
}
