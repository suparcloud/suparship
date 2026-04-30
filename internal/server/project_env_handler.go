package server

// project_env_handler.go — project-level environment CRUD
//
// Endpoints:
//
//	GET    /api/v1/projects/{project}/environments   — merged (org defaults + project overrides)
//	POST   /api/v1/projects/{project}/environments   — add project-specific env or store override
//	PUT    /api/v1/projects/{project}/environments/{env}  — update project override
//	DELETE /api/v1/projects/{project}/environments/{env}  — remove override (org env) or env (project-only)
//
// All endpoints require at minimum Viewer role; write operations require
// ProjectAdmin. Registered via rbacHandler.registerRoutes.
//
// GET returns environments merged from org defaults + project overrides, each
// annotated with an "origin" field:
//   "org"      — fully inherited from org, no project customisation
//   "override" — org environment with project-level overrides applied
//   "project"  — project-specific environment not in org defaults

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
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

	org, _ := rh.orgStore.GetOrg(r.Context())
	var orgEnvs []rbac.OrgEnvironment
	if org != nil {
		orgEnvs = org.Environments
	}
	merged := rbac.MergeEnvironments(orgEnvs, proj.Spec.Environments)
	dtos := make([]EnvironmentDTO, len(merged))
	for i, m := range merged {
		dtos[i] = envToDTO(proj.Metadata.Name, m.Environment)
		dtos[i].Origin = m.Origin
	}
	writeJSON(w, http.StatusOK, map[string]any{"environments": dtos})
}

// ── POST /api/v1/projects/{project}/environments ──────────────────────────────

type upsertEnvRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Order       int    `json:"order"`
	// ClusterRefs when non-nil replaces the project-level cluster set
	// for this env. Nil means inherit from the org-level entry.
	ClusterRefs *[]string `json:"clusterRefs,omitempty"`
	// ActiveClusterRef when non-nil replaces the active deploy cluster
	// for this env override. Must be a member of the resolved ClusterRefs.
	ActiveClusterRef *string `json:"activeClusterRef,omitempty"`
	BaseDomain       string  `json:"baseDomain,omitempty"`
	NamespacePattern string  `json:"namespacePattern,omitempty"`
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

	newEnv := project.Environment{
		Name:             req.Name,
		DisplayName:      req.DisplayName,
		Order:            order,
		BaseDomain:       req.BaseDomain,
		NamespacePattern: req.NamespacePattern,
	}
	if req.ClusterRefs != nil {
		newEnv.ClusterRefs = *req.ClusterRefs
	}
	if req.ActiveClusterRef != nil {
		newEnv.ActiveClusterRef = *req.ActiveClusterRef
	}
	if err := validateProjectEnvActiveInRefs(newEnv); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}
	proj.Spec.Environments = append(proj.Spec.Environments, newEnv)
	sortEnvs(proj.Spec.Environments)

	if err := rh.projectStore.Save(r.Context(), proj); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save project"})
		return
	}

	writeJSON(w, http.StatusCreated, envToDTO(projectName, newEnv))
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

	// Check if it's an org env (inherited or already overriding).
	org, _ := rh.orgStore.GetOrg(r.Context())
	isOrgEnv := false
	if org != nil {
		for _, e := range org.Environments {
			if e.Name == envName {
				isOrgEnv = true
				break
			}
		}
	}

	found := false
	for i, e := range proj.Spec.Environments {
		if e.Name == envName {
			if req.DisplayName != "" {
				proj.Spec.Environments[i].DisplayName = req.DisplayName
			}
			if req.ClusterRefs != nil {
				proj.Spec.Environments[i].ClusterRefs = *req.ClusterRefs
			}
			if req.ActiveClusterRef != nil {
				proj.Spec.Environments[i].ActiveClusterRef = *req.ActiveClusterRef
			}
			if req.BaseDomain != "" {
				proj.Spec.Environments[i].BaseDomain = req.BaseDomain
			}
			if req.NamespacePattern != "" {
				proj.Spec.Environments[i].NamespacePattern = req.NamespacePattern
			}
			if req.Order > 0 {
				proj.Spec.Environments[i].Order = req.Order
			}
			if err := validateProjectEnvActiveInRefs(proj.Spec.Environments[i]); err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
				return
			}
			found = true
			break
		}
	}

	if !found {
		if !isOrgEnv {
			// Not in org and not in project — nothing to update.
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "environment not found"})
			return
		}
		// First override for an inherited org env: create a sparse override entry.
		override := project.Environment{Name: envName}
		if req.DisplayName != "" {
			override.DisplayName = req.DisplayName
		}
		if req.ClusterRefs != nil {
			override.ClusterRefs = *req.ClusterRefs
		}
		if req.ActiveClusterRef != nil {
			override.ActiveClusterRef = *req.ActiveClusterRef
		}
		if req.BaseDomain != "" {
			override.BaseDomain = req.BaseDomain
		}
		if req.NamespacePattern != "" {
			override.NamespacePattern = req.NamespacePattern
		}
		if req.Order > 0 {
			override.Order = req.Order
		}
		if err := validateProjectEnvActiveInRefs(override); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
			return
		}
		proj.Spec.Environments = append(proj.Spec.Environments, override)
	}

	sortEnvs(proj.Spec.Environments)

	if err := rh.projectStore.Save(r.Context(), proj); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save project"})
		return
	}

	// Return merged effective environment.
	var orgEnvs []rbac.OrgEnvironment
	if org != nil {
		orgEnvs = org.Environments
	}
	merged := rbac.MergeEnvironments(orgEnvs, proj.Spec.Environments)
	for _, m := range merged {
		if m.Name == envName {
			dto := envToDTO(projectName, m.Environment)
			dto.Origin = m.Origin
			writeJSON(w, http.StatusOK, dto)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── DELETE /api/v1/projects/{project}/environments/{env} ─────────────────────
//
// Behaviour depends on origin:
//   - "project" env: fully removed.
//   - "org" or "override" env: project override is cleared; env remains
//     visible as inherited (HTTP 200 with the resulting inherited DTO).

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

	// Determine if the env is in org defaults.
	org, _ := rh.orgStore.GetOrg(r.Context())
	isOrgEnv := false
	var orgEnv rbac.OrgEnvironment
	if org != nil {
		for _, e := range org.Environments {
			if e.Name == envName {
				isOrgEnv = true
				orgEnv = e
				break
			}
		}
	}

	// Remove from project overrides.
	filtered := proj.Spec.Environments[:0]
	found := false
	for _, e := range proj.Spec.Environments {
		if e.Name == envName {
			found = true
		} else {
			filtered = append(filtered, e)
		}
	}

	if !found && !isOrgEnv {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "environment not found"})
		return
	}

	proj.Spec.Environments = filtered
	if err := rh.projectStore.Save(r.Context(), proj); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save project"})
		return
	}

	if isOrgEnv {
		// Return the now-inherited org env so the UI can update without a refetch.
		writeJSON(w, http.StatusOK, EnvironmentDTO{
			Name:             orgEnv.Name,
			DisplayName:      orgEnv.DisplayName,
			Project:          projectName,
			Namespace:        runtime.Namespace(projectName, orgEnv.Name),
			Order:            orgEnv.Order,
			ClusterRefs:      orgEnv.ClusterRefs,
			ActiveClusterRef: orgEnv.ActiveClusterRef,
			BaseDomain:       orgEnv.BaseDomain,
			NamespacePattern: orgEnv.NamespacePattern,
			Origin:           rbac.OriginOrg,
		})
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
		ClusterRefs:      e.ClusterRefs,
		ActiveClusterRef: e.ActiveClusterRef,
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

// validateProjectEnvActiveInRefs mirrors validateActiveInRefs for project-
// level overrides. ActiveClusterRef must be a member of ClusterRefs (or
// empty, in which case EffectiveClusterRef falls back to ClusterRefs[0]).
func validateProjectEnvActiveInRefs(e project.Environment) error {
	if e.ActiveClusterRef == "" {
		return nil
	}
	for _, c := range e.ClusterRefs {
		if c == e.ActiveClusterRef {
			return nil
		}
	}
	return fmt.Errorf("activeClusterRef %q must be present in clusterRefs %v", e.ActiveClusterRef, e.ClusterRefs)
}
