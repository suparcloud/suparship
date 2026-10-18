package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
)

type fakeTemplateMirror struct {
	calls int
	err   error
}

func (f *fakeTemplateMirror) MirrorTemplateConfig(context.Context) error {
	f.calls++
	return f.err
}

// A registry update mirrors once; a sync does not (rows only); a mirror
// failure never fails the request.
func TestTemplateRegistryHandler_MirrorsOnDesiredStateChangesOnly(t *testing.T) {
	_, ah, client := newTemplateRegistryMuxWithClient(t)
	mirror := &fakeTemplateMirror{}
	mux := http.NewServeMux()
	ah.registerRoutes(mux)
	trh := &templateRegistryHandler{store: tpl.NewRegistryStore(client), auth: ah, kubeClient: client, mirror: mirror, logger: slog.Default()}
	trh.registerRoutes(mux)
	cookie := sessionCookieFor(ah, "admin", "org_admin")

	put := func(body string) int {
		req := httptest.NewRequest("PUT", "/api/v1/templates/registry", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	if code := put(`{"builtIn":[],"external":[{"name":"acme","type":"gitcharts","repoURL":"https://example.com/a.git"}],"sources":[]}`); code != http.StatusOK {
		t.Fatalf("PUT: %d", code)
	}
	if mirror.calls != 1 {
		t.Errorf("registry PUT should mirror once, got %d", mirror.calls)
	}

	// Sync routes never mirror (no engine here → 503, and still no mirror call).
	req := httptest.NewRequest("POST", "/api/v1/templates/registry/sync", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if mirror.calls != 1 {
		t.Errorf("sync must not mirror, calls=%d", mirror.calls)
	}

	// A failing mirror still returns 200 and the cluster state is saved.
	mirror.err = errors.New("git down")
	if code := put(`{"builtIn":[],"external":[],"sources":[]}`); code != http.StatusOK {
		t.Fatalf("PUT with failing mirror: %d", code)
	}
	reg, err := tpl.NewRegistryStore(client).Get(context.Background())
	if err != nil || len(reg.External) != 0 {
		t.Errorf("cluster state must be saved regardless of the mirror: %+v %v", reg, err)
	}
}

// Override PUT and metadata PATCH mirror after their ConfigMap writes.
func TestTemplateHandler_OverrideWritesMirror(t *testing.T) {
	client := fake.NewSimpleClientset()
	if err := kube.SaveTemplate(context.Background(), client, metadataTestTemplate(), nil); err != nil {
		t.Fatal(err)
	}
	mirror := &fakeTemplateMirror{}
	th := &templateHandler{
		kubeClient: client, mirror: mirror, logger: slog.Default(),
		clusterLoader: func(ctx context.Context) ([]*tpl.Template, error) { return kube.LoadTemplates(ctx, client) },
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/templates/voiceai-livekit-agent/overrides", bytes.NewBufferString(`{"defaultValues":{"replicaCount":2}}`))
	req.SetPathValue("name", "voiceai-livekit-agent")
	th.handlePutTemplateOverride(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT overrides: %d %s", rec.Code, rec.Body.String())
	}
	if mirror.calls != 1 {
		t.Errorf("override PUT should mirror once, got %d", mirror.calls)
	}

	disabled := true
	rec = httptest.NewRecorder()
	th.handleUpdateTemplateMetadata(rec, patchReq("voiceai-livekit-agent", templateMetadataPatch{Disabled: &disabled}))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH disabled: %d %s", rec.Code, rec.Body.String())
	}
	if mirror.calls < 2 {
		t.Errorf("metadata PATCH should mirror, calls=%d", mirror.calls)
	}
}
