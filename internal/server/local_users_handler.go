package server

// local_users_handler.go — org-admin management of invite-provisioned local
// users (the basic-auth escape hatch for orgs without an IdP).
//
// Endpoints (all org_admin):
//
//	GET    /api/v1/org/users
//	POST   /api/v1/org/users
//	POST   /api/v1/org/users/{username}/invite
//	DELETE /api/v1/org/users/{username}
//
// The public redemption endpoints live in auth.go (the invite flow itself
// establishes the session).

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/suparcloud/suparship/internal/localuser"
	"github.com/suparcloud/suparship/internal/rbac"
)

// LocalUserDTO is the listing view of a local user.
type LocalUserDTO struct {
	Username  string `json:"username"`
	CreatedAt string `json:"createdAt"`
	// Status: "active" (password set), "invited" (outstanding invite),
	// "invite_expired" (invite lapsed unredeemed, no password yet).
	Status   string `json:"status"`
	Disabled bool   `json:"disabled,omitempty"`
	// InviteExpiresAt is set while an invite is outstanding (RFC 3339).
	InviteExpiresAt string `json:"inviteExpiresAt,omitempty"`
}

type createLocalUserRequest struct {
	Username string `json:"username"`
	// Teams optionally names existing teams to add the user to, so they have
	// a role on first login instead of an empty shell.
	Teams []string `json:"teams,omitempty"`
}

// localUserInviteResponse carries the ONE-TIME plaintext invite token. It is
// never stored or shown again; the UI turns it into a copyable link.
type localUserInviteResponse struct {
	Username    string `json:"username"`
	InviteToken string `json:"inviteToken"`
	ExpiresAt   string `json:"expiresAt"`
}

func localUserToDTO(u localuser.User, now time.Time) LocalUserDTO {
	dto := LocalUserDTO{
		Username:  u.Username,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
		Disabled:  u.Disabled,
	}
	switch {
	case u.InviteExpiresAt != nil && now.Before(*u.InviteExpiresAt):
		dto.Status = "invited"
		dto.InviteExpiresAt = u.InviteExpiresAt.UTC().Format(time.RFC3339)
	case u.HasPassword:
		dto.Status = "active"
	default:
		dto.Status = "invite_expired"
	}
	return dto
}

func (rh *rbacHandler) localUsers() localuser.Store {
	if rh.auth == nil {
		return nil
	}
	return rh.auth.localUsers
}

// GET /api/v1/org/users
func (rh *rbacHandler) handleListLocalUsers(w http.ResponseWriter, r *http.Request) {
	store := rh.localUsers()
	if store == nil {
		writeJSON(w, http.StatusOK, []LocalUserDTO{})
		return
	}
	users, err := store.List(r.Context())
	if err != nil {
		slog.Error("list local users", "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to list users"})
		return
	}
	now := time.Now()
	dtos := make([]LocalUserDTO, 0, len(users))
	for _, u := range users {
		dtos = append(dtos, localUserToDTO(u, now))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// POST /api/v1/org/users — create user (+ optional team membership) and issue
// the first invite.
func (rh *rbacHandler) handleCreateLocalUser(w http.ResponseWriter, r *http.Request) {
	store := rh.localUsers()
	if store == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "local users are not enabled"})
		return
	}
	var req createLocalUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if err := store.CreateUser(r.Context(), req.Username); err != nil {
		switch {
		case errors.Is(err, localuser.ErrExists):
			writeJSON(w, http.StatusConflict, errorResponse{Error: "a user with that username already exists"})
		case errors.Is(err, localuser.ErrReserved):
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "that username is reserved"})
		default:
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		}
		return
	}

	// Optional team membership so the user has a role on first login. Team
	// names that don't exist are skipped (the UI offers only existing teams).
	if len(req.Teams) > 0 {
		if err := rbac.MutateOrg(r.Context(), rh.orgStore, func(o *rbac.Org) error {
			for i := range o.Teams {
				if !slices.Contains(req.Teams, o.Teams[i].Name) {
					continue
				}
				if !slices.Contains(o.Teams[i].Members, req.Username) {
					o.Teams[i].Members = append(o.Teams[i].Members, req.Username)
				}
			}
			return nil
		}); err != nil {
			slog.Error("add local user to teams", "username", req.Username, "error", err)
			// The user exists; team membership can be fixed in Team settings.
		}
	}

	rh.issueInvite(w, r, req.Username)
}

// POST /api/v1/org/users/{username}/invite — re-issue (doubles as password
// reset; older outstanding links die).
func (rh *rbacHandler) handleReinviteLocalUser(w http.ResponseWriter, r *http.Request) {
	store := rh.localUsers()
	if store == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "local users are not enabled"})
		return
	}
	rh.issueInvite(w, r, r.PathValue("username"))
}

func (rh *rbacHandler) issueInvite(w http.ResponseWriter, r *http.Request, username string) {
	token, expires, err := rh.localUsers().IssueInvite(r.Context(), username)
	if err != nil {
		if errors.Is(err, localuser.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "user not found"})
			return
		}
		slog.Error("issue invite", "username", username, "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to issue invite"})
		return
	}
	writeJSON(w, http.StatusOK, localUserInviteResponse{
		Username:    username,
		InviteToken: token,
		ExpiresAt:   expires.UTC().Format(time.RFC3339),
	})
}

// DELETE /api/v1/org/users/{username} — remove the user, any outstanding
// invite, and their team memberships.
func (rh *rbacHandler) handleDeleteLocalUser(w http.ResponseWriter, r *http.Request) {
	store := rh.localUsers()
	if store == nil {
		writeJSON(w, http.StatusNotImplemented, errorResponse{Error: "local users are not enabled"})
		return
	}
	username := r.PathValue("username")
	if err := store.Delete(r.Context(), username); err != nil {
		if errors.Is(err, localuser.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, errorResponse{Error: "user not found"})
			return
		}
		slog.Error("delete local user", "username", username, "error", err)
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "failed to delete user"})
		return
	}
	if err := rbac.MutateOrg(r.Context(), rh.orgStore, func(o *rbac.Org) error {
		for i := range o.Teams {
			o.Teams[i].Members = slices.DeleteFunc(o.Teams[i].Members, func(m string) bool { return m == username })
		}
		return nil
	}); err != nil {
		slog.Error("strip deleted local user from teams", "username", username, "error", err)
		// The credential is gone (auth fails); leftover membership is inert.
	}
	w.WriteHeader(http.StatusNoContent)
}
