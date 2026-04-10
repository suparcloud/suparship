// Package server — this file contains the legacy service-oriented inventory
// endpoints:
//
//	GET /api/v1/environments
//	GET /api/v1/projects/{project}/services
//	GET /api/v1/projects/{project}/services/{service}
//
// # Deprecation notice
//
// These endpoints are retained for backwards compatibility. The primary
// app-oriented equivalents are:
//
//	GET /api/v1/projects/{project}/apps
//	GET /api/v1/projects/{project}/apps/{app}
//	GET /api/v1/projects/{project}/apps/{app}/environments
//
// New integrations should use the app endpoints. Existing callers of the
// service endpoints continue to work without modification and will receive a
// Deprecation response header as a migration signal.
// See docs/migration-app-model.md for the full transition guide.
package server

import (
	"net/http"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/runtime"
)

// --- Inventory DTOs ---

// EnvironmentDTO describes a project environment.
// Used by GET /api/v1/environments (legacy) and GET /api/v1/projects/{project}/apps/{app}/environments.
type EnvironmentDTO struct {
	Name             string `json:"name"`
	DisplayName      string `json:"displayName,omitempty"`
	Project          string `json:"project"`
	Namespace        string `json:"namespace"`
	Order            int    `json:"order"`
	ClusterRef       string `json:"clusterRef,omitempty"`
	BaseDomain       string `json:"baseDomain,omitempty"`
	NamespacePattern string `json:"namespacePattern,omitempty"`
	// Origin describes where this environment definition comes from:
	//   "org"      — fully inherited from org defaults, no project customisation
	//   "override" — org environment with project-level field overrides applied
	//   "project"  — project-specific environment not present in org defaults
	// Empty string means origin was not resolved (legacy/inventory endpoints).
	Origin string `json:"origin,omitempty"`
}

// EnvironmentsResponse is the JSON body for GET /api/v1/environments.
//
// Deprecated: This aggregate endpoint is legacy. Environment state is now
// surfaced per-app via GET /api/v1/projects/{project}/apps/{app}/environments.
// See docs/migration-app-model.md.
type EnvironmentsResponse struct {
	Environments []EnvironmentDTO `json:"environments"`
}

// ServiceRuntimeDTO is the merged desired+runtime view of a service in the
// list endpoint.
//
// Deprecated: Use AppSummaryDTO (from GET /api/v1/projects/{project}/apps)
// for the app-oriented equivalent. See docs/migration-app-model.md.
type ServiceRuntimeDTO struct {
	Name     string         `json:"name"`
	Template templateRefDTO `json:"template"`
	Runtime  RuntimeDTO     `json:"runtime"`
}

// RuntimeDTO is the runtime portion of a legacy service response.
// The app-oriented equivalent is AppStatusSummaryDTO.
type RuntimeDTO struct {
	Status       string   `json:"status"`
	Image        string   `json:"image,omitempty"`
	Replicas     int32    `json:"replicas"`
	Available    int32    `json:"available"`
	IngressURLs  []string `json:"ingressUrls"`
	Namespace    string   `json:"namespace"`
	LastDeployed string   `json:"lastDeployed,omitempty"`
}

// ProjectServicesResponse is the JSON body for the legacy
// GET /api/v1/projects/{project}/services endpoint.
//
// Deprecated: Use AppListResponse (GET /api/v1/projects/{project}/apps).
// See docs/migration-app-model.md.
type ProjectServicesResponse struct {
	Project  string              `json:"project"`
	Services []ServiceRuntimeDTO `json:"services"`
}

// ServiceDetailResponse is the JSON body for the legacy
// GET /api/v1/projects/{project}/services/{service} endpoint.
//
// Deprecated: Use AppDetailResponse (GET /api/v1/projects/{project}/apps/{app}).
// See docs/migration-app-model.md.
type ServiceDetailResponse struct {
	Name         string             `json:"name"`
	Project      string             `json:"project"`
	Template     templateRefDTO     `json:"template"`
	Values       map[string]any     `json:"values"`
	SecretRefs   []secretRefRequest `json:"secretRefs"`
	Environments []ServiceEnvDTO    `json:"environments"`
}

// ServiceEnvDTO is the per-environment runtime state in the legacy service
// detail response.
//
// Deprecated: Use AppEnvironmentSummaryDTO (from the app environments endpoint).
// See docs/migration-app-model.md.
type ServiceEnvDTO struct {
	Environment string     `json:"environment"`
	Namespace   string     `json:"namespace"`
	Runtime     RuntimeDTO `json:"runtime"`
}

// --- Inventory handler ---

// inventoryHandler serves the legacy service inventory endpoints. It reads
// desired config from the project store and runtime state from the runtime
// provider.
//
// Deprecated: inventoryHandler backs the service-oriented inventory routes.
// The app-oriented equivalents are handled by appHandler.
// See docs/migration-app-model.md.
type inventoryHandler struct {
	projectStore    project.Store
	runtimeProvider runtime.Provider  // may be nil
	orgProvider     rbac.OrgProvider  // optional; used as fallback for org-level environments
}

func newInventoryHandler(store project.Store, rp runtime.Provider) *inventoryHandler {
	return &inventoryHandler{projectStore: store, runtimeProvider: rp}
}

