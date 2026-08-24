package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suparcloud/suparship/internal/fake"
	"github.com/suparcloud/suparship/internal/server"
	"github.com/suparcloud/suparship/internal/tpl"
)

// testHistoryReader is a minimal server.DeploymentHistoryReader so the golden
// path can exercise the deployment-history endpoint without a real ArgoCD.
type testHistoryReader struct{}

func (testHistoryReader) GetAppDeploymentHistory(_ context.Context, _, app, env string) ([]server.DeploymentHistoryEntry, error) {
	return []server.DeploymentHistoryEntry{
		{ID: 1, Revision: "abc1234", DeployedAt: "2026-06-10T00:00:00Z", Path: "charts/web-service/latest"},
	}, nil
}

// newAppServer wires the fully assembled server with the in-memory dev deps
// AND a statically-injected template (there are no on-disk built-ins — every
// template arrives via the registry in production), so the app-model endpoints
// (create/list/promote/logs/history) are enabled.
func newAppServer(t *testing.T) *httptest.Server {
	t.Helper()
	deps := fake.NewDevServerDeps()

	templates := []*tpl.Template{{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: "web-service", Version: "1.0.0"},
		Spec: tpl.TemplateSpec{
			Title:    "Web Service",
			Category: "web",
			Engine:   tpl.Engine{Type: tpl.EngineHelm, Chart: tpl.ChartLocator{Path: "./chart"}},
		},
	}}

	srv := server.New(server.Config{
		Addr:                    ":0",
		Logger:                  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Authenticator:           deps.Authenticator,
		OrgProvider:             deps.OrgProvider,
		ProjectStore:            deps.ProjectStore,
		PreviewStore:            deps.PreviewStore,
		RuntimeProvider:         deps.RuntimeProvider,
		LogsProvider:            deps.LogsProvider,
		AppStore:                deps.AppStore,
		ClusterStore:            deps.ClusterStore,
		Templates:               templates,
		DeploymentHistoryReader: testHistoryReader{},
		CookieSecure:            false,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// req runs an authenticated JSON request and returns the status + decoded body.
func req(t *testing.T, ts *httptest.Server, cookie *http.Cookie, method, path string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, ts.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.AddCookie(cookie)
	resp, err := ts.Client().Do(r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// TestGoldenPath drives the core app lifecycle over the real HTTP API against
// the fully assembled server (fakes + on-disk templates). It does NOT exercise
// a real cluster/ArgoCD — those concerns are covered by the manual acceptance
// checklist in docs/ — but it locks the API contract, RBAC, and handler wiring
// for: login → create app → list/status → edit config → logs → history.
func TestGoldenPath(t *testing.T) {
	ts := newAppServer(t)
	cookie := sessionLogin(t, ts, fake.FakeAdminUsername, fake.FakeAdminPassword)

	const project = "demo"
	const appName = "golden-web"

	// 1. Create an app from the web-service template.
	createBody := map[string]any{
		"name":        appName,
		"displayName": "Golden Web",
		"template":    "web-service",
		"rawValues": map[string]any{
			"image": map[string]any{"repository": "nginx", "tag": "1.27"},
		},
	}
	status, body := req(t, ts, cookie, http.MethodPost, "/api/v1/projects/"+project+"/apps", createBody)
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create app: want 201/200, got %d: %s", status, body)
	}

	// 2. The app shows up in the project's app list.
	status, body = req(t, ts, cookie, http.MethodGet, "/api/v1/projects/"+project+"/apps", nil)
	if status != http.StatusOK {
		t.Fatalf("list apps: want 200, got %d: %s", status, body)
	}
	var list struct {
		Apps []struct {
			Name   string `json:"name"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode app list: %v\n%s", err, body)
	}
	if !hasApp(list.Apps, appName) {
		t.Fatalf("created app %q not in list: %s", appName, body)
	}

	// 3. App detail returns environments + a status phase.
	status, body = req(t, ts, cookie, http.MethodGet, "/api/v1/projects/"+project+"/apps/"+appName, nil)
	if status != http.StatusOK {
		t.Fatalf("get app: want 200, got %d: %s", status, body)
	}
	var detailResp struct {
		App struct {
			Name         string `json:"name"`
			Environments []struct {
				EnvName string `json:"envName"`
			} `json:"environments"`
		} `json:"app"`
	}
	if err := json.Unmarshal(body, &detailResp); err != nil {
		t.Fatalf("decode app detail: %v\n%s", err, body)
	}
	detail := detailResp.App
	if detail.Name != appName {
		t.Errorf("detail name = %q, want %q", detail.Name, appName)
	}
	if len(detail.Environments) == 0 {
		t.Error("app detail should have at least one environment")
	}

	// 4. Edit the app config (PATCH) — change the image tag via the values
	// overlay (the chart's own keys; there are no template inputs).
	patchBody := map[string]any{
		"rawValues": map[string]any{
			"image": map[string]any{"repository": "nginx", "tag": "1.28"},
		},
	}
	status, body = req(t, ts, cookie, http.MethodPatch, "/api/v1/projects/"+project+"/apps/"+appName, patchBody)
	if status != http.StatusOK {
		t.Fatalf("edit app config: want 200, got %d: %s", status, body)
	}

	// 5. Logs for the app (FakeLogsProvider) — use the app's first env.
	firstEnv := "staging"
	if len(detail.Environments) > 0 {
		firstEnv = detail.Environments[0].EnvName
	}
	status, body = req(t, ts, cookie, http.MethodGet, "/api/v1/projects/"+project+"/apps/"+appName+"/logs?environment="+firstEnv, nil)
	if status != http.StatusOK && status != http.StatusNotFound {
		// 404 = "no pods found" (fake returns none for this app) is acceptable;
		// 500 / other would mean the endpoint is broken.
		t.Fatalf("logs: want 200 or 404, got %d: %s", status, body)
	}

	// 6. Deployment history endpoint is wired and returns entries.
	status, body = req(t, ts, cookie, http.MethodGet, "/api/v1/projects/"+project+"/apps/"+appName+"/environments/"+firstEnv+"/history", nil)
	if status != http.StatusOK {
		t.Fatalf("history: want 200, got %d: %s", status, body)
	}
	var hist struct {
		History []struct {
			Revision string `json:"revision"`
		} `json:"history"`
	}
	if err := json.Unmarshal(body, &hist); err != nil {
		t.Fatalf("decode history: %v\n%s", err, body)
	}
	if len(hist.History) == 0 {
		t.Error("expected at least one deployment-history entry")
	}
}

// TestGoldenPath_AuthProviders verifies the public SSO-discovery endpoint is
// wired. In fake mode no OIDC is configured, so it reports disabled — the login
// page uses this to decide whether to show "Sign in with SSO".
func TestGoldenPath_AuthProviders(t *testing.T) {
	ts := newAppServer(t)
	resp, err := ts.Client().Get(ts.URL + "/api/v1/auth/providers")
	if err != nil {
		t.Fatalf("auth providers: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth providers: want 200, got %d", resp.StatusCode)
	}
	var pr struct {
		OIDC struct {
			Enabled bool `json:"enabled"`
		} `json:"oidc"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	if pr.OIDC.Enabled {
		t.Error("OIDC should be disabled in fake mode (no config)")
	}
}

func hasApp(apps []struct {
	Name   string `json:"name"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}, name string) bool {
	for _, a := range apps {
		if a.Name == name {
			return true
		}
	}
	return false
}
