package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

func testTemplates() []*tpl.Template {
	min1, max10 := 1.0, 10.0
	return []*tpl.Template{
		{
			APIVersion: tpl.CurrentAPIVersion,
			Kind:       tpl.TemplateKind,
			Metadata:   tpl.Metadata{Name: "api-service", Version: "1.0.0"},
			Spec: tpl.TemplateSpec{
				Title:    "API Service",
				Category: "web",
				Engine:   tpl.Engine{Type: tpl.EngineHelm, Chart: tpl.ChartLocator{Path: "./chart"}},
				Inputs: []tpl.Input{
					{Name: "image", Title: "Image", Type: tpl.InputTypeString, Required: true},
					{Name: "replicas", Title: "Replicas", Type: tpl.InputTypeNumber, Default: 2, Min: &min1, Max: &max10},
					{Name: "tier", Title: "Tier", Type: tpl.InputTypeEnum, Options: []string{"small", "large"}, Default: "small"},
				},
				AdvancedInputs: []tpl.Input{
					{Name: "debug", Title: "Debug", Type: tpl.InputTypeBoolean},
				},
				SecretInputs: []tpl.SecretInput{
					{Name: "db_url", Title: "Database URL", SecretRef: "db.url"},
				},
				Presets: []tpl.Preset{
					{Name: "starter", Title: "Starter", Values: map[string]any{"replicas": 1}},
				},
			},
		},
		{
			APIVersion: tpl.CurrentAPIVersion,
			Kind:       tpl.TemplateKind,
			Metadata:   tpl.Metadata{Name: "worker", Version: "2.0.0"},
			Spec: tpl.TemplateSpec{
				Title:    "Background Worker",
				Category: "worker",
				Engine:   tpl.Engine{Type: tpl.EngineHelm},
			},
		},
	}
}

func newTestTemplateMux() (*http.ServeMux, *authHandler) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	th := newTemplateHandler(ah, testTemplates(), nil, nil)
	th.registerRoutes(mux)

	return mux, ah
}

// --- GET /api/v1/templates ---

func TestListTemplatesAuthenticated(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp TemplatesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Templates) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(resp.Templates))
	}
	if resp.Templates[0].Name != "api-service" {
		t.Fatalf("expected first template %q, got %q", "api-service", resp.Templates[0].Name)
	}
	if resp.Templates[0].Engine != "helm" {
		t.Fatalf("expected engine %q, got %q", "helm", resp.Templates[0].Engine)
	}
}

func TestListTemplatesUnauthenticated(t *testing.T) {
	mux, _ := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestListTemplatesSummaryShape(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var resp TemplatesResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	for _, tmpl := range resp.Templates {
		if tmpl.Name == "" || tmpl.Title == "" || tmpl.Category == "" || tmpl.Engine == "" {
			t.Fatalf("summary missing required fields: %+v", tmpl)
		}
	}
}

// --- GET /api/v1/templates/{name} ---

func TestGetTemplateDetail(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates/api-service", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp TemplateDetailDTO
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Name != "api-service" {
		t.Fatalf("expected name %q, got %q", "api-service", resp.Name)
	}
	if resp.Version != "1.0.0" {
		t.Fatalf("expected version %q, got %q", "1.0.0", resp.Version)
	}
	if len(resp.Inputs) != 3 {
		t.Fatalf("expected 3 inputs, got %d", len(resp.Inputs))
	}
	if len(resp.AdvancedInputs) != 1 {
		t.Fatalf("expected 1 advanced input, got %d", len(resp.AdvancedInputs))
	}
	if len(resp.SecretInputs) != 1 {
		t.Fatalf("expected 1 secret input, got %d", len(resp.SecretInputs))
	}
	if len(resp.Presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(resp.Presets))
	}
}

func TestGetTemplateDetailInputFields(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates/api-service", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var resp TemplateDetailDTO
	_ = json.NewDecoder(rec.Body).Decode(&resp)

	imageInput := resp.Inputs[0]
	if imageInput.Name != "image" || imageInput.Type != "string" || !imageInput.Required {
		t.Fatalf("image input mismatch: %+v", imageInput)
	}

	replicasInput := resp.Inputs[1]
	if replicasInput.Min == nil || *replicasInput.Min != 1 {
		t.Fatalf("replicas min should be 1, got %v", replicasInput.Min)
	}
	if replicasInput.Max == nil || *replicasInput.Max != 10 {
		t.Fatalf("replicas max should be 10, got %v", replicasInput.Max)
	}

	tierInput := resp.Inputs[2]
	if tierInput.Type != "enum" || len(tierInput.Options) != 2 {
		t.Fatalf("tier input should be enum with 2 options: %+v", tierInput)
	}
}

