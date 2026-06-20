package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/registry"
)

// registryHandler serves the container registry configuration API.
type registryHandler struct {
	store  *registry.Store
	auth   *authHandler
	logger *slog.Logger
	// projectStore, when set, lets a registry-config save refresh the Kargo
	// image-credential Secret in every existing Kargo Project namespace.
	projectStore project.Store
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

	// Refresh the Kargo image-credential Secret in every Kargo Project namespace
	// so Warehouses can authenticate after a credential change. Best-effort.
	h.reconcileKargoCreds(r.Context())

	writeJSON(w, http.StatusOK, registryConfigResponse{Configured: true, Config: &cfg})
}

// reconcileKargoCreds (re)provisions the Kargo image-credential Secret in the
// Kargo Project namespace of every project, from the current registry config.
// Best-effort: per-project failures are logged, not surfaced to the caller.
func (h *registryHandler) reconcileKargoCreds(ctx context.Context) {
	if h.projectStore == nil {
		return
	}
	projects, err := h.projectStore.List(ctx)
	if err != nil {
		h.logger.Warn("reconcile kargo creds: list projects failed", "error", err)
		return
	}
	for _, p := range projects {
		ns := gitops.KargoNamespaceForProject(p.Metadata.Name)
		if err := h.store.EnsureKargoCred(ctx, ns); err != nil {
			h.logger.Warn("reconcile kargo cred", "project", p.Metadata.Name, "namespace", ns, "error", err)
		}
	}
}

type registryConfigResponse struct {
	Configured bool             `json:"configured"`
	Config     *registry.Config `json:"config"`
}
