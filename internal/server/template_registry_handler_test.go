package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/registrysync"
)

func newTemplateRegistryMux(t *testing.T) (*http.ServeMux, *authHandler) {
	t.Helper()
	mux, ah, _ := newTemplateRegistryMuxWithClient(t)
	return mux, ah
}

func newTemplateRegistryMuxWithClient(t *testing.T) (*http.ServeMux, *authHandler, *kubefake.Clientset) {
	t.Helper()
	client := kubefake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "suparship-system"}},
	)

	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
		cookieSecure:  false,
	}
	ah.registerRoutes(mux)

	trh := &templateRegistryHandler{
		store:      tpl.NewRegistryStore(client),
		auth:       ah,
		kubeClient: client,
		logger:     slog.Default(),
	}
	trh.registerRoutes(mux)

	return mux, ah, client
}

func TestTemplateRegistryHandler_GetEmpty(t *testing.T) {
	mux, ah := newTemplateRegistryMux(t)

	req := httptest.NewRequest("GET", "/api/v1/templates/registry", nil)
	req.AddCookie(sessionCookieFor(ah, "admin", "org_admin"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp templateRegistryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Configured {
		t.Error("expected configured=false for fresh store")
	}
}

func TestTemplateRegistryHandler_PutAndGet(t *testing.T) {
	mux, ah := newTemplateRegistryMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	body := `{
		"builtIn": ["web-service", "color-app"],
		"sources": [
			{"name": "web-service", "origin": "builtin", "version": "1.0.0"},
			{"name": "color-app", "origin": "builtin", "version": "1.0.0"}
		]
	}`

	putReq := httptest.NewRequest("PUT", "/api/v1/templates/registry", bytes.NewBufferString(body))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, putReq)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	getReq := httptest.NewRequest("GET", "/api/v1/templates/registry", nil)
	getReq.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, getReq)

	var resp templateRegistryResponse
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Configured {
		t.Error("expected configured=true after PUT")
	}
	if len(resp.Registry.Sources) != 2 {
		t.Errorf("sources count = %d, want 2", len(resp.Registry.Sources))
	}
}

func TestTemplateRegistryHandler_ListSources(t *testing.T) {
	mux, ah := newTemplateRegistryMux(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	body := `{
		"builtIn": ["web-service"],
		"sources": [
			{"name": "web-service", "origin": "builtin", "version": "1.0.0"}
		]
	}`
	putReq := httptest.NewRequest("PUT", "/api/v1/templates/registry", bytes.NewBufferString(body))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, putReq)

	if w.Code != http.StatusOK {
		t.Fatalf("PUT setup failed: %d", w.Code)
	}

	getReq := httptest.NewRequest("GET", "/api/v1/templates/sources", nil)
	getReq.AddCookie(cookie)
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, getReq)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}

	var resp templateSourcesResponse
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Sources) != 1 {
		t.Errorf("sources count = %d, want 1", len(resp.Sources))
	}
	if resp.Sources[0].Name != "web-service" {
		t.Errorf("source name = %q, want web-service", resp.Sources[0].Name)
	}
}

