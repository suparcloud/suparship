package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/suparcloud/suparship/internal/license"
	"github.com/suparcloud/suparship/internal/version"
)

// MetaResponse is the JSON body returned by GET /api/v1/meta.
type MetaResponse struct {
	App       string `json:"app"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	// Edition is the active product edition ("community" or "enterprise").
	Edition string `json:"edition"`
	// Features lists entitled enterprise features so the UI can conditionally
	// render enterprise panels. Empty for the community edition.
	Features []string `json:"features"`
}

// readyzCheckResult is one entry in the GET /readyz response body.
type readyzCheckResult struct {
	Name    string `json:"name"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message,omitempty"`
}

// readyzResponse is the JSON body returned by GET /readyz.
type readyzResponse struct {
	Ready  bool                `json:"ready"`
	Checks []readyzCheckResult `json:"checks"`
}

func registerRoutes(mux *http.ServeMux, probers []ReadinessProber, lic license.Validator) {
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.Handle("GET /readyz", buildReadyzHandler(probers))
	mux.Handle("GET /api/v1/meta", buildMetaHandler(lic))
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// buildReadyzHandler returns an http.Handler for GET /readyz.
//
// When no probers are configured the handler returns 200 OK immediately (same
// as the previous stub behaviour). When probers are supplied each is run with a
// per-check deadline of 3 s. If all pass the response is 200; on any failure it
// is 503 Service Unavailable.
//
// The response body is always JSON so that orchestration tools (Kubernetes
// readiness probes with httpGet) can parse individual check results when needed.
func buildReadyzHandler(probers []ReadinessProber) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(probers) == 0 {
			writeJSON(w, http.StatusOK, readyzResponse{Ready: true, Checks: []readyzCheckResult{}})
			return
		}

		results := make([]readyzCheckResult, 0, len(probers))
		allHealthy := true

		for _, p := range probers {
			checkCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			err := p.Check(checkCtx)
			cancel()

			res := readyzCheckResult{Name: p.Name, Healthy: err == nil}
			if err != nil {
				res.Message = err.Error()
				allHealthy = false
			}
			results = append(results, res)
		}

		status := http.StatusOK
		if !allHealthy {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, readyzResponse{Ready: allHealthy, Checks: results})
	})
}

// buildMetaHandler returns the GET /api/v1/meta handler. The active edition and
// entitled enterprise features are read from lic (a nil validator is treated as
// the community edition).
func buildMetaHandler(lic license.Validator) http.HandlerFunc {
	v := license.Resolve(lic)
	return func(w http.ResponseWriter, _ *http.Request) {
		feats := v.Features()
		names := make([]string, 0, len(feats))
		for _, f := range feats {
			names = append(names, string(f))
		}

		resp := MetaResponse{
			App:       "suparship",
			Version:   version.Version,
			Commit:    version.Commit,
			BuildDate: version.Date,
			Edition:   v.Edition(),
			Features:  names,
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
	}
}
