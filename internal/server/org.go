package server

import (
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/runtime"
)

// ── org.go — read handlers for GET /api/v1/org, /teams, /projects, /projects/{project}
// Write operations (org environment CRUD) are in org_env_handler.go.

// --- API DTO types ---

// OrgResponse is the JSON body for GET /api/v1/org.
type OrgResponse struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	CreatedAt   string `json:"createdAt,omitempty"`
}

// TeamDTO represents a team in API responses.
type TeamDTO struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Members     []string `json:"members"`
}

// TeamsResponse is the JSON body for GET /api/v1/teams.
type TeamsResponse struct {
	Teams []TeamDTO `json:"teams"`
}

// ProjectDTO represents a project in API responses.
type ProjectDTO struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Description string `json:"description,omitempty"`
}

// ProjectsResponse is the JSON body for GET /api/v1/projects.
type ProjectsResponse struct {
	Projects []ProjectDTO `json:"projects"`
}

// ProjectDetailResponse is the JSON body for GET /api/v1/projects/{project}.
type ProjectDetailResponse struct {
	Name         string           `json:"name"`
	DisplayName  string           `json:"displayName,omitempty"`
	Description  string           `json:"description,omitempty"`
	Environments []EnvironmentDTO `json:"environments"`
	Services     []string         `json:"services"`
}

// RoleBindingDTO represents a role binding in API responses.
type RoleBindingDTO struct {
	Project string `json:"project"`
	Team    string `json:"team"`
	Role    string `json:"role"`
}

// ProjectRBACResponse is the JSON body for GET /api/v1/projects/{project}/rbac.
type ProjectRBACResponse struct {
	Project      string           `json:"project"`
	RoleBindings []RoleBindingDTO `json:"roleBindings"`
}

// --- Handlers ---

func (rh *rbacHandler) handleGetOrg(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
		return
	}

	writeJSON(w, http.StatusOK, OrgResponse{
		Name:        org.Name,
		DisplayName: org.DisplayName,
		CreatedAt:   org.CreatedAt,
	})
}

func (rh *rbacHandler) handleGetTeams(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
		return
	}

	teams := make([]TeamDTO, len(org.Teams))
	for i, t := range org.Teams {
		members := t.Members
		if members == nil {
			members = []string{}
		}
		teams[i] = TeamDTO{
			Name:        t.Name,
			DisplayName: t.DisplayName,
			Members:     members,
		}
	}

	writeJSON(w, http.StatusOK, TeamsResponse{Teams: teams})
}

func (rh *rbacHandler) handleGetProjects(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
		return
	}

	seen := make(map[string]bool)
	for _, name := range collectProjects(org.RoleBindings) {
		seen[name] = true
	}

	// stored holds project metadata (display name, description) keyed by project name.
	stored := make(map[string]*project.Project)
	if rh.projectStore != nil {
		list, err := rh.projectStore.List(r.Context())
		if err == nil {
			for _, p := range list {
				seen[p.Metadata.Name] = true
				stored[p.Metadata.Name] = p
			}
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)

	projects := make([]ProjectDTO, len(names))
	for i, n := range names {
		dto := ProjectDTO{Name: n}
		if p, ok := stored[n]; ok {
			dto.DisplayName = p.Spec.DisplayName
			dto.Description = p.Spec.Description
		}
		projects[i] = dto
	}

	writeJSON(w, http.StatusOK, ProjectsResponse{Projects: projects})
}

// handleGetProject returns full project detail for GET /api/v1/projects/{project}.
// When a projectStore is wired and the project exists in it, the response
// includes display name, description, environments, and service names.
// When the project is unknown to the store it returns 404.
// When no store is wired (e.g. unit tests that only check RBAC), a minimal
// 200 response is returned so RBAC tests remain valid.
func (rh *rbacHandler) handleGetProject(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")

	if rh.projectStore != nil {
		proj, err := rh.projectStore.Get(r.Context(), projectName)
		if err != nil {
			writeJSON(w, http.StatusNotFound, errorResponse{
				Error: "project \"" + projectName + "\" not found",
			})
			return
		}

		// Resolve effective environments by merging org defaults with project overrides.
		org, _ := rh.orgStore.GetOrg(r.Context())
		var orgEnvs []rbac.OrgEnvironment
		if org != nil {
			orgEnvs = org.Environments
		}
		merged := rbac.MergeEnvironments(orgEnvs, proj.Spec.Environments)
		envs := make([]EnvironmentDTO, len(merged))
		for i, m := range merged {
			envs[i] = EnvironmentDTO{
				Name:             m.Name,
				DisplayName:      m.DisplayName,
				Project:          proj.Metadata.Name,
				Namespace:        runtime.Namespace(proj.Metadata.Name, m.Name),
				Order:            m.Order,
				ClusterRefs:      m.ClusterRefs,
				ActiveClusterRef: m.ActiveClusterRef,
				BaseDomain:       m.BaseDomain,
				NamespacePattern: m.NamespacePattern,
				Origin:           m.Origin,
			}
		}

		svcNames := make([]string, len(proj.Spec.Services))
		for i, svc := range proj.Spec.Services {
			svcNames[i] = svc.Name
		}

		writeJSON(w, http.StatusOK, ProjectDetailResponse{
			Name:         proj.Metadata.Name,
			DisplayName:  proj.Spec.DisplayName,
			Description:  proj.Spec.Description,
			Environments: envs,
			Services:     svcNames,
		})
		return
	}

	// No project store wired; project access already verified by RBAC middleware.
	// Return a minimal response so callers always get valid JSON.
	writeJSON(w, http.StatusOK, ProjectDetailResponse{
		Name:         projectName,
		Environments: []EnvironmentDTO{},
		Services:     []string{},
	})
}

func (rh *rbacHandler) handleGetProjectRBAC(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgStore.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
		return
	}

	project := r.PathValue("project")
	matching := bindingsForProject(org.RoleBindings, project)
	dtos := make([]RoleBindingDTO, len(matching))
	for i, rb := range matching {
		dtos[i] = RoleBindingDTO{
			Project: string(rb.Project),
			Team:    rb.Team,
			Role:    string(rb.Role),
		}
	}

	writeJSON(w, http.StatusOK, ProjectRBACResponse{
		Project:      project,
		RoleBindings: dtos,
	})
}

// --- Helpers ---

// collectProjects returns a sorted, deduplicated list of project names from
// role bindings, excluding the wildcard "*".
func collectProjects(bindings []rbac.RoleBinding) []string {
	seen := make(map[string]bool)
	var projects []string
	for _, rb := range bindings {
		if rb.Project == "*" {
			continue
		}
		if !seen[rb.Project] {
			seen[rb.Project] = true
			projects = append(projects, rb.Project)
		}
	}
	sort.Strings(projects)
	return projects
}

// bindingsForProject returns all role bindings that apply to the given
// project, including wildcard ("*") bindings.
func bindingsForProject(bindings []rbac.RoleBinding, project string) []rbac.RoleBinding {
	var result []rbac.RoleBinding
	for _, rb := range bindings {
		if rb.Project == project || rb.Project == "*" {
			result = append(result, rb)
		}
	}
	return result
}
