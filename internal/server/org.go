package server

import (
	"net/http"
	"sort"

	"github.com/suparcloud/suparship/internal/rbac"
)

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
	Name string `json:"name"`
}

// ProjectsResponse is the JSON body for GET /api/v1/projects.
type ProjectsResponse struct {
	Projects []ProjectDTO `json:"projects"`
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
	org, err := rh.orgProvider.GetOrg(r.Context())
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
	org, err := rh.orgProvider.GetOrg(r.Context())
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
	org, err := rh.orgProvider.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
		return
	}

	names := collectProjects(org.RoleBindings)
	projects := make([]ProjectDTO, len(names))
	for i, n := range names {
		projects[i] = ProjectDTO{Name: n}
	}

	writeJSON(w, http.StatusOK, ProjectsResponse{Projects: projects})
}

func (rh *rbacHandler) handleGetProjectRBAC(w http.ResponseWriter, r *http.Request) {
	org, err := rh.orgProvider.GetOrg(r.Context())
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
