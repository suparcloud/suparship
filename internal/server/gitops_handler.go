package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/suparcloud/suparship/internal/gitops"
)

// gitopsHandler serves the GitOps configuration API.
type gitopsHandler struct {
	store  *gitops.ConfigStore
	auth   *authHandler
	logger *slog.Logger
}

func (h *gitopsHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/gitops/config", h.auth.requireAuth(h.handleGetConfig))
	mux.HandleFunc("PUT /api/v1/gitops/config", h.auth.requireAuth(h.handleUpdateConfig))
	mux.HandleFunc("POST /api/v1/gitops/test-connection", h.auth.requireAuth(h.handleTestConnection))
}

// handleGetConfig returns the current GitOps repo configuration.
func (h *gitopsHandler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.store.Get(r.Context())
	if err != nil {
		if errors.Is(err, gitops.ErrConfigNotFound) {
			writeJSON(w, http.StatusOK, gitopsConfigResponse{Configured: false})
			return
		}
		h.logger.Error("get gitops config", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to read gitops configuration"})
		return
	}

	writeJSON(w, http.StatusOK, gitopsConfigResponse{
		Configured: true,
		Config:     cfg,
	})
}

// handleUpdateConfig saves or updates the GitOps repo configuration.
func (h *gitopsHandler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var cfg gitops.RepoConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
		return
	}

	if err := cfg.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	if err := h.store.Save(r.Context(), &cfg); err != nil {
		h.logger.Error("save gitops config", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save gitops configuration"})
		return
	}

	writeJSON(w, http.StatusOK, gitopsConfigResponse{
		Configured: true,
		Config:     &cfg,
	})
}

// testConnectionRequest is the body for POST /api/v1/gitops/test-connection.
type testConnectionRequest struct {
	RepoURL  string `json:"repoURL"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// testConnectionResponse is the response for POST /api/v1/gitops/test-connection.
type testConnectionResponse struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	DurationMs int64  `json:"durationMs"`
}

// handleTestConnection verifies that the given credentials can reach the Git repo.
// It uses `git ls-remote` which works without cloning the full repo.
func (h *gitopsHandler) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	var req testConnectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
		return
	}

	if req.RepoURL == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "repoURL is required"})
		return
	}

	repoURL := req.RepoURL
	if req.Username != "" && req.Password != "" && strings.HasPrefix(repoURL, "https://") {
		repoURL = injectCredentials(repoURL, req.Username, req.Password)
	}

	start := time.Now()
	cmd := exec.CommandContext(r.Context(), "git", "ls-remote", "--exit-code", repoURL)
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	resp := testConnectionResponse{DurationMs: elapsed.Milliseconds()}
	if err != nil {
		resp.Success = false
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		resp.Message = sanitizeGitError(msg)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	resp.Success = true
	resp.Message = "connection successful"
	writeJSON(w, http.StatusOK, resp)
}

// gitopsConfigResponse wraps the GitOps config for the API response.
type gitopsConfigResponse struct {
	Configured bool               `json:"configured"`
	Config     *gitops.RepoConfig `json:"config,omitempty"`
}

// injectCredentials embeds username:password into an HTTPS URL for git operations.
func injectCredentials(repoURL, username, password string) string {
	const prefix = "https://"
	if !strings.HasPrefix(repoURL, prefix) {
		return repoURL
	}
	rest := repoURL[len(prefix):]
	return prefix + username + ":" + password + "@" + rest
}

// sanitizeGitError removes embedded credentials from git error messages.
func sanitizeGitError(msg string) string {
	lines := strings.Split(msg, "\n")
	var clean []string
	for _, line := range lines {
		if strings.Contains(line, "@") && (strings.Contains(line, "https://") || strings.Contains(line, "http://")) {
			idx := strings.Index(line, "://")
			if idx >= 0 {
				atIdx := strings.Index(line[idx:], "@")
				if atIdx >= 0 {
					line = line[:idx+3] + "***@" + line[idx+atIdx+1:]
				}
			}
		}
		clean = append(clean, line)
	}
	return strings.Join(clean, "\n")
}