func TestTemplateRegistryHandler_Unauthenticated(t *testing.T) {
	mux, _ := newTemplateRegistryMux(t)

	req := httptest.NewRequest("GET", "/api/v1/templates/registry", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// Removing an external source from the registry must also drop the Sources
// rows it imported and the template ConfigMaps behind them — otherwise the
// deleted source keeps "owning" its template names and every later source
// shipping the same charts is refused on sync.
func TestTemplateRegistryHandler_PutPrunesRemovedSource(t *testing.T) {
	mux, ah, client := newTemplateRegistryMuxWithClient(t)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	// Cluster state left behind by the (typo-named) source's last sync.
	for _, name := range []string{"worker", "cronjob"} {
		_, err := client.CoreV1().ConfigMaps("suparship-system").Create(t.Context(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "suparship-template-" + name,
				Labels: map[string]string{"suparship.io/template-name": name},
			},
		}, metav1.CreateOptions{})
		if err != nil {
			t.Fatalf("seed configmap %s: %v", name, err)
		}
	}
	_, err := client.CoreV1().ConfigMaps("suparship-system").Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "suparship-template-web",
			Labels: map[string]string{"suparship.io/template-name": "web"},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed configmap web: %v", err)
	}

	// The UI sends back the registry it loaded with the source removed from
	// external[] but every sources[] row still present.
	body := `{
		"builtIn": [],
		"external": [{"name": "platform-templates", "repoURL": "https://example.com/t.git", "ref": "main", "path": "charts"}],
		"sources": [
			{"name": "web", "origin": "external", "externalRepo": "platform-templates"},
			{"name": "worker", "origin": "external", "externalRepo": "platfrom-templates"},
			{"name": "cronjob", "origin": "external", "externalRepo": "platfrom-templates"},
			{"name": "byo", "origin": "cluster"}
		]
	}`
	putReq := httptest.NewRequest("PUT", "/api/v1/templates/registry", bytes.NewBufferString(body))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, putReq)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp templateRegistryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var names []string
	for _, s := range resp.Registry.Sources {
		names = append(names, s.Name)
	}
	if len(names) != 2 || names[0] != "web" || names[1] != "byo" {
		t.Fatalf("sources after PUT = %v, want [web byo]", names)
	}

	// Ghost rows' ConfigMaps are gone; the live source's and unrelated ones stay.
	for name, wantGone := range map[string]bool{"worker": true, "cronjob": true, "web": false} {
		_, err := client.CoreV1().ConfigMaps("suparship-system").Get(t.Context(), "suparship-template-"+name, metav1.GetOptions{})
		gone := err != nil
		if gone != wantGone {
			t.Errorf("configmap %s gone=%v, want %v (err=%v)", name, gone, wantGone, err)
		}
	}
}

// fakeRepinner records the renames the namespace action asked for.
type fakeRepinner struct {
	renames map[string]string
}

func (f *fakeRepinner) RepinTemplates(_ context.Context, renames map[string]string) ([]TemplateRepinApp, []TemplateRepinFailure) {
	f.renames = renames
	return []TemplateRepinApp{{Project: "demo", App: "hello"}}, nil
}

