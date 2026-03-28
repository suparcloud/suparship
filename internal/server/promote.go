package server

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/project"
)

// --- Promotion DTOs ---

// PromoteRequest is the JSON body for POST .../promote.
type PromoteRequest struct {
	TargetEnvironment string `json:"targetEnvironment"`
}

// PromoteResponse is the JSON body returned on successful promotion.
type PromoteResponse struct {
	Project     string `json:"project"`
	Service     string `json:"service"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Namespace   string `json:"namespace"`
	Message     string `json:"message"`
}

// --- Handler ---

// promoteHandler handles service promotion between environments.
// For MVP, it validates the request and returns a structured result.
// When Kargo is available, this will trigger a Kargo promotion.
type promoteHandler struct {
	projectStore project.Store
}

func newPromoteHandler(store project.Store) *promoteHandler {
	return &promoteHandler{projectStore: store}
}

func (ph *promoteHandler) handlePromote(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	serviceName := r.PathValue("service")

	var req PromoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.TargetEnvironment == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "targetEnvironment is required"})
		return
	}

	proj, err := ph.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "project \"" + projectName + "\" not found",
		})
		return
	}

	var serviceFound bool
	for _, svc := range proj.Spec.Services {
		if svc.Name == serviceName {
			serviceFound = true
			break
		}
	}
	if !serviceFound {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "service \"" + serviceName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	envs := sortedEnvironments(proj.Spec.Environments)
	targetIdx := -1
	for i, env := range envs {
		if env.Name == req.TargetEnvironment {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "environment \"" + req.TargetEnvironment + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	if targetIdx == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "cannot promote to the lowest environment \"" + req.TargetEnvironment + "\"",
		})
		return
	}

	source := envs[targetIdx-1].Name
	targetNS := projectName + "-" + req.TargetEnvironment

	writeJSON(w, http.StatusOK, PromoteResponse{
		Project:     projectName,
		Service:     serviceName,
		Source:      source,
		Destination: req.TargetEnvironment,
		Namespace:   targetNS,
		Message:     "Promotion of " + serviceName + " from " + source + " to " + req.TargetEnvironment + " initiated",
	})
}

// sortedEnvironments returns environments sorted by Order (ascending).
func sortedEnvironments(envs []project.Environment) []project.Environment {
	sorted := make([]project.Environment, len(envs))
	copy(sorted, envs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Order < sorted[j].Order
	})
	return sorted
}
