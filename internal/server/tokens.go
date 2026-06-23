package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/token"
)

// tokenHandler serves project-scoped API token management. All routes require
// project_admin (wired in registerRoutes); minting a long-lived credential is a
// privileged action.
type tokenHandler struct {
	store        token.Store
	projectStore project.Store
}

func newTokenHandler(store token.Store, projStore project.Store) *tokenHandler {
	return &tokenHandler{store: store, projectStore: projStore}
}

// createTokenRequest is the POST body. Role defaults to developer; ExpiresInDays
// omitted (or zero) means the token never expires.
type createTokenRequest struct {
	Name          string `json:"name"`
	Role          string `json:"role,omitempty"`
	ExpiresInDays int    `json:"expiresInDays,omitempty"`
}

// createTokenResponse carries the one-time plaintext token plus its metadata.
// The plaintext is shown only here and is never retrievable again.
type createTokenResponse struct {
	Token string `json:"token"`
	token.Metadata
}

// tokensResponse is the GET list body.
type tokensResponse struct {
	Tokens []token.Metadata `json:"tokens"`
}

// projectTokenRoles are the roles a project token may carry. org_admin is
// intentionally excluded — a project-scoped credential can never be org admin.
func validProjectTokenRole(role string) bool {
	switch rbac.Role(role) {
	case rbac.RoleViewer, rbac.RoleDeveloper, rbac.RoleProjectAdmin:
		return true
	default:
		return false
	}
}

func (th *tokenHandler) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	sess := sessionFromContext(r.Context())

	if _, err := th.projectStore.Get(r.Context(), projectName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project \"" + projectName + "\" not found"})
		return
	}

	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "token name is required"})
		return
	}
	role := req.Role
	if role == "" {
		role = string(rbac.RoleDeveloper)
	}
	if !validProjectTokenRole(role) {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "role must be one of viewer, developer, project_admin"})
		return
	}
	if req.ExpiresInDays < 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "expiresInDays must not be negative"})
		return
	}

	now := time.Now().UTC()
	meta := token.Metadata{
		Name:      req.Name,
		Project:   projectName,
		Role:      role,
		CreatedBy: sess.Username,
		CreatedAt: now,
	}
	if req.ExpiresInDays > 0 {
		exp := now.Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		meta.ExpiresAt = &exp
	}

	plaintext, out, err := th.store.Issue(r.Context(), meta)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to create token"})
		return
	}

	writeJSON(w, http.StatusCreated, createTokenResponse{Token: plaintext, Metadata: out})
}

func (th *tokenHandler) handleListTokens(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")

	if _, err := th.projectStore.Get(r.Context(), projectName); err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "project \"" + projectName + "\" not found"})
		return
	}

	tokens, err := th.store.List(r.Context(), projectName)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list tokens"})
		return
	}
	writeJSON(w, http.StatusOK, tokensResponse{Tokens: tokens})
}

func (th *tokenHandler) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	projectName := r.PathValue("project")
	id := r.PathValue("id")

	if err := th.store.Delete(r.Context(), projectName, id); err != nil {
		if errors.Is(err, token.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "token \"" + id + "\" not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete token"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