func gitChartRepo(t *testing.T, charts ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for _, c := range charts {
		p := filepath.Join(dir, "charts", c)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "Chart.yaml"), []byte("apiVersion: v2\nname: "+c+"\nversion: 1.0.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

// Namespacing an existing source renames its templates to "<source>.<chart>",
// carries org overrides over, rewrites app pins through the repinner and
// removes the bare-named template entries — in that order.
func TestTemplateRegistryHandler_NamespaceSource(t *testing.T) {
	mux, ah, client := newTemplateRegistryMuxWithClient(t)
	repo := gitChartRepo(t, "web")
	rep := &fakeRepinner{}
	// Reach into the handler registered by the harness: rebuild the mux with
	// an engine + repinner wired in.
	mux = http.NewServeMux()
	ah.registerRoutes(mux)
	trh := &templateRegistryHandler{
		store:      tpl.NewRegistryStore(client),
		auth:       ah,
		engine:     &registrysync.Engine{Client: client},
		kubeClient: client,
		repinner:   rep,
		logger:     slog.Default(),
	}
	trh.registerRoutes(mux)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	// Registry: one bare-named gitcharts source that owns "web".
	body := `{"builtIn":[],"external":[{"name":"acme","type":"gitcharts","repoURL":` + strconv.Quote(repo) + `,"ref":"main"}],
		"sources":[{"name":"web","origin":"external","externalRepo":"acme"}]}`
	putReq := httptest.NewRequest("PUT", "/api/v1/templates/registry", bytes.NewBufferString(body))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, putReq)
	if w.Code != http.StatusOK {
		t.Fatalf("seed PUT: %d %s", w.Code, w.Body.String())
	}
	// Cluster state from the source's earlier bare-named sync + an org override.
	if _, err := client.CoreV1().ConfigMaps("suparship-system").Create(t.Context(), &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "suparship-template-web", Labels: map[string]string{"suparship.io/template-name": "web"}},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := kube.SaveTemplateOverride(t.Context(), client, "web", &domain.TemplateOverride{DefaultValues: map[string]any{"replicas": 2}}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("POST", "/api/v1/templates/registry/sources/acme/namespace", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("namespace: %d %s", w.Code, w.Body.String())
	}
	var resp templateNamespaceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Templates) != 1 || resp.Templates[0].From != "web" || resp.Templates[0].To != "acme.web" {
		t.Fatalf("renames = %+v, want web → acme.web", resp.Templates)
	}
	if rep.renames["web"] != "acme.web" {
		t.Errorf("repinner renames = %v, want web → acme.web", rep.renames)
	}
	if len(resp.Apps) != 1 || len(resp.Failures) != 0 {
		t.Errorf("apps=%v failures=%v", resp.Apps, resp.Failures)
	}

	reg, err := tpl.NewRegistryStore(client).Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reg.External[0].Namespaced {
		t.Error("source should be flagged namespaced")
	}
	var names []string
	for _, s := range reg.Sources {
		names = append(names, s.Name)
	}
	if len(names) != 1 || names[0] != "acme.web" {
		t.Errorf("registry rows = %v, want [acme.web]", names)
	}
	cms := client.CoreV1().ConfigMaps("suparship-system")
	if _, err := cms.Get(t.Context(), "suparship-template-acme.web", metav1.GetOptions{}); err != nil {
		t.Errorf("namespaced template ConfigMap missing: %v", err)
	}
	if _, err := cms.Get(t.Context(), "suparship-template-web", metav1.GetOptions{}); err == nil {
		t.Error("bare-named template ConfigMap should be deleted")
	}
	if ov, err := kube.LoadTemplateOverride(t.Context(), client, "acme.web"); err != nil || ov == nil || ov.DefaultValues["replicas"] != 2 {
		t.Errorf("override not carried over: %v %v", ov, err)
	}
	if ov, _ := kube.LoadTemplateOverride(t.Context(), client, "web"); ov != nil {
		t.Error("old override should be removed")
	}

	// Second call: already namespaced → 409.
	req = httptest.NewRequest("POST", "/api/v1/templates/registry/sources/acme/namespace", nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("second namespace call = %d, want 409", w.Code)
	}
}

func TestOrphanedManagedCreds(t *testing.T) {
	managed := func(name string) tpl.ExternalTemplateRepo {
		return tpl.ExternalTemplateRepo{Name: name, ExistingSecret: "suparship-tpl-credentials-" + name}
	}
	handWired := func(name, secret string) tpl.ExternalTemplateRepo {
		return tpl.ExternalTemplateRepo{Name: name, ExistingSecret: secret}
	}

	tests := []struct {
		name        string
		before      []tpl.ExternalTemplateRepo
		after       []tpl.ExternalTemplateRepo
		wantOrphans []string
	}{
		{
			name:        "managed source removed",
			before:      []tpl.ExternalTemplateRepo{managed("foo"), managed("bar")},
			after:       []tpl.ExternalTemplateRepo{managed("foo")},
			wantOrphans: []string{"bar"},
		},
		{
			name:   "hand-wired secret left alone",
			before: []tpl.ExternalTemplateRepo{handWired("foo", "shared-creds")},
			after:  []tpl.ExternalTemplateRepo{},
		},
		{
			name:        "rename produces an orphan",
			before:      []tpl.ExternalTemplateRepo{managed("old-name")},
			after:       []tpl.ExternalTemplateRepo{managed("new-name")},
			wantOrphans: []string{"old-name"},
		},
		{
			name:   "no change",
			before: []tpl.ExternalTemplateRepo{managed("foo")},
			after:  []tpl.ExternalTemplateRepo{managed("foo")},
		},
		{
			name:   "first save (no before)",
			before: nil,
			after:  []tpl.ExternalTemplateRepo{managed("foo")},
		},
		{
			name:   "removed source with empty existingSecret",
			before: []tpl.ExternalTemplateRepo{{Name: "foo"}},
			after:  []tpl.ExternalTemplateRepo{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := orphanedManagedCreds(tc.before, tc.after)
			if len(got) != len(tc.wantOrphans) {
				t.Fatalf("got %v, want %v", got, tc.wantOrphans)
			}
			for i, name := range tc.wantOrphans {
				if got[i] != name {
					t.Errorf("orphans[%d] = %q, want %q", i, got[i], name)
				}
			}
		})
	}
}