func TestGetTemplateNotFound(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates/nonexistent", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if resp.Error != "template not found" {
		t.Fatalf("expected 'template not found', got %q", resp.Error)
	}
}

func TestGetTemplateUnauthenticated(t *testing.T) {
	mux, _ := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates/api-service", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestGetTemplateEmptyArraysNotNull(t *testing.T) {
	mux, ah := newTestTemplateMux()

	req := httptest.NewRequest("GET", "/api/v1/templates/worker", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var raw map[string]json.RawMessage
	_ = json.NewDecoder(rec.Body).Decode(&raw)

	for _, field := range []string{"inputs", "advancedInputs", "secretInputs", "presets"} {
		if string(raw[field]) == "null" {
			t.Fatalf("%s should be [] not null", field)
		}
	}
}

func TestGetTemplateDetail_EmptyComponentsArrayNotNull(t *testing.T) {
	// Templates without a components section serialize as [] (not null)
	// so the UI's array iteration doesn't blow up.
	mux, ah := newTestTemplateMux()
	req := httptest.NewRequest("GET", "/api/v1/templates/worker", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var raw map[string]json.RawMessage
	_ = json.NewDecoder(rec.Body).Decode(&raw)
	if string(raw["components"]) == "null" {
		t.Fatal("components should be [] not null when no components declared")
	}
}

// --- Template resolution: cluster overrides built-in ---

// namedTemplate is a tiny builder for resolution/delete tests.
func namedTemplate(name, version string) *tpl.Template {
	return &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata:   tpl.Metadata{Name: name, Version: version},
		Spec: tpl.TemplateSpec{
			Title:  name,
			Engine: tpl.Engine{Type: tpl.EngineHelm},
		},
	}
}

// clusterTemplates returns a ClusterTemplateLoader that always yields ts.
func clusterTemplates(ts ...*tpl.Template) ClusterTemplateLoader {
	return func(context.Context) ([]*tpl.Template, error) {
		return ts, nil
	}
}

func TestResolveTemplates_ClusterOverridesBuiltin(t *testing.T) {
	builtin := []*tpl.Template{namedTemplate("voiceai-agent", "1.0.0")}
	loader := clusterTemplates(namedTemplate("voiceai-agent", "2.0.0"))

	byName, err := ResolveTemplates(context.Background(), builtin, loader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := byName["voiceai-agent"]
	if !ok {
		t.Fatal("voiceai-agent missing from resolved set")
	}
	if got.Metadata.Version != "2.0.0" {
		t.Fatalf("cluster copy should override built-in: version = %q, want 2.0.0", got.Metadata.Version)
	}
}

func TestResolveTemplates_BuiltinFallbackWhenNoClusterCopy(t *testing.T) {
	builtin := []*tpl.Template{namedTemplate("api-service", "1.0.0")}
	// Cluster has a different template; api-service must fall back to built-in.
	loader := clusterTemplates(namedTemplate("voiceai-agent", "2.0.0"))

	byName, err := ResolveTemplates(context.Background(), builtin, loader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := byName["api-service"]
	if !ok {
		t.Fatal("api-service should fall back to the built-in copy")
	}
	if got.Metadata.Version != "1.0.0" {
		t.Fatalf("api-service version = %q, want 1.0.0 (built-in)", got.Metadata.Version)
	}
	if _, ok := byName["voiceai-agent"]; !ok {
		t.Fatal("cluster-only template should also be present")
	}
}

// TestResolveTemplates_FetchErrorReturnsBuiltinsAndError locks in the F3
// contract: a cluster-fetch error is surfaced to the caller, but the built-ins
// are still returned so read endpoints can degrade gracefully.
func TestResolveTemplates_FetchErrorReturnsBuiltinsAndError(t *testing.T) {
	builtin := []*tpl.Template{namedTemplate("api-service", "1.0.0")}
	loader := func(context.Context) ([]*tpl.Template, error) {
		return nil, errors.New("apiserver unavailable")
	}

	byName, err := ResolveTemplates(context.Background(), builtin, loader)
	if err == nil {
		t.Fatal("expected the cluster-fetch error to be surfaced")
	}
	if _, ok := byName["api-service"]; !ok {
		t.Fatal("built-ins should still be returned on fetch error (degrade path)")
	}
}

// --- DELETE /api/v1/templates/{name} ---

// templateDeleteMuxNoClient wires the DELETE route with NO kube client, to
// exercise the disk-only / local-dev path (th.kubeClient left nil).
func templateDeleteMuxNoClient(builtin []*tpl.Template) (*http.ServeMux, *authHandler) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)
	th := newTemplateHandler(ah, builtin, nil, nil) // kubeClient deliberately nil
	th.registerRoutes(mux)
	return mux, ah
}

func TestHandleDelete_BuiltinNoClientReturns409(t *testing.T) {
	// Disk-only mode (nil kube client): a built-in name gets the informative
	// 409, not a generic 503. (F4)
	mux, ah := templateDeleteMuxNoClient([]*tpl.Template{namedTemplate("api-service", "1.0.0")})
	rec := deleteTemplateReq(mux, ah, "api-service")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for built-in with no kube client, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "built-in default") {
		t.Fatalf("error should mention built-in default, got %q", resp.Error)
	}
}

