package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
)

const (
	sessionCookieName = "suparship_session"
	sessionTTL        = 24 * time.Hour
	roleOrgAdmin      = "org_admin"
)

type contextKey string

const sessionCtxKey contextKey = "session"

// authHandler groups the auth-related HTTP handlers and their dependencies.
type authHandler struct {
	authenticator auth.Authenticator
	sessions      *session.Store
	cookieSecure  bool
	// orgProvider supplies the OIDC config (org.Auth.OIDC). Optional; nil
	// disables the SSO endpoints.
	orgProvider rbac.OrgProvider
	// kubeClient reads the OIDC client-secret Secret. Optional; nil disables SSO.
	kubeClient kubernetes.Interface
	// oidcProviders caches discovered *oidc.Provider by issuer URL so each
	// login doesn't re-run discovery.
	oidcProviders sync.Map
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

	sess, err := ah.sessions.Create(creds.Username, roleOrgAdmin)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "session creation failed"})
		return
	}

	http.SetCookie(w, ah.sessionCookie(sess.ID, sess.ExpiresAt))
	writeJSON(w, http.StatusOK, userResponse{
		Username: creds.Username,
		Role:     roleOrgAdmin,
	})
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

// requireAuth wraps a handler, rejecting requests without a valid session.
func (ah *authHandler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

func sessionFromContext(ctx context.Context) *session.Session {
	sess, _ := ctx.Value(sessionCtxKey).(*session.Session)
	return sess
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
