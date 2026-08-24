package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

func patchComponentEnvJSON(mux *http.ServeMux, cookie *http.Cookie, project, app string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch,
		"/api/v1/projects/"+project+"/apps/"+app, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func envVarsTestApp(projectName string) *domain.App {
	return &domain.App{
		Name:        "my-app",
		ProjectName: projectName,
		Spec: domain.AppSpec{
			Template: domain.AppTemplateRef{Name: "web-service"},
			Components: []domain.ComponentSpec{
				{
					Name: "web", Type: domain.ComponentWeb, Enabled: true,
					Values: map[string]any{"replicaCount": 2},
				},
				{Name: "worker", Type: domain.ComponentWorker, Enabled: true},
			},
		},
	}
}

// The componentEnvVars patch updates ONE component's env-var settings without
// touching anything else — not its own legacy Config, not its siblings.
func TestUpdateApp_ComponentEnvVarsPatch(t *testing.T) {
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, &recordingPublisher{}, nil, nil)
	store.addApp(envVarsTestApp(testProject))
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	off := false
	rec := patchComponentEnvJSON(mux, cookie, testProject, "my-app", map[string]any{
		"componentEnvVars": map[string]ComponentEnvVarsPatchDTO{
			"web": {
				InheritAppVars: &off,
				EnvVars: &[]ComponentEnvVarDTO{
					{Name: "DB_URL", FromSecret: "DATABASE_URL"},
					{Name: "MODE", Value: "worker"},
				},
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	web := app.Spec.Components[0]
	if web.InheritAppVars == nil || *web.InheritAppVars {
		t.Error("inheritAppVars=false not applied")
	}
	if len(web.EnvVars) != 2 || web.EnvVars[0].FromSecret != "DATABASE_URL" {
		t.Errorf("envVars not applied: %+v", web.EnvVars)
	}
	if web.Values["replicaCount"] != 2 {
		t.Error("patch must not touch the component's other fields")
	}
	if app.Spec.Components[1].EnvVars != nil {
		t.Error("sibling component must be untouched")
	}
}

// Inherit-mode source mappings are rejected with 422 (validation), and unknown
// component names with 400 — nothing mutated either way.
func TestUpdateApp_ComponentEnvVarsPatchRejections(t *testing.T) {
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, &recordingPublisher{}, nil, nil)
	store.addApp(envVarsTestApp(testProject))
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	rec := patchComponentEnvJSON(mux, cookie, testProject, "my-app", map[string]any{
		"componentEnvVars": map[string]ComponentEnvVarsPatchDTO{
			"web": {EnvVars: &[]ComponentEnvVarDTO{{Name: "A", FromConfig: "X"}}},
		},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("inherit-mode fromConfig: expected 422, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = patchComponentEnvJSON(mux, cookie, testProject, "my-app", map[string]any{
		"componentEnvVars": map[string]ComponentEnvVarsPatchDTO{"nope": {}},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown component: expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	if app.Spec.Components[0].EnvVars != nil {
		t.Error("rejected requests must not mutate the spec")
	}
}

// A manage-components save (req.Components replaces the list) expresses the
// full component surface in the DTO — nothing legacy to carry forward.
func TestUpdateApp_ComponentsResave(t *testing.T) {
	mux, ah, store := newTestAppPromoteMuxWithGate(testProject, &recordingPublisher{}, nil, nil)
	store.addApp(envVarsTestApp(testProject))
	cookie := sessionCookieFor(ah, "alice", "org_admin")

	rec := patchComponentEnvJSON(mux, cookie, testProject, "my-app", map[string]any{
		"components": []map[string]any{
			{"name": "web", "type": "web", "enabled": true, "exposeMode": "external"},
			{"name": "worker", "type": "worker", "enabled": true},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	app, _ := store.GetApp(context.Background(), testProject, "my-app")
	if len(app.Spec.Components) != 2 || app.Spec.Components[0].ExposeMode != domain.ExposeExternal {
		t.Errorf("components not replaced: %+v", app.Spec.Components)
	}
}
