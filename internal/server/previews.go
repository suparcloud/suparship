package server

import (
	"encoding/json"
	"net/http"

	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/runtime"
)

// --- Preview DTOs ---

// PreviewDTO is the JSON representation of a preview environment in the legacy
// service-oriented preview endpoints.
//
// Deprecated: Use AppPreviewSummaryDTO (from the app-scoped preview endpoints)
// for new integrations. See docs/migration-app-model.md.
type PreviewDTO struct {
	Name      string `json:"name"`
	Project   string `json:"project"`
	// Service is the name of the service (i.e. app) that owns this preview.
	// Deprecated: use AppPreviewSummaryDTO.AppName for the app-oriented field.
	Service   string `json:"service"`
	Namespace string `json:"namespace"`
	Status    string `json:"status"`
	URL       string `json:"url,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// PreviewsResponse is the JSON body for the legacy GET /api/v1/previews
// endpoint.
//
// Deprecated: The app-oriented equivalent is AppPreviewsResponse, returned by
// GET /api/v1/projects/{project}/apps/{app}/previews.
// See docs/migration-app-model.md.
type PreviewsResponse struct {
	Previews []PreviewDTO `json:"previews"`
}

// CreatePreviewRequest is the JSON body for the legacy POST /api/v1/previews
// endpoint.
//
// Deprecated: Use CreateAppPreviewRequest
// (POST /api/v1/projects/{project}/apps/{app}/previews) for new integrations.
// See docs/migration-app-model.md.
type CreatePreviewRequest struct {
	Name    string `json:"name"`
	Project string `json:"project"`
	// Service identifies the service (i.e. app) to create a preview for.
	// Deprecated: use the app-scoped preview endpoint instead, where the app
	// name is a URL path parameter rather than a request body field.
	Service string `json:"service"`
}

// --- Handler ---

// previewHandler serves the legacy service-oriented preview endpoints. RBAC is
// enforced inline because the project is taken from the request body (POST) or
// from the stored preview (DELETE), not from a URL path parameter.
//
// Deprecated: previewHandler backs the legacy preview routes
// (GET|POST /api/v1/previews, DELETE /api/v1/previews/{name}, and
// GET /api/v1/projects/{project}/services/{service}/previews). New preview
// management should use appHandler, which serves the app-scoped preview
// routes. See docs/migration-app-model.md.
type previewHandler struct {
	previewStore    preview.Store
	projectStore    project.Store
	runtimeProvider runtime.Provider // may be nil
	orgProvider     rbac.OrgProvider
}

func newPreviewHandler(ps preview.Store, projStore project.Store, rp runtime.Provider, org rbac.OrgProvider) *previewHandler {
	return &previewHandler{
		previewStore:    ps,
		projectStore:    projStore,
		runtimeProvider: rp,
		orgProvider:     org,
	}
}

// handleListServicePreviews returns previews filtered to a specific project and
// service. It is registered at
// GET /api/v1/projects/{project}/services/{service}/previews
// and enforces viewer-level RBAC via the rbacHandler middleware.
//
// Deprecated: Use GET /api/v1/projects/{project}/apps/{app}/previews instead.
// See docs/migration-app-model.md.
func (ph *previewHandler) handleListServicePreviews(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	serviceName := r.PathValue("service")

	all, err := ph.previewStore.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list previews"})
		return
	}

	dtos := make([]PreviewDTO, 0)
	for _, p := range all {
		if p.Spec.Project == projectName && p.Spec.Service == serviceName {
			dtos = append(dtos, ph.toDTO(r, p))
		}
	}

	writeJSON(w, http.StatusOK, PreviewsResponse{Previews: dtos})
}

// handleListPreviews returns all preview environments.
//
// Deprecated: Registered at the legacy GET /api/v1/previews route. Use the
// app-scoped GET /api/v1/projects/{project}/apps/{app}/previews instead.
// See docs/migration-app-model.md.
func (ph *previewHandler) handleListPreviews(w http.ResponseWriter, r *http.Request) {
	previews, err := ph.previewStore.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list previews"})
		return
	}

	dtos := make([]PreviewDTO, 0, len(previews))
	for _, p := range previews {
		dtos = append(dtos, ph.toDTO(r, p))
	}

	writeJSON(w, http.StatusOK, PreviewsResponse{Previews: dtos})
}

// handleCreatePreview creates a new preview environment.
// Requires developer role on the target project.
//
// Deprecated: Registered at the legacy POST /api/v1/previews route. Use
// POST /api/v1/projects/{project}/apps/{app}/previews instead.
// See docs/migration-app-model.md.
func (ph *previewHandler) handleCreatePreview(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r.Context())

	var req CreatePreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "preview name is required"})
		return
	}
	if req.Project == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "project is required"})
		return
	}
	if req.Service == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "service is required"})
		return
	}

	org, err := ph.orgProvider.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
		return
	}
	if !rbac.CanCreatePreview(org, sess.Username, req.Project) {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "insufficient permissions"})
		return
	}

	proj, err := ph.projectStore.Get(r.Context(), req.Project)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "project \"" + req.Project + "\" not found",
		})
		return
	}

	serviceExists := false
	for _, svc := range proj.Spec.Services {
		if svc.Name == req.Service {
			serviceExists = true
			break
		}
	}
	if !serviceExists {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "service \"" + req.Service + "\" not found in project \"" + req.Project + "\"",
		})
		return
	}

	existing, _ := ph.previewStore.Get(r.Context(), req.Name)
	if existing != nil {
		writeJSON(w, http.StatusConflict, errorResponse{
			Error: "preview \"" + req.Name + "\" already exists",
		})
		return
	}

	p := preview.New(req.Name, req.Project, req.Service)
	if err := p.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	if err := ph.previewStore.Save(r.Context(), p); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save preview"})
		return
	}

	writeJSON(w, http.StatusCreated, ph.toDTO(r, p))
}

// handleDeletePreview deletes a preview environment.
// Requires developer role on the preview's project.
//
// Deprecated: Registered at the legacy DELETE /api/v1/previews/{name} route.
// Use DELETE /api/v1/projects/{project}/apps/{app}/previews/{name} instead.
// See docs/migration-app-model.md.
func (ph *previewHandler) handleDeletePreview(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r.Context())
	name := r.PathValue("name")

	p, err := ph.previewStore.Get(r.Context(), name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "preview \"" + name + "\" not found",
		})
		return
	}

	org, err := ph.orgProvider.GetOrg(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to load org config"})
		return
	}
	if !rbac.CanCreatePreview(org, sess.Username, p.Spec.Project) {
		writeJSON(w, http.StatusForbidden, errorResponse{Error: "insufficient permissions"})
		return
	}

	if err := ph.previewStore.Delete(r.Context(), name); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete preview"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// toDTO converts a preview model to a DTO, enriching with runtime status.
func (ph *previewHandler) toDTO(r *http.Request, p *preview.Preview) PreviewDTO {
	ns := p.PreviewNamespace()
	status := runtime.StatusNotDeployed
	url := ""

	if ph.runtimeProvider != nil {
		info, err := ph.runtimeProvider.GetServiceRuntime(r.Context(), ns, p.Spec.Service)
		if err == nil && info != nil {
			status = info.Status
			if len(info.IngressURLs) > 0 {
				url = info.IngressURLs[0]
			}
		}
	}

	return PreviewDTO{
		Name:      p.Metadata.Name,
		Project:   p.Spec.Project,
		Service:   p.Spec.Service,
		Namespace: ns,
		Status:    status,
		URL:       url,
		CreatedAt: p.Metadata.CreatedAt,
	}
}
