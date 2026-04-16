package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/suparcloud/suparship/internal/registry"
)

// registryHandler serves the container registry configuration API.
type registryHandler struct {
	store  *registry.Store
	auth   *authHandler
	logger *slog.Logger
}

func (h *registryHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/registry/config", h.auth.requireAuth(h.handleGetConfig))
	mux.HandleFunc("PUT /api/v1/registry/config", h.auth.requireAuth(h.handleUpdateConfig))
}

func (h *registryHandler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.store.Get(r.Context())
	if err != nil {
		if errors.Is(err, registry.ErrConfigNotFound) {
			writeJSON(w, http.StatusOK, registryConfigResponse{
				Configured: false,
				Config:     &registry.Config{Enabled: false},
			})
			return
		}
		h.logger.Error("get registry config", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read registry configuration"})
		return
	}

	writeJSON(w, http.StatusOK, registryConfigResponse{Configured: true, Config: cfg})
}

func (h *registryHandler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var cfg registry.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
		return
	}

	if err := cfg.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	if err := h.store.Save(r.Context(), &cfg); err != nil {
		h.logger.Error("save registry config", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save registry configuration"})
		return
	}

	writeJSON(w, http.StatusOK, registryConfigResponse{Configured: true, Config: &cfg})
}

type registryConfigResponse struct {
	Configured bool             `json:"configured"`
	Config     *registry.Config `json:"config"`
}
