package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/secrets"
)

// An app-env secrets request for a preview environment must resolve to a
// preview-PR scope in the BASE env's vault — a preview has no vault of its own.
func TestScopeForRequest_PreviewUsesBaseEnvVault(t *testing.T) {
	store := newMemAppStore()
	store.addApp(&domain.App{Name: "lk-sh-express-caller", ProjectName: "voiceai"})
	store.addEnv(&domain.AppEnvironment{
		AppName: "lk-sh-express-caller", ProjectName: "voiceai",
		EnvName: "pr-712", EnvType: domain.AppEnvPreview, BaseEnv: "staging",
	})
	store.addEnv(&domain.AppEnvironment{
		AppName: "lk-sh-express-caller", ProjectName: "voiceai",
		EnvName: "staging", EnvType: domain.AppEnvStaging,
	})
	h := &secretsHandler{appStore: store, logger: slog.Default()}

	newReq := func(env string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		r.SetPathValue("project", "voiceai")
		r.SetPathValue("app", "lk-sh-express-caller")
		r.SetPathValue("env", env)
		return r
	}

	// Preview env → preview-PR scope, vault keyed by the base env.
	scope := h.scopeForRequest(newReq("pr-712"))
	if scope.Kind != secrets.ScopePreviewPR {
		t.Fatalf("kind = %q, want %q", scope.Kind, secrets.ScopePreviewPR)
	}
	if scope.Env != "staging" || scope.Preview != "pr-712" {
		t.Fatalf("scope = %+v, want base env staging + preview pr-712", scope)
	}
	if got := secrets.VaultName(scope); got != secrets.EnvVaultName("staging") {
		t.Fatalf("vault = %q, want the staging env vault", got)
	}

	// A real stable env is unaffected.
	if s := h.scopeForRequest(newReq("staging")); s.Kind != secrets.ScopeEnv || s.Env != "staging" {
		t.Fatalf("stable env scope = %+v, want plain env scope", s)
	}
}
