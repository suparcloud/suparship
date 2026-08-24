package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"

	"log/slog"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/localuser"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/token"
)

const (
	sessionCookieName = "suparship_session"
	sessionTTL        = 24 * time.Hour
	roleOrgAdmin      = "org_admin"
)

type contextKey string

const (
	sessionCtxKey contextKey = "session"
	// tokenCtxKey holds the *token.Metadata when a request authenticated via an
	// API token rather than a session cookie. Authorization middleware consults
	// it to scope the request to the token's single project+role grant.
	tokenCtxKey contextKey = "apitoken"
)

// authHandler groups the auth-related HTTP handlers and their dependencies.
type authHandler struct {
	authenticator auth.Authenticator
	sessions      *session.Store
	// tokenStore validates Authorization: Bearer API tokens. Optional; nil
	// disables bearer auth (cookie sessions only).
	tokenStore   token.Store
	cookieSecure bool
	// orgProvider supplies the OIDC config (org.Auth.OIDC). Optional; nil
	// disables the SSO endpoints.
	orgProvider rbac.OrgProvider
	// kubeClient reads the OIDC client-secret Secret. Optional; nil disables SSO.
	kubeClient kubernetes.Interface
	// oidcProviders caches discovered *oidc.Provider by issuer URL so each
	// login doesn't re-run discovery.
	oidcProviders sync.Map
	// localUsers manages invite-provisioned basic-auth users. Optional; nil
	// disables the public invite endpoints (login still works through the
	// authenticator chain).
	localUsers localuser.Store
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type userResponse struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func (ah *authHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", ah.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", ah.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", ah.requireAuth(ah.handleMe))

	// OIDC SSO. Public (no session): the login flow establishes the session.
	// Enabled at runtime only when org.Auth.OIDC.Enabled (checked per request).
	mux.HandleFunc("GET /api/v1/auth/providers", ah.handleAuthProviders)
	mux.HandleFunc("GET /api/v1/auth/oidc/login", ah.handleOIDCLogin)
	mux.HandleFunc("GET /api/v1/auth/oidc/callback", ah.handleOIDCCallback)

	// Invite redemption. Public like the OIDC routes: the flow itself
	// establishes the session (the user sets a password on first use).
	mux.HandleFunc("GET /api/v1/auth/invite/{token}", ah.handleInviteInfo)
	mux.HandleFunc("POST /api/v1/auth/invite/accept", ah.handleInviteAccept)
}

// displayRoleFor resolves the session's DISPLAY role for /auth/me from the
// org's teams/role-bindings; authorization itself is re-derived per request by
// the RBAC middleware. Defaults to viewer when unmatched or the org is
// unreadable.
func (ah *authHandler) displayRoleFor(ctx context.Context, username string, groups []string) string {
	role := "viewer"
	if ah.orgProvider == nil {
		return role
	}
	if org, err := ah.orgProvider.GetOrg(ctx); err == nil && org != nil {
		if eff, found := org.EffectiveRoleForIdentity(username, groups, "*"); found {
			role = string(eff)
		}
	}
	return role
}

func (ah *authHandler) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "username and password required"})
		return
	}

	creds, err := ah.authenticator.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid credentials"})
		case errors.Is(err, auth.ErrNotConfigured):
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "admin credentials not configured"})
		default:
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "authentication error"})
		}
		return
	}

	// Local logins used to hardcode org_admin here — correct for the single
	// break-glass admin, wrong the moment invite-provisioned users exist.
	role := ah.displayRoleFor(r.Context(), creds.Username, nil)
	sess, err := ah.sessions.Create(creds.Username, role)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "session creation failed"})
		return
	}

	http.SetCookie(w, ah.sessionCookie(sess.ID, sess.ExpiresAt))
	writeJSON(w, http.StatusOK, userResponse{
		Username: creds.Username,
		Role:     role,
	})
}

// --- invite redemption (public) ---

// inviteInfoResponse greets the set-password page. Invalid/expired invites
// yield valid=false with no further detail (no token-state oracle).
type inviteInfoResponse struct {
	Valid    bool   `json:"valid"`
	Username string `json:"username,omitempty"`
}