// handleListEnvironments returns all environments across all projects.
//
// Deprecated: Registered at the legacy GET /api/v1/environments route.
// Environment state is now surfaced per-app via the app environments endpoint.
// See docs/migration-app-model.md.
func (ih *inventoryHandler) handleListEnvironments(w http.ResponseWriter, r *http.Request) {
	projects, err := ih.projectStore.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list projects"})
		return
	}

	// Collect project-level environments (may be empty if projects inherit from org).
	var envs []EnvironmentDTO
	for _, p := range projects {
		for _, e := range p.Spec.Environments {
			envs = append(envs, EnvironmentDTO{
				Name:        e.Name,
				DisplayName: e.DisplayName,
				Project:     p.Metadata.Name,
				Namespace:   runtime.Namespace(p.Metadata.Name, e.Name),
				Order:       e.Order,
			})
		}
	}

	// When no project-level environments exist, fall back to org-level definitions.
	// Projects now inherit org environments by default; the project spec stores
	// overrides only. This avoids the Environments stat showing 0 in the dashboard.
	if len(envs) == 0 && ih.orgProvider != nil {
		if org, orgErr := ih.orgProvider.GetOrg(r.Context()); orgErr == nil && org != nil {
			for _, e := range org.Environments {
				envs = append(envs, EnvironmentDTO{
					Name:        e.Name,
					DisplayName: e.DisplayName,
					Order:       e.Order,
					ClusterRef:  e.ClusterRef,
					BaseDomain:  e.BaseDomain,
				})
			}
		}
	}

	if envs == nil {
		envs = []EnvironmentDTO{}
	}

	writeJSON(w, http.StatusOK, EnvironmentsResponse{Environments: envs})
}

// handleListServices returns the services for a project with runtime state.
//
// Deprecated: Registered at the legacy
// GET /api/v1/projects/{project}/services route. Use
// GET /api/v1/projects/{project}/apps for new integrations.
// See docs/migration-app-model.md.
func (ih *inventoryHandler) handleListServices(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")

	proj, err := ih.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "project \"" + projectName + "\" not found",
		})
		return
	}

	services := make([]ServiceRuntimeDTO, 0, len(proj.Spec.Services))
	for _, svc := range proj.Spec.Services {
		dto := ServiceRuntimeDTO{
			Name: svc.Name,
			Template: templateRefDTO{
				Name:    svc.Template.Name,
				Version: svc.Template.Version,
			},
			Runtime: ih.bestRuntime(r, proj, svc.Name),
		}
		services = append(services, dto)
	}

	writeJSON(w, http.StatusOK, ProjectServicesResponse{
		Project:  projectName,
		Services: services,
	})
}

// handleGetService returns detailed config and per-environment runtime state.
//
// Deprecated: Registered at the legacy
// GET /api/v1/projects/{project}/services/{service} route. Use
// GET /api/v1/projects/{project}/apps/{app} for new integrations.
// See docs/migration-app-model.md.
func (ih *inventoryHandler) handleGetService(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	serviceName := r.PathValue("service")

	proj, err := ih.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "project \"" + projectName + "\" not found",
		})
		return
	}

	var svc *project.Service
	for i := range proj.Spec.Services {
		if proj.Spec.Services[i].Name == serviceName {
			svc = &proj.Spec.Services[i]
			break
		}
	}
	if svc == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "service \"" + serviceName + "\" not found in project \"" + projectName + "\"",
		})
		return
	}

	var envDTOs []ServiceEnvDTO
	for _, env := range proj.Spec.Environments {
		ns := runtime.Namespace(projectName, env.Name)
		rt := ih.runtimeForNamespace(r, ns, serviceName)
		envDTOs = append(envDTOs, ServiceEnvDTO{
			Environment: env.Name,
			Namespace:   ns,
			Runtime:     rt,
		})
	}
	if envDTOs == nil {
		envDTOs = []ServiceEnvDTO{}
	}

	values := svc.Values
	if values == nil {
		values = map[string]any{}
	}

	writeJSON(w, http.StatusOK, ServiceDetailResponse{
		Name:    svc.Name,
		Project: projectName,
		Template: templateRefDTO{
			Name:    svc.Template.Name,
			Version: svc.Template.Version,
		},
		Values:       values,
		SecretRefs:   toSecretRefDTOs(svc.SecretRefs),
		Environments: envDTOs,
	})
}

// bestRuntime returns the runtime state for the first environment (summary).
func (ih *inventoryHandler) bestRuntime(r *http.Request, proj *project.Project, serviceName string) RuntimeDTO {
	if len(proj.Spec.Environments) == 0 {
		return notDeployedDTO("")
	}
	ns := runtime.Namespace(proj.Metadata.Name, proj.Spec.Environments[0].Name)
	return ih.runtimeForNamespace(r, ns, serviceName)
}

func (ih *inventoryHandler) runtimeForNamespace(r *http.Request, namespace, serviceName string) RuntimeDTO {
	if ih.runtimeProvider == nil {
		return notDeployedDTO(namespace)
	}

	info, err := ih.runtimeProvider.GetServiceRuntime(r.Context(), namespace, serviceName)
	if err != nil || info == nil {
		return notDeployedDTO(namespace)
	}

	urls := info.IngressURLs
	if urls == nil {
		urls = []string{}
	}

	return RuntimeDTO{
		Status:       info.Status,
		Image:        info.Image,
		Replicas:     info.Replicas,
		Available:    info.Available,
		IngressURLs:  urls,
		Namespace:    info.Namespace,
		LastDeployed: info.LastDeployed,
	}
}

func notDeployedDTO(namespace string) RuntimeDTO {
	return RuntimeDTO{
		Status:      runtime.StatusNotDeployed,
		IngressURLs: []string{},
		Namespace:   namespace,
	}
}
