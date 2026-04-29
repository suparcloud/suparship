package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/registrysync"
)

// templateRegistryHandler serves the template registry API.
//
// engine is optional — when nil the /sync endpoints return 503. The other
// endpoints continue to work because they only need the store. authMiddleware
// composes auth + org_admin enforcement; required for the write/sync paths,
// and used uniformly so a future tightening of read access only touches one
// line.
type templateRegistryHandler struct {
	store          *tpl.RegistryStore
	auth           *authHandler
	engine         *registrysync.Engine
	authMiddleware func(http.HandlerFunc) http.HandlerFunc
	logger         *slog.Logger
}

func (h *templateRegistryHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/templates/registry", h.auth.requireAuth(h.handleGetRegistry))
	mux.HandleFunc("PUT /api/v1/templates/registry", h.adminOrAuth()(h.handleUpdateRegistry))
	mux.HandleFunc("GET /api/v1/templates/sources", h.auth.requireAuth(h.handleListSources))
	mux.HandleFunc("POST /api/v1/templates/registry/sync", h.adminOrAuth()(h.handleSyncAll))
	mux.HandleFunc("POST /api/v1/templates/registry/sources/{name}/sync", h.adminOrAuth()(h.handleSyncOne))
}

// adminOrAuth returns the org_admin middleware when wired, falling back to
// plain auth so the registry endpoints still work in environments that
// haven't installed the rbac handler (notably tests).
func (h *templateRegistryHandler) adminOrAuth() func(http.HandlerFunc) http.HandlerFunc {
	if h.authMiddleware != nil {
		return h.authMiddleware
	}
	return h.auth.requireAuth
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

// syncResultDTO is the JSON-friendly view of a single source's sync outcome.
// We expose Templates + Error explicitly so the UI can show "3 imported, 1
// failed" without needing to inspect an opaque Go error.
type syncResultDTO struct {
	SourceName string    `json:"sourceName"`
	Templates  []string  `json:"templates"`
	SyncedAt   time.Time `json:"syncedAt"`
	Error      string    `json:"error,omitempty"`
}

// syncResponse wraps the per-source results returned from the sync
// endpoints. Always returns 200 even when individual sources failed —
// partial success is a normal outcome and the caller inspects each entry.
type syncResponse struct {
	Results []syncResultDTO `json:"results"`
}

// handleSyncAll triggers a sync across every ExternalTemplateRepo in the
// registry. Updates the registry's Sources list with the freshly imported
// templates and returns per-source results inline.
func (h *templateRegistryHandler) handleSyncAll(w http.ResponseWriter, r *http.Request) {
	if h.engine == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "registry sync engine not configured"})
		return
	}
	reg, err := h.store.Get(r.Context())
	if err != nil {
		if errors.Is(err, tpl.ErrRegistryNotFound) {
			// No registry → nothing to sync. Respond 200 with an empty
			// results list so callers don't need to special-case "no
			// registry yet".
			writeJSON(w, http.StatusOK, syncResponse{Results: []syncResultDTO{}})
			return
		}
		h.logger.Error("sync: get registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read template registry"})
		return
	}

	results := h.engine.SyncAll(r.Context(), reg)
	for i, repo := range reg.External {
		if i < len(results) {
			registrysync.ApplyResult(reg, repo, results[i])
		}
	}
	if err := h.store.Save(r.Context(), reg); err != nil {
		h.logger.Error("sync: save registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist sync state"})
		return
	}

	writeJSON(w, http.StatusOK, syncResponse{Results: toSyncDTOs(results)})
}

// handleSyncOne syncs a single named source. The {name} path parameter must
// match an ExternalTemplateRepo.Name in the registry.
func (h *templateRegistryHandler) handleSyncOne(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "source name required"})
		return
	}
	if h.engine == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "registry sync engine not configured"})
		return
	}
	reg, err := h.store.Get(r.Context())
	if err != nil {
		h.logger.Error("sync: get registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read template registry"})
		return
	}
	var target *tpl.ExternalTemplateRepo
	for i := range reg.External {
		if reg.External[i].Name == name {
			target = &reg.External[i]
			break
		}
	}
	if target == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "source not found: " + name})
		return
	}

	result := h.engine.SyncOne(r.Context(), *target)
	registrysync.ApplyResult(reg, *target, result)
	if err := h.store.Save(r.Context(), reg); err != nil {
		h.logger.Error("sync: save registry", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to persist sync state"})
		return
	}
	writeJSON(w, http.StatusOK, syncResponse{Results: toSyncDTOs([]registrysync.SyncResult{result})})
}

// toSyncDTOs flattens engine results into JSON-friendly form. Errors are
// stringified at the boundary because the JSON encoder won't expose Go
// error types meaningfully.
func toSyncDTOs(results []registrysync.SyncResult) []syncResultDTO {
	out := make([]syncResultDTO, 0, len(results))
	for _, r := range results {
		dto := syncResultDTO{
			SourceName: r.SourceName,
			Templates:  r.Templates,
			SyncedAt:   r.SyncedAt,
		}
		if r.Err != nil {
			dto.Error = r.Err.Error()
		}
		if dto.Templates == nil {
			dto.Templates = []string{}
		}
		out = append(out, dto)
	}
	return out
}