func TestHandleDelete_UnknownNoClientReturns503(t *testing.T) {
	// Non-built-in name with no kube client: can't resolve without a cluster,
	// so 503 is the honest answer.
	mux, ah := templateDeleteMuxNoClient(nil)
	rec := deleteTemplateReq(mux, ah, "whatever")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for unknown name with no kube client, got %d: %s", rec.Code, rec.Body.String())
	}
}

// clusterTemplateCM builds a ConfigMap carrying the template-name label so
// kube.DeleteTemplate's selector finds (and deletes) it.
func clusterTemplateCM(name string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suparship-template-" + name,
			Namespace: "suparship-system",
			Labels: map[string]string{
				"suparship.io/type":          "template",
				"suparship.io/template-name": name,
			},
		},
	}
}

// templateDeleteMux wires a templateHandler with a kube client so the DELETE
// route is active. authMiddleware is left nil → plain auth.
func templateDeleteMux(builtin []*tpl.Template, client *fake.Clientset) (*http.ServeMux, *authHandler) {
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	th := newTemplateHandler(ah, builtin, nil, nil)
	th.kubeClient = client
	th.registerRoutes(mux)
	return mux, ah
}

func deleteTemplateReq(mux *http.ServeMux, ah *authHandler, name string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("DELETE", "/api/v1/templates/"+name, nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleDelete_ClusterCopyDeletableEvenWhenBuiltin(t *testing.T) {
	// voiceai-agent is BOTH a built-in default AND synced into the cluster.
	// Deleting the cluster copy must succeed (204): the built-in stays as the
	// fallback. This is the core of the override fix — a built-in name no
	// longer blocks deleting its synced override.
	builtin := []*tpl.Template{namedTemplate("voiceai-agent", "1.0.0")}
	client := fake.NewSimpleClientset(clusterTemplateCM("voiceai-agent"))

	mux, ah := templateDeleteMux(builtin, client)
	rec := deleteTemplateReq(mux, ah, "voiceai-agent")

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting synced override, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleDelete_PureBuiltinReturns409(t *testing.T) {
	// api-service is a built-in with NO cluster copy: nothing to delete, and
	// the platform can't remove a shipped default → 409 with a clear message.
	builtin := []*tpl.Template{namedTemplate("api-service", "1.0.0")}
	client := fake.NewSimpleClientset() // empty cluster

	mux, ah := templateDeleteMux(builtin, client)
	rec := deleteTemplateReq(mux, ah, "api-service")

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for pure built-in, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp errorResponse
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "built-in default") {
		t.Fatalf("error should mention built-in default, got %q", resp.Error)
	}
}

func TestHandleDelete_UnknownReturns404(t *testing.T) {
	client := fake.NewSimpleClientset() // empty cluster, no built-ins
	mux, ah := templateDeleteMux(nil, client)
	rec := deleteTemplateReq(mux, ah, "nope")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown template, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- semverGreater (PR5.2 helper) ---

func TestSemverGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"2.0.0", "1.9.9", true},
		{"1.10.0", "1.9.0", true},
		{"1.9.0", "1.10.0", false},
		{"1.0.0", "1.0.0", false},
		{"1.0.0-rc.1", "1.0.0-rc.0", false}, // pre-release stripped → equal → false
		{"v1.0.0", "1.0.0", true},           // non-numeric "v" prefix falls back to lex
		{"a.b.c", "1.0.0", true},            // unparseable falls back to lex
	}
	for _, tc := range cases {
		t.Run(tc.a+">"+tc.b, func(t *testing.T) {
			if got := semverGreater(tc.a, tc.b); got != tc.want {
				t.Errorf("semverGreater(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestParseSemVer(t *testing.T) {
	cases := []struct {
		in     string
		want   [3]int
		wantOK bool
	}{
		{"1.2.3", [3]int{1, 2, 3}, true},
		{"0.0.0", [3]int{0, 0, 0}, true},
		{"10.20.30", [3]int{10, 20, 30}, true},
		{"1.2.3-rc.1", [3]int{1, 2, 3}, true},
		{"1.2.3+build.5", [3]int{1, 2, 3}, true},
		{"1.2", [3]int{}, false},
		{"v1.2.3", [3]int{}, false},
		{"abc", [3]int{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseSemVer(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
