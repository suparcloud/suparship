package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// When the Kargo / ArgoCD readers aren't wired (local/dev installs), the CD
// status endpoints degrade gracefully: HTTP 200 with available=false and empty
// data, so the polling UI shows "unavailable" instead of erroring on a repeated
// 501. These call the handlers directly with no readers configured.

func TestKargoStages_UnavailableWhenReaderUnwired(t *testing.T) {
	ah := &appHandler{} // no kargoPipelineReader
	req := httptest.NewRequest("GET", "/api/v1/projects/demo/apps/web/kargo/stages", nil)
	req.SetPathValue("project", "demo")
	req.SetPathValue("app", "web")
	rec := httptest.NewRecorder()
	ah.handleGetKargoStages(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp KargoAppPipelineResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Available {
		t.Errorf("available = true, want false when no pipeline reader is wired")
	}
	if len(resp.Stages) != 0 {
		t.Errorf("stages = %+v, want empty", resp.Stages)
	}
}

func TestKargoPromotionStatus_UnavailableWhenReaderUnwired(t *testing.T) {
	ah := &appHandler{} // no kargoStatusReader
	req := httptest.NewRequest("GET", "/api/v1/projects/demo/apps/web/promotions/p1", nil)
	req.SetPathValue("project", "demo")
	req.SetPathValue("app", "web")
	req.SetPathValue("name", "p1")
	rec := httptest.NewRecorder()
	ah.handleGetKargoPromotion(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp KargoPromotionStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Available {
		t.Errorf("available = true, want false when no status reader is wired")
	}
}

func TestDeploymentHistory_UnavailableWhenReaderUnwired(t *testing.T) {
	ah := &appHandler{} // no deploymentHistoryReader
	req := httptest.NewRequest("GET", "/api/v1/projects/demo/apps/web/environments/staging/history", nil)
	req.SetPathValue("project", "demo")
	req.SetPathValue("app", "web")
	req.SetPathValue("env", "staging")
	rec := httptest.NewRecorder()
	ah.handleGetAppDeploymentHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp AppDeploymentHistoryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Available {
		t.Errorf("available = true, want false when no history reader is wired")
	}
	if len(resp.History) != 0 {
		t.Errorf("history = %+v, want empty", resp.History)
	}
}
