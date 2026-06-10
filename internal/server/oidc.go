package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
)

// oidc.go — OpenID Connect SSO login flow (slice 2). The /auth/oidc/login →
// IdP → /auth/oidc/callback dance establishes a session carrying the user's
// IdP group claims, which the RBAC middleware resolves via
// Org.HasPermissionForIdentity. Config comes from org.Auth.OIDC (set in the
// UI); the client secret is read from a Kubernetes Secret. The local admin
// password login remains as break-glass.

const (
	oidcStateCookie = "suparship_oidc_state"
	oidcNonceCookie = "suparship_oidc_nonce"
	oidcCookieTTL   = 10 * time.Minute
	loginPath       = "/login"
)

// authProvidersResponse tells the login UI which auth methods are available so
// it can show a "Sign in with SSO" button. Public (no session required).
type authProvidersResponse struct {
	OIDC oidcProviderInfo `json:"oidc"`
}

type oidcProviderInfo struct {
	Enabled  bool   `json:"enabled"`
	LoginURL string `json:"loginURL,omitempty"`
}

func (ah *authHandler) handleAuthProviders(w http.ResponseWriter, r *http.Request) {
	info := oidcProviderInfo{}
	if cfg, _ := ah.oidcConfig(r.Context()); cfg != nil && cfg.Enabled {
		info.Enabled = true
		info.LoginURL = "/api/v1/auth/oidc/login"
	}
	writeJSON(w, http.StatusOK, authProvidersResponse{OIDC: info})
}

// handleOIDCLogin starts the auth-code flow: it stores random state + nonce in
// short-lived cookies and redirects the browser to the IdP.
func (ah *authHandler) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	cfg, err := ah.oidcConfig(r.Context())
	if err != nil || cfg == nil {
		http.Redirect(w, r, loginPath+"?error=sso_unavailable", http.StatusFound)
		return
	}
	oauthCfg, _, err := ah.oauth2Config(r.Context(), cfg)
	if err != nil {
		slog.Error("oidc: login init failed", "error", err)
		http.Redirect(w, r, loginPath+"?error=sso_init", http.StatusFound)
		return
	}

	state, nonce := randToken(), randToken()
	http.SetCookie(w, ah.oidcTempCookie(oidcStateCookie, state))
	http.SetCookie(w, ah.oidcTempCookie(oidcNonceCookie, nonce))
	http.Redirect(w, r, oauthCfg.AuthCodeURL(state, oidc.Nonce(nonce)), http.StatusFound)
}

// handleOIDCCallback completes the flow: verify state, exchange the code,
// verify the ID token (incl. nonce), map claims to a username + groups, and
// create a session.
func (ah *authHandler) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	cfg, err := ah.oidcConfig(r.Context())
	if err != nil || cfg == nil {
		http.Redirect(w, r, loginPath+"?error=sso_unavailable", http.StatusFound)
		return
	}

	// CSRF: the state echoed by the IdP must match the cookie we set.
	stateCookie, _ := r.Cookie(oidcStateCookie)
	nonceCookie, _ := r.Cookie(oidcNonceCookie)
	// One-shot cookies — clear them regardless of outcome.
	http.SetCookie(w, ah.expiredNamedCookie(oidcStateCookie))
	http.SetCookie(w, ah.expiredNamedCookie(oidcNonceCookie))
	if stateCookie == nil || r.URL.Query().Get("state") != stateCookie.Value {
		http.Redirect(w, r, loginPath+"?error=sso_state", http.StatusFound)
		return
	}
	if idpErr := r.URL.Query().Get("error"); idpErr != "" {
		slog.Warn("oidc: IdP returned error", "error", idpErr, "description", r.URL.Query().Get("error_description"))
		http.Redirect(w, r, loginPath+"?error=sso_denied", http.StatusFound)
		return
	}

	oauthCfg, provider, err := ah.oauth2Config(r.Context(), cfg)
	if err != nil {
		slog.Error("oidc: callback init failed", "error", err)
		http.Redirect(w, r, loginPath+"?error=sso_init", http.StatusFound)
		return
	}

	token, err := oauthCfg.Exchange(r.Context(), r.URL.Query().Get("code"))
	if err != nil {
		slog.Error("oidc: code exchange failed", "error", err)
		http.Redirect(w, r, loginPath+"?error=sso_exchange", http.StatusFound)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		http.Redirect(w, r, loginPath+"?error=sso_no_id_token", http.StatusFound)
		return
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(r.Context(), rawIDToken)
	if err != nil {
		slog.Error("oidc: ID token verification failed", "error", err)
		http.Redirect(w, r, loginPath+"?error=sso_verify", http.StatusFound)
		return
	}
	if nonceCookie == nil || idToken.Nonce != nonceCookie.Value {
		http.Redirect(w, r, loginPath+"?error=sso_nonce", http.StatusFound)
		return
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		http.Redirect(w, r, loginPath+"?error=sso_claims", http.StatusFound)
		return
	}
	username := claimString(claims, cfg.UsernameClaim)
	if username == "" {
		username = idToken.Subject // fall back to the stable subject identifier
	}
	groups := claimStrings(claims, cfg.GroupsClaim)

	// Resolve a display role for /auth/me; authorization itself is re-derived
	// per request from (username, groups) by the RBAC middleware.
	role := "viewer"
	if org, oerr := ah.orgProvider.GetOrg(r.Context()); oerr == nil && org != nil {
		if eff, found := org.EffectiveRoleForIdentity(username, groups, "*"); found {
			role = string(eff)
		}
	}

	sess, err := ah.sessions.CreateWithGroups(username, role, groups)
	if err != nil {
		http.Redirect(w, r, loginPath+"?error=sso_session", http.StatusFound)
		return
	}
	http.SetCookie(w, ah.sessionCookie(sess.ID, sess.ExpiresAt))
	http.Redirect(w, r, "/", http.StatusFound)
}

