package server

import (
	"context"
	"fmt"
	"github.com/suparcloud/suparship/internal/kube"
	"k8s.io/apimachinery/pkg/util/validation"
	"net/http"
)

type verifiedReleaseDetailsReader interface {
	VerifiedReleaseDetails(context.Context, string, string, string, string) ([]kube.VerifiedReleaseCandidate, error)
}

type verifiedReleaseReader interface {
	VerifiedReleaseCandidates(context.Context, string, string, string, string) ([]string, error)
}

func (ah *appHandler) handleVerifiedReleaseCandidates(w http.ResponseWriter, r *http.Request) {
	project, appName := r.PathValue("project"), r.PathValue("app")
	reader, ok := ah.kargoPromoter.(verifiedReleaseReader)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "releases": []string{}})
		return
	}
	app, err := ah.appStore.GetApp(r.Context(), project, appName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "app not found"})
		return
	}
	if app.Spec.IsDirect() {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "direct-delivery apps do not support promotion"})
		return
	}
	target, err := ah.appStore.GetAppEnvironment(r.Context(), project, appName, r.URL.Query().Get("targetEnvironment"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "target environment not found"})
		return
	}
	source, err := ah.findPromotionSource(r.Context(), project, appName, target)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if detailReader, ok := ah.kargoPromoter.(verifiedReleaseDetailsReader); ok {
		records, err := detailReader.VerifiedReleaseDetails(r.Context(), project, appName, source.EnvName, target.EnvName)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "Could not read release verification. Retry when the pipeline is available."})
			return
		}
		names := []string{}
		for _, record := range records {
			names = append(names, record.Name)
		}
		writeJSON(w, http.StatusOK, map[string]any{"available": true, "releases": names, "details": records})
		return
	}
	releases, err := reader.VerifiedReleaseCandidates(r.Context(), project, appName, source.EnvName, target.EnvName)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "could not read verified releases"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "releases": releases})
}

func validateVerificationConfig(cd *CDConfigDTO) error {
	if cd == nil {
		return nil
	}
	for env, names := range cd.Verification {
		if len(validation.IsDNS1123Label(env)) > 0 {
			return fmt.Errorf("invalid verification environment %q", env)
		}
		seen := map[string]bool{}
		for _, name := range names {
			if len(validation.IsDNS1123Subdomain(name)) > 0 {
				return fmt.Errorf("invalid AnalysisTemplate name %q", name)
			}
			if seen[name] {
				return fmt.Errorf("duplicate AnalysisTemplate %q in %s", name, env)
			}
			seen[name] = true
		}
	}
	return nil
}
