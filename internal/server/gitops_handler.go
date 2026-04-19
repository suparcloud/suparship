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
	store     *gitops.ConfigStore
	auth      *authHandler
	logger    *slog.Logger
	activator GitOpsActivatorFunc // optional; nil = skip post-save activation
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

	credSet, err := h.store.HasCredentials(r.Context())
	if err != nil {
		h.logger.Warn("check gitops credentials", "error", err)
	}

	writeJSON(w, http.StatusOK, gitopsConfigResponse{
		Configured:     true,
		CredentialsSet: credSet,
		Config:         cfg,
	})
}

// gitopsUpdateRequest is the body for PUT /api/v1/gitops/config.
// It extends RepoConfig with optional credential fields so the UI can
// submit credentials without requiring users to pre-create a K8s Secret.
type gitopsUpdateRequest struct {
	gitops.RepoConfig
	Credentials *gitopsCredentials `json:"credentials,omitempty"`
}

// gitopsCredentials holds the plaintext credential values submitted by the UI.
// They are written into the managed K8s Secret; never persisted in Git.
type gitopsCredentials struct {
	// Token is used for GitHub, GitLab, and Gitea (personal access token).
	Token string `json:"token,omitempty"`
	// Username is used for Bitbucket and generic providers.
	Username string `json:"username,omitempty"`
	// Password is the password or app-password for Bitbucket / generic providers.
	Password string `json:"password,omitempty"`
}

// handleUpdateConfig saves or updates the GitOps repo configuration and,
// if credentials are provided, stores them in the managed K8s Secret.
// After saving, it calls the optional activator to register the repo with
// ArgoCD and hot-reload the publisher.
func (h *gitopsHandler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var req gitopsUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid JSON body"})
		return
	}

	if err := req.RepoConfig.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	hasNewCredentials := req.Credentials != nil &&
		(req.Credentials.Token != "" || req.Credentials.Username != "" || req.Credentials.Password != "")

	if hasNewCredentials {
		// Save new credentials into the managed Secret.
		if err := h.store.SaveCredentials(r.Context(), req.Provider, req.Credentials.Token, req.Credentials.Username, req.Credentials.Password); err != nil {
			h.logger.Error("save gitops credentials", "error", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save credentials"})
			return
		}
		// Always point config at the managed secret so downstream tooling
		// (publisher, ArgoCD reg) can find credentials without manual setup.
		req.RepoConfig.AuthSecretRef = gitops.ManagedCredentialSecretName
	} else {
		// No new credentials: carry forward any existing authSecretRef so
		// a re-save of the URL/branch doesn't accidentally orphan stored creds.
		if existing, err := h.store.Get(r.Context()); err == nil && existing.AuthSecretRef != "" {
			req.RepoConfig.AuthSecretRef = existing.AuthSecretRef
		}
	}

	if err := h.store.Save(r.Context(), &req.RepoConfig); err != nil {
		h.logger.Error("save gitops config", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to save gitops configuration"})
		return
	}

	credSet, err := h.store.HasCredentials(r.Context())
	if err != nil {
		h.logger.Warn("check gitops credentials", "error", err)
	}

	// Post-save activation: register repo with ArgoCD + hot-reload publisher.
	var activationWarning string
	if h.activator != nil {
		// Resolve credentials from the just-saved (or previously saved) managed Secret.
		username, password, credErr := h.store.GetCredentials(r.Context(), &req.RepoConfig)
		if credErr != nil {
			h.logger.Warn("resolve credentials for activator", "error", credErr)
		}
		if actErr := h.activator(r.Context(), &req.RepoConfig, username, password); actErr != nil {
			h.logger.Warn("gitops activation failed", "error", actErr)
			activationWarning = actErr.Error()
		}
	}

	writeJSON(w, http.StatusOK, gitopsConfigResponse{
		Configured:        true,
		CredentialsSet:    credSet,
		Config:            &req.RepoConfig,
		ActivationWarning: activationWarning,
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
// When no credentials are provided in the request body it falls back to any
// credentials already stored in the managed K8s Secret.
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

	username := req.Username
	password := req.Password

	// Fall back to stored credentials when none are provided in the request.
	if username == "" || password == "" {
		if cfg, err := h.store.Get(r.Context()); err == nil {
			if u, p, err := h.store.GetCredentials(r.Context(), cfg); err == nil {
				if username == "" {
					username = u
				}
				if password == "" {
					password = p
				}
			}
		}
	}

	repoURL := req.RepoURL
	if username != "" && password != "" && strings.HasPrefix(repoURL, "https://") {
		repoURL = injectCredentials(repoURL, username, password)
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
	Configured     bool               `json:"configured"`
	CredentialsSet bool               `json:"credentialsSet"`
	Config         *gitops.RepoConfig `json:"config,omitempty"`
	// ActivationWarning is non-empty when post-save ArgoCD registration or
	// publisher hot-reload encountered a non-fatal error. The config was saved
	// successfully; the user can investigate and retry.
	ActivationWarning string `json:"activationWarning,omitempty"`
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