// --- helpers ---

// oidcConfig returns the defaulted OIDC config when SSO is enabled, else nil.
func (ah *authHandler) oidcConfig(ctx context.Context) (*rbac.OIDCConfig, error) {
	if ah.orgProvider == nil {
		return nil, nil
	}
	org, err := ah.orgProvider.GetOrg(ctx)
	if err != nil {
		return nil, err
	}
	if org == nil || org.Auth.OIDC == nil || !org.Auth.OIDC.Enabled {
		return nil, nil
	}
	c := org.Auth.OIDC.Defaulted()
	return &c, nil
}

// oauth2Config builds the oauth2.Config + discovered provider for a login.
func (ah *authHandler) oauth2Config(ctx context.Context, cfg *rbac.OIDCConfig) (*oauth2.Config, *oidc.Provider, error) {
	provider, err := ah.oidcProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, nil, fmt.Errorf("oidc discovery for %q: %w", cfg.IssuerURL, err)
	}
	secret, err := ah.oidcClientSecret(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: secret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       cfg.Scopes,
	}, provider, nil
}

// oidcProvider returns a discovered provider, cached by issuer URL.
func (ah *authHandler) oidcProvider(ctx context.Context, issuer string) (*oidc.Provider, error) {
	if v, ok := ah.oidcProviders.Load(issuer); ok {
		return v.(*oidc.Provider), nil
	}
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	ah.oidcProviders.Store(issuer, p)
	return p, nil
}

func (ah *authHandler) oidcClientSecret(ctx context.Context, cfg *rbac.OIDCConfig) (string, error) {
	if ah.kubeClient == nil {
		return "", fmt.Errorf("no Kubernetes client to read the OIDC client secret")
	}
	key := cfg.ClientSecretRef.Key
	if key == "" {
		key = oidcSecretKey
	}
	sec, err := ah.kubeClient.CoreV1().Secrets(secrets.SystemNamespace).Get(ctx, cfg.ClientSecretRef.Name, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read OIDC client secret %q: %w", cfg.ClientSecretRef.Name, err)
	}
	v := string(sec.Data[key])
	if v == "" {
		return "", fmt.Errorf("OIDC client secret %q/%q is empty", cfg.ClientSecretRef.Name, key)
	}
	return v, nil
}

func (ah *authHandler) oidcTempCookie(name, value string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(oidcCookieTTL.Seconds()),
		HttpOnly: true,
		Secure:   ah.cookieSecure,
		SameSite: http.SameSiteLaxMode, // sent on the top-level redirect back from the IdP
	}
}

func (ah *authHandler) expiredNamedCookie(name string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   ah.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// claimString reads a string claim by name.
func claimString(claims map[string]any, name string) string {
	if s, ok := claims[name].(string); ok {
		return s
	}
	return ""
}

// claimStrings reads a groups-style claim, tolerating both a []string-ish array
// and a single string value.
func claimStrings(claims map[string]any, name string) []string {
	switch v := claims[name].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		if v != "" {
			return []string{v}
		}
	}
	return nil
}
