package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/suparcloud/suparship/internal/tpl"
)

// templateRegistryHandler serves the template registry API.
type templateRegistryHandler struct {
	store  *tpl.RegistryStore
	auth   *authHandler
	logger *slog.Logger
}

func (h *templateRegistryHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/templates/registry", h.auth.requireAuth(h.handleGetRegistry))
	mux.HandleFunc("PUT /api/v1/templates/registry", h.auth.requireAuth(h.handleUpdateRegistry))
	mux.HandleFunc("GET /api/v1/templates/sources", h.auth.requireAuth(h.handleListSources))
}

// handleGetRegistry returns the full template registry.
func (h *templateRegistryHandler) handleGetRegistry(w http.ResponseWriter, r *http.Request) {
	reg, err := h.store.Get(r.Context())
	if err != nil {
		if errors.Is(err, tpl.ErrRegistryNotFound) {
			writeJSON(w, http.StatusOK, templateRegistryResponse{
				Configured: false,
				Registry:   &tpl.TemplateRegistry{BuiltIn: []string{}, Sources: []tpl.TemplateSource{}},
			})
			return
		}
		h.logger.Error("get template registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read template registry"})
		return
	}

	writeJSON(w, http.StatusOK, templateRegistryResponse{Configured: true, Registry: reg})
}

// handleUpdateRegistry saves the template registry configuration
// (built-in list and external repos). This does not trigger a sync — it only
// persists the desired state.
func (h *templateRegistryHandler) handleUpdateRegistry(w http.ResponseWriter, r *http.Request) {
	var reg tpl.TemplateRegistry
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
		return
	}

	if reg.BuiltIn == nil {
		reg.BuiltIn = []string{}
	}
	if reg.Sources == nil {
		reg.Sources = []tpl.TemplateSource{}
	}

	if err := h.store.Save(r.Context(), &reg); err != nil {
		h.logger.Error("save template registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save template registry"})
		return
	}

	writeJSON(w, http.StatusOK, templateRegistryResponse{Configured: true, Registry: &reg})
}

// handleListSources returns just the resolved source list (lighter than full registry).
func (h *templateRegistryHandler) handleListSources(w http.ResponseWriter, r *http.Request) {
	reg, err := h.store.Get(r.Context())
	if err != nil {
		if errors.Is(err, tpl.ErrRegistryNotFound) {
			writeJSON(w, http.StatusOK, templateSourcesResponse{Sources: []tpl.TemplateSource{}})
			return
		}
		h.logger.Error("list template sources", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read template sources"})
		return
	}

	writeJSON(w, http.StatusOK, templateSourcesResponse{Sources: reg.Sources})
}

// templateRegistryResponse wraps the registry for the API.
type templateRegistryResponse struct {
	Configured bool                  `json:"configured"`
	Registry   *tpl.TemplateRegistry `json:"registry"`
}

// templateSourcesResponse is the lighter sources-only response.
type templateSourcesResponse struct {
	Sources []tpl.TemplateSource `json:"sources"`
}
