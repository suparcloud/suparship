package server

// project_env_handler.go — project-level environment CRUD
//
// Endpoints:
//
//	GET    /api/v1/projects/{project}/environments
//	POST   /api/v1/projects/{project}/environments
//	PUT    /api/v1/projects/{project}/environments/{env}
//	DELETE /api/v1/projects/{project}/environments/{env}
//
// All endpoints require at minimum Viewer role; write operations require
// ProjectAdmin. They are registered via rbacHandler.registerRoutes and rely
// on the rbacHandler.projectStore field being non-nil.

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/runtime"
)

// ── GET /api/v1/projects/{project}/environments ───────────────────────────────

func (rh *rbacHandler) handleListProjectEnvironments(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	if rh.projectStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"environments": []any{}})
		return
	}
	proj, err := rh.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
		return
	}
	envs := projectEnvsToDTO(proj)
	writeJSON(w, http.StatusOK, map[string]any{"environments": envs})
}

// ── POST /api/v1/projects/{project}/environments ──────────────────────────────

type upsertEnvRequest struct {
	Name             string `json:"name"`
	DisplayName      string `json:"displayName,omitempty"`
	Order            int    `json:"order"`
	ClusterRef       string `json:"clusterRef,omitempty"`
	BaseDomain       string `json:"baseDomain,omitempty"`
	NamespacePattern string `json:"namespacePattern,omitempty"`
}

func (rh *rbacHandler) handleCreateProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	if rh.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "project store not configured"})
		return
	}

	var req upsertEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "name is required"})
		return
	}
	if err := domain.ValidateNamespacePattern(req.NamespacePattern); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	proj, err := rh.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
		return
	}

	// Reject duplicates.
	for _, e := range proj.Spec.Environments {
		if e.Name == req.Name {
			writeJSON(w, http.StatusConflict, errorResponse{Error: "environment already exists"})
			return
		}
	}

	order := req.Order
	if order == 0 {
		order = len(proj.Spec.Environments) + 1
	}

	proj.Spec.Environments = append(proj.Spec.Environments, project.Environment{
		Name:             req.Name,
		DisplayName:      req.DisplayName,
		Order:            order,
		ClusterRef:       req.ClusterRef,
		BaseDomain:       req.BaseDomain,
		NamespacePattern: req.NamespacePattern,
	})
	sortEnvs(proj.Spec.Environments)

	if err := rh.projectStore.Save(r.Context(), proj); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save project"})
		return
	}

	writeJSON(w, http.StatusCreated, envToDTO(projectName, project.Environment{
		Name:             req.Name,
		DisplayName:      req.DisplayName,
		Order:            order,
		ClusterRef:       req.ClusterRef,
		BaseDomain:       req.BaseDomain,
		NamespacePattern: req.NamespacePattern,
	}))
}

// ── PUT /api/v1/projects/{project}/environments/{env} ─────────────────────────

func (rh *rbacHandler) handleUpdateProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	envName := r.PathValue("env")
	if rh.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "project store not configured"})
		return
	}

	var req upsertEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if err := domain.ValidateNamespacePattern(req.NamespacePattern); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	proj, err := rh.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
		return
	}

	found := false
	for i, e := range proj.Spec.Environments {
		if e.Name == envName {
			proj.Spec.Environments[i].DisplayName = req.DisplayName
			proj.Spec.Environments[i].ClusterRef = req.ClusterRef
			proj.Spec.Environments[i].BaseDomain = req.BaseDomain
			proj.Spec.Environments[i].NamespacePattern = req.NamespacePattern
			if req.Order > 0 {
				proj.Spec.Environments[i].Order = req.Order
			}
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "environment not found"})
		return
	}
	sortEnvs(proj.Spec.Environments)

	if err := rh.projectStore.Save(r.Context(), proj); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save project"})
		return
	}

	for _, e := range proj.Spec.Environments {
		if e.Name == envName {
			writeJSON(w, http.StatusOK, envToDTO(projectName, e))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── DELETE /api/v1/projects/{project}/environments/{env} ─────────────────────

func (rh *rbacHandler) handleDeleteProjectEnvironment(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	envName := r.PathValue("env")
	if rh.projectStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "project store not configured"})
		return
	}

	proj, err := rh.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project not found"})
		return
	}

	filtered := proj.Spec.Environments[:0]
	found := false
	for _, e := range proj.Spec.Environments {
		if e.Name == envName {
			found = true
		} else {
			filtered = append(filtered, e)
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "environment not found"})
		return
	}

	proj.Spec.Environments = filtered
	if err := rh.projectStore.Save(r.Context(), proj); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save project"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func envToDTO(projectName string, e project.Environment) EnvironmentDTO {
	return EnvironmentDTO{
		Name:             e.Name,
		DisplayName:      e.DisplayName,
		Project:          projectName,
		Namespace:        runtime.Namespace(projectName, e.Name),
		Order:            e.Order,
		ClusterRef:       e.ClusterRef,
		BaseDomain:       e.BaseDomain,
		NamespacePattern: e.NamespacePattern,
	}
}

func projectEnvsToDTO(proj *project.Project) []EnvironmentDTO {
	envs := make([]EnvironmentDTO, len(proj.Spec.Environments))
	for i, e := range proj.Spec.Environments {
		envs[i] = envToDTO(proj.Metadata.Name, e)
	}
	return envs
}

func sortEnvs(envs []project.Environment) {
	sort.Slice(envs, func(i, j int) bool {
		if envs[i].Order != envs[j].Order {
			return envs[i].Order < envs[j].Order
		}
		return envs[i].Name < envs[j].Name
	})
}
