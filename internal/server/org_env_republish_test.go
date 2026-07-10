package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
	"log/slog"
)

func TestEnvWorkloadRelocatedAndDeployConfigChanged(t *testing.T) {
	// staging: active c1, additionally bound to c2 (fan-out standby).
	base := rbac.OrgEnvironment{
		Name: "staging", ClusterRefs: []string{"c1", "c2"},
		ActiveClusterRef: "c1", DeployMode: "active",
	}
	cases := []struct {
		name          string
		mut           func(e rbac.OrgEnvironment) rbac.OrgEnvironment
		wantRelocated bool
		wantDeploy    bool
	}{
		{"identical", func(e rbac.OrgEnvironment) rbac.OrgEnvironment { return e }, false, false},
		{"displayName only", func(e rbac.OrgEnvironment) rbac.OrgEnvironment { e.DisplayName = "X"; return e }, false, false},

		// Additive / in-place → not relocating, but deploy-affecting (auto-republish).
		{"add cluster", func(e rbac.OrgEnvironment) rbac.OrgEnvironment {
			e.ClusterRefs = []string{"c1", "c2", "c3"}
			return e
		}, false, true},
		{"remove non-active cluster", func(e rbac.OrgEnvironment) rbac.OrgEnvironment {
			e.ClusterRefs = []string{"c1"} // active c1 stays → home unchanged
			return e
		}, false, true},
		{"deploy mode active->all", func(e rbac.OrgEnvironment) rbac.OrgEnvironment { e.DeployMode = "all"; return e }, false, true},
		{"base domain", func(e rbac.OrgEnvironment) rbac.OrgEnvironment { e.BaseDomain = "acme.com"; return e }, false, true},
		{"order", func(e rbac.OrgEnvironment) rbac.OrgEnvironment { e.Order = 5; return e }, false, true},

		// Relocating → home (cluster or namespace) moves.
		{"switch active", func(e rbac.OrgEnvironment) rbac.OrgEnvironment { e.ActiveClusterRef = "c2"; return e }, true, true},
		{"remove active cluster (fallback moves)", func(e rbac.OrgEnvironment) rbac.OrgEnvironment {
			e.ClusterRefs = []string{"c2"} // c1 gone → EffectiveClusterRef falls to c2
			e.ActiveClusterRef = ""
			return e
		}, true, true},
		{"namespace pattern", func(e rbac.OrgEnvironment) rbac.OrgEnvironment {
			e.AppNamespacePattern = "{project}-{app}-{env}"
			return e
		}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := base
			b.ClusterRefs = append([]string(nil), base.ClusterRefs...)
			after := tc.mut(b)
			if got := envWorkloadRelocated(b, after); got != tc.wantRelocated {
				t.Errorf("envWorkloadRelocated = %v, want %v", got, tc.wantRelocated)
			}
			if got := envDeployConfigChanged(b, after); got != tc.wantDeploy {
				t.Errorf("envDeployConfigChanged = %v, want %v", got, tc.wantDeploy)
			}
		})
	}
}

// signalPublisher records PublishApp calls on a channel so an async fan-out can
// be asserted race-free (channel ops are synchronised).
type signalPublisher struct {
	*recordingPublisher
	published chan string
}

func (p *signalPublisher) PublishApp(_ context.Context, app *domain.App, _ []*domain.AppEnvironment) error {
	p.published <- app.Name
	return nil
}

func newOrgEnvRepublishMux(t *testing.T, org *rbac.Org, pub GitOpsPublisher) (*http.ServeMux, *authHandler) {
	t.Helper()
	mux := http.NewServeMux()
	ah := &authHandler{
		authenticator: &fakeAuthenticator{username: "admin", password: "pass"},
		sessions:      session.NewStore(time.Hour),
	}
	ah.registerRoutes(mux)

	projStore := newMemProjectStore()
	_ = projStore.Save(context.Background(), &project.Project{Metadata: project.ProjectMeta{Name: "api"}})

	appStore := newMemAppStore()
	appStore.addApp(&domain.App{Name: "backend", ProjectName: "api",
		Spec: domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}}})
	appStore.addEnv(&domain.AppEnvironment{AppName: "backend", ProjectName: "api", EnvName: "staging", EnvType: domain.AppEnvStaging})
	appStore.addEnv(&domain.AppEnvironment{AppName: "backend", ProjectName: "api", EnvName: "prod", EnvType: domain.AppEnvProd})

	orgStore := &staticOrgProvider{org: org}
	ech := &envConfigHandler{orgStore: orgStore, projectStore: projStore, appStore: appStore, publisher: pub, logger: slog.Default()}
	rh := &rbacHandler{auth: ah, orgStore: orgStore, projectStore: projStore, envConfigHandler: ech}
	rh.registerRoutes(mux)
	return mux, ah
}

func putOrgEnv(mux *http.ServeMux, ah *authHandler, env, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/api/v1/org/environments/"+env, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookieFor(ah, "alice", "org_admin"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The org-env update splits changes into additive/in-place (auto-republish, no
// warning) vs relocating (warn, no republish).
func TestUpdateOrgEnv_RepublishVsRelocateWarning(t *testing.T) {
	cases := []struct {
		name          string
		body          string
		wantRepublish bool
		wantWarning   bool
	}{
		{"add cluster (additive)", `{"clusterRefs":["c1","c2","c3"]}`, true, false},
		{"base domain (in-place, caveat 2)", `{"baseDomain":"acme.com"}`, true, false},
		{"switch active (relocating)", `{"activeClusterRef":"c2"}`, false, true},
		{"cosmetic displayName", `{"displayName":"Staging 2"}`, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			org := testRBACOrg()
			// staging bound to c1+c2, active c1 — so switching active to c2 is valid.
			org.Environments = []rbac.OrgEnvironment{
				{Name: "staging", DisplayName: "Staging", Order: 1, ClusterRefs: []string{"c1", "c2"}, ActiveClusterRef: "c1"},
				{Name: "prod", DisplayName: "Production", Order: 2, ClusterRefs: []string{"c1"}, ActiveClusterRef: "c1"},
			}
			pub := &signalPublisher{recordingPublisher: &recordingPublisher{}, published: make(chan string, 8)}
			mux, ah := newOrgEnvRepublishMux(t, org, pub)

			rec := putOrgEnv(mux, ah, "staging", tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var resp struct {
				Warning string `json:"warning"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if (resp.Warning != "") != tc.wantWarning {
				t.Errorf("warning present = %v (%q), want %v", resp.Warning != "", resp.Warning, tc.wantWarning)
			}

			if tc.wantRepublish {
				select {
				case name := <-pub.published:
					if name != "backend" {
						t.Errorf("republished %q, want backend", name)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("expected a republish, none fired")
				}
			} else {
				select {
				case name := <-pub.published:
					t.Fatalf("expected no republish, got %q", name)
				case <-time.After(150 * time.Millisecond):
				}
			}
		})
	}
}