func (ah *authHandler) handleInviteInfo(w http.ResponseWriter, r *http.Request) {
	if ah.localUsers == nil {
		writeJSON(w, http.StatusOK, inviteInfoResponse{Valid: false})
		return
	}
	username, err := ah.localUsers.InviteUsername(r.Context(), r.PathValue("token"))
	if err != nil {
		writeJSON(w, http.StatusOK, inviteInfoResponse{Valid: false})
		return
	}
	writeJSON(w, http.StatusOK, inviteInfoResponse{Valid: true, Username: username})
}

type inviteAcceptRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// handleInviteAccept redeems a one-time invite: sets the user's password and
// logs them straight in (session cookie), so the invite flow ends inside the
// product, not at another login form.
func (ah *authHandler) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	if ah.localUsers == nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "local users are not enabled"})
		return
	}
	var req inviteAcceptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	username, err := ah.localUsers.RedeemInvite(r.Context(), req.Token, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, localuser.ErrWeakPassword):
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		case errors.Is(err, localuser.ErrInvalidInvite):
			writeJSON(w, http.StatusGone, errorResponse{Error: "this invite link is invalid, expired, or already used — ask your admin for a new one"})
		default:
			slog.Error("invite redemption failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "could not complete the invite"})
		}
		return
	}

	role := ah.displayRoleFor(r.Context(), username, nil)
	sess, err := ah.sessions.Create(username, role)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "session creation failed"})
		return
	}
	http.SetCookie(w, ah.sessionCookie(sess.ID, sess.ExpiresAt))
	writeJSON(w, http.StatusOK, userResponse{Username: username, Role: role})
}

func (ah *authHandler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		ah.sessions.Delete(cookie.Value)
	}

	http.SetCookie(w, ah.expiredCookie())
	w.WriteHeader(http.StatusNoContent)
}

func (ah *authHandler) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := sessionFromContext(r.Context())
	writeJSON(w, http.StatusOK, userResponse{
		Username: sess.Username,
		Role:     sess.Role,
	})
}

// requireAuth wraps a handler, rejecting requests that present neither a valid
// session cookie nor a valid API token. An Authorization: Bearer token takes
// precedence; absent that, the session cookie is checked.
func (ah *authHandler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if raw, ok := bearerToken(r); ok {
			ah.authenticateToken(w, r, next, raw)
			return
		}

		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "not authenticated"})
			return
		}

		sess, ok := ah.sessions.Get(cookie.Value)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "not authenticated"})
			return
		}

		ctx := context.WithValue(r.Context(), sessionCtxKey, sess)
		next(w, r.WithContext(ctx))
	}
}

// authenticateToken resolves an API token and, on success, runs next with both
// the token grant and a synthetic session in context. The synthetic session
// (username only) keeps session-reading handlers working for audit/createdBy;
// authorization is driven by the token grant, never by the username's live org
// membership (see requireRole and the org-admin gates).
func (ah *authHandler) authenticateToken(w http.ResponseWriter, r *http.Request, next http.HandlerFunc, raw string) {
	if ah.tokenStore == nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "not authenticated"})
		return
	}
	meta, err := ah.tokenStore.Resolve(r.Context(), raw)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid api token"})
		return
	}
	sess := &session.Session{Username: meta.CreatedBy, Role: meta.Role}
	ctx := context.WithValue(r.Context(), sessionCtxKey, sess)
	ctx = context.WithValue(ctx, tokenCtxKey, &meta)
	next(w, r.WithContext(ctx))
}

// bearerToken extracts a Bearer credential from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	const scheme = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	return strings.TrimSpace(h[len(scheme):]), true
}

func sessionFromContext(ctx context.Context) *session.Session {
	sess, _ := ctx.Value(sessionCtxKey).(*session.Session)
	return sess
}

// tokenFromContext returns the API token grant for a token-authenticated
// request, or nil when the request used a session cookie.
func tokenFromContext(ctx context.Context) *token.Metadata {
	t, _ := ctx.Value(tokenCtxKey).(*token.Metadata)
	return t
}

func (ah *authHandler) sessionCookie(value string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   ah.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (ah *authHandler) expiredCookie() *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   ah.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
