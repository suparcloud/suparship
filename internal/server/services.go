// Package server — this file contains the legacy service-oriented create
// endpoint (POST /api/v1/projects/{project}/services).
//
// # Deprecation notice
//
// All types and handlers in this file are retained for backwards compatibility
// only. The primary app creation endpoint is now:
//
//	POST /api/v1/projects/{project}/apps
//
// New integrations should use the app-oriented endpoint. Existing callers of
// the service endpoint continue to work without modification.
// See docs/migration-app-model.md for the full transition guide.
package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/tpl"
)

// --- Request / Response DTOs ---

// createServiceRequest is the JSON body for the legacy
// POST /api/v1/projects/{project}/services endpoint.
//
// Deprecated: Use createAppRequest (POST /api/v1/projects/{project}/apps)
// for new integrations. See docs/migration-app-model.md.
type createServiceRequest struct {
	Name       string             `json:"name"`
	Template   string             `json:"template"`
	Values     map[string]any     `json:"values"`
	SecretRefs []secretRefRequest `json:"secretRefs"`
}

// secretRefRequest is shared between the legacy service API and the app API.
// It is not itself deprecated, only its presence in service-oriented DTOs is.
type secretRefRequest struct {
	Name      string `json:"name"`
	SecretRef string `json:"secretRef"`
}

// createServiceResponse is returned by the legacy
// POST /api/v1/projects/{project}/services endpoint.
//
// Deprecated: See createAppResponse for the app-oriented equivalent.
// See docs/migration-app-model.md.
type createServiceResponse struct {
	Service    serviceResponseDTO `json:"service"`
	HelmValues map[string]any     `json:"helmValues"`
}

// serviceResponseDTO is the service representation in legacy create responses.
//
// Deprecated: See AppDetailDTO for the app-oriented equivalent.
// See docs/migration-app-model.md.
type serviceResponseDTO struct {
	Name       string             `json:"name"`
	Template   templateRefDTO     `json:"template"`
	Values     map[string]any     `json:"values"`
	SecretRefs []secretRefRequest `json:"secretRefs"`
}

// templateRefDTO is a lightweight template reference used by both the legacy
// service API and the inventory handler. It is not itself deprecated.
type templateRefDTO struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// --- Handler ---

// serviceHandler handles service creation within projects. It is wired into
// the rbacHandler's route registration so that RBAC middleware is applied.
//
// Deprecated: serviceHandler backs the legacy
// POST /api/v1/projects/{project}/services endpoint. New app creation is
// handled by appHandler.handleCreateApp.
// See docs/migration-app-model.md.
type serviceHandler struct {
	projectStore project.Store
	templateIdx  map[string]*tpl.Template
}

// newServiceHandler constructs the legacy service creation handler.
//
// Deprecated: See newServiceHandler's type comment.
func newServiceHandler(store project.Store, templates []*tpl.Template) *serviceHandler {
	idx := make(map[string]*tpl.Template, len(templates))
	for _, t := range templates {
		idx[t.Metadata.Name] = t
	}
	return &serviceHandler{projectStore: store, templateIdx: idx}
}

// handleCreateService handles POST /api/v1/projects/{project}/services.
//
// Deprecated: This endpoint is superseded by
// POST /api/v1/projects/{project}/apps.
// It remains registered for backwards compatibility and emits a
// Deprecation response header so API clients can detect the migration signal.
// See docs/migration-app-model.md.
func (sh *serviceHandler) handleCreateService(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")

	var req createServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "service name is required"})
		return
	}
	if req.Template == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "template name is required"})
		return
	}

	tmpl, ok := sh.templateIdx[req.Template]
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorResponse{
			Error: "template \"" + req.Template + "\" not found",
		})
		return
	}

	values := req.Values
	if values == nil {
		values = map[string]any{}
	}
	secretRefs := toModelSecretRefs(req.SecretRefs)

	if err := project.ValidateServiceInputs(values, secretRefs, tmpl); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}

	proj, err := sh.projectStore.Get(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{
			Error: "project \"" + projectName + "\" not found",
		})
		return
	}

	for _, existing := range proj.Spec.Services {
		if existing.Name == req.Name {
			writeJSON(w, http.StatusConflict, errorResponse{
				Error: "service \"" + req.Name + "\" already exists in project \"" + projectName + "\"",
			})
			return
		}
	}

	svc := project.Service{
		Name: req.Name,
		Template: project.TemplateRef{
			Name:    tmpl.Metadata.Name,
			Version: tmpl.Metadata.Version,
		},
		Values:     values,
		SecretRefs: secretRefs,
	}

	proj.Spec.Services = append(proj.Spec.Services, svc)

	if err := sh.projectStore.Save(r.Context(), proj); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save project"})
		return
	}

	helmValues, err := project.RenderHelmValues(&svc, tmpl)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("render template mappings: %v", err)})
		return
	}

	writeJSON(w, http.StatusCreated, createServiceResponse{
		Service: serviceResponseDTO{
			Name: svc.Name,
			Template: templateRefDTO{
				Name:    svc.Template.Name,
				Version: svc.Template.Version,
			},
			Values:     svc.Values,
			SecretRefs: toSecretRefDTOs(svc.SecretRefs),
		},
		HelmValues: helmValues,
	})
}

func toModelSecretRefs(dtos []secretRefRequest) []project.SecretRef {
	if len(dtos) == 0 {
		return nil
	}
	refs := make([]project.SecretRef, len(dtos))
	for i, d := range dtos {
		refs[i] = project.SecretRef{Name: d.Name, SecretRef: d.SecretRef}
	}
	return refs
}

func toSecretRefDTOs(refs []project.SecretRef) []secretRefRequest {
	if len(refs) == 0 {
		return []secretRefRequest{}
	}
	dtos := make([]secretRefRequest, len(refs))
	for i, r := range refs {
		dtos[i] = secretRefRequest{Name: r.Name, SecretRef: r.SecretRef}
	}
	return dtos
}
