package server

import (
	"context"
	"errors"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/kube"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type selectedReleasePromoter struct {
	recordingPromoter
	selected       string
	reject         bool
	handoffBlocked bool
	handoffErr     error
}

func (p *selectedReleasePromoter) PromoteVerifiedRelease(_ context.Context, _, _, _, _, freight string) (KargoPromotionResult, error) {
	p.selected = freight
	if p.reject {
		return KargoPromotionResult{}, kube.ErrReleaseNotVerified
	}
	return KargoPromotionResult{Name: "promotion", Freight: freight, Phase: "Pending"}, nil
}

func TestSelectedReleaseWithPinnedSource(t *testing.T) {
	for _, reject := range []bool{false, true} {
		pub := &recordingPublisher{}
		promoter := &selectedReleasePromoter{reject: reject}
		mux, auth, store := newTestAppPromoteMuxWithGate(testProject, pub, promoter, &fakeArgoAppGate{exists: true})
		app := promoteTestApp(testProject)
		app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{"staging": {PinnedFrom: "pr-1", PinnedImageTag: "preview"}}
		store.addApp(app)
		seedFullPromotionChain(store, testProject)
		resp := postAppPromoteJSON(mux, sessionCookieFor(auth, "alice", "org_admin"), testProject, "my-app", AppPromoteRequest{TargetEnvironment: "prod", Freight: "verified-old"})
		want := http.StatusOK
		if reject {
			want = http.StatusBadRequest
		}
		if resp.Code != want {
			t.Fatalf("status %d: %s", resp.Code, resp.Body.String())
		}
		if len(pub.publishedEnvs) != 0 {
			t.Fatal("selected release wrote deployable manifests before Kargo")
		}
		if promoter.calls != 0 || promoter.selected != "verified-old" {
			t.Fatal("used legacy promotion")
		}
		stored, _ := store.GetApp(context.Background(), testProject, "my-app")
		if stored.Spec.EnvironmentDefaults["staging"].PinnedFrom != "pr-1" {
			t.Fatal("source was unpinned")
		}
	}
}

func TestVerifiedReleaseRejectsUnavailableTarget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pinned  bool
		exists  bool
		gateErr error
		want    int
	}{
		{name: "pinned target", pinned: true, exists: true, want: http.StatusBadRequest},
		{name: "missing application", want: http.StatusConflict},
		{name: "application read failure", gateErr: errors.New("offline"), want: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub := &recordingPublisher{}
			promoter := &selectedReleasePromoter{}
			mux, auth, store := newTestAppPromoteMuxWithGate(testProject, pub, promoter, &fakeArgoAppGate{exists: tc.exists, err: tc.gateErr})
			app := promoteTestApp(testProject)
			if tc.pinned {
				app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{"prod": {PinnedFrom: "rollback"}}
			}
			store.addApp(app)
			seedFullPromotionChain(store, testProject)
			resp := postAppPromoteJSON(mux, sessionCookieFor(auth, "alice", "org_admin"), testProject, "my-app", AppPromoteRequest{TargetEnvironment: "prod", Freight: "verified-old"})
			if resp.Code != tc.want {
				t.Fatalf("status %d: %s", resp.Code, resp.Body.String())
			}
			if promoter.selected != "" || promoter.calls != 0 || len(pub.publishedEnvs) != 0 {
				t.Fatal("rejected promotion had side effects")
			}
		})
	}
}

func (p *selectedReleasePromoter) VerifiedReleaseCandidates(_ context.Context, _, _, _, _ string) ([]string, error) {
	return []string{"verified-old"}, nil
}

func TestVerifiedReleaseCandidatesRoute(t *testing.T) {
	mux, auth, store := newTestAppPromoteMuxWithGate(testProject, &recordingPublisher{}, &selectedReleasePromoter{}, nil)
	store.addApp(promoteTestApp(testProject))
	seedFullPromotionChain(store, testProject)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+testProject+"/apps/my-app/promotion-candidates?targetEnvironment=prod", nil)
	request.AddCookie(sessionCookieFor(auth, "alice", "org_admin"))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "verified-old") {
		t.Fatalf("%d: %s", response.Code, response.Body.String())
	}
}

func (p *selectedReleasePromoter) NextVerifiedRelease(context.Context, string, string, string, string) (string, error) {
	return "verified-old", nil
}

func TestAutoPromoteUsesHistoryWhileSourcePinned(t *testing.T) {
	ah, store, _, pipeline := autoPromoteFixture(t, true)
	app := storedApp(t, store)
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{"staging": {PinnedFrom: "pr-1"}}
	store.addApp(app)
	pipeline.statuses[0].CurrentFreight = "unverified-preview"
	pipeline.statuses[0].Health = "Unhealthy"
	promoter := &selectedReleasePromoter{}
	ah.kargoPromoter = promoter
	ah.argoAppGate = &fakeArgoAppGate{exists: true}
	ah.autoPromoteApp(context.Background(), app)
	if promoter.selected != "verified-old" {
		t.Fatalf("selected = %q", promoter.selected)
	}
	if promoter.calls != 0 {
		t.Fatal("used current-source legacy promotion")
	}
}

func TestVerificationConfigValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		values  map[string][]string
		invalid bool
	}{
		{"valid", map[string][]string{"staging": {"smoke-tests", "integration.tests"}}, false},
		{"clear", map[string][]string{}, false},
		{"invalid template", map[string][]string{"staging": {"has spaces"}}, true},
		{"duplicate", map[string][]string{"staging": {"smoke", "smoke"}}, true},
		{"invalid env", map[string][]string{"Bad Env": {"smoke"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateVerificationConfig(&CDConfigDTO{Verification: tc.values}); (err != nil) != tc.invalid {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

type preparingPublisher struct {
	recordingPublisher
	gate     *fakeArgoAppGate
	prepared int
}

func (p *preparingPublisher) PrepareAppEnv(_ context.Context, _ *domain.App, _ *domain.AppEnvironment) error {
	p.prepared++
	p.gate.exists = true
	return nil
}

type resolvingPromoter struct{ selectedReleasePromoter }

func (p *resolvingPromoter) ResolveVerifiedRelease(_ context.Context, _, _, _, _, freight string) (string, error) {
	if p.reject {
		return "", kube.ErrReleaseNotVerified
	}
	return freight, nil
}

func TestVerifiedFirstDeploymentThroughPublisherHolder(t *testing.T) {
	for _, reject := range []bool{false, true} {
		gate := &fakeArgoAppGate{}
		pub := &preparingPublisher{gate: gate}
		promoter := &resolvingPromoter{selectedReleasePromoter: selectedReleasePromoter{reject: reject}}
		mux, auth, store := newTestAppPromoteMuxWithGate(testProject, NewPublisherHolder(pub), promoter, gate)
		store.addApp(promoteTestApp(testProject))
		seedFullPromotionChain(store, testProject)
		resp := postAppPromoteJSON(mux, sessionCookieFor(auth, "alice", "org_admin"), testProject, "my-app", AppPromoteRequest{TargetEnvironment: "prod", Freight: "verified-old"})
		want := http.StatusOK
		if reject {
			want = http.StatusBadRequest
		}
		if resp.Code != want {
			t.Fatalf("status %d: %s", resp.Code, resp.Body.String())
		}
		if reject && pub.prepared != 0 {
			t.Fatal("unverified release prepared production")
		}
		if !reject && (pub.prepared != 1 || promoter.selected != "verified-old") {
			t.Fatal("verified first deployment not prepared and queued")
		}
		if len(pub.publishedEnvs) != 0 {
			t.Fatal("used active seed publication")
		}
	}
}

func (p *selectedReleasePromoter) DownstreamSchedulerReady(context.Context, string, string, string) (bool, error) {
	return !p.handoffBlocked, p.handoffErr
}

func TestAutoPromotionWaitsForSchedulerHandoff(t *testing.T) {
	ah, store, _, _ := autoPromoteFixture(t, true)
	app := storedApp(t, store)
	promoter := &selectedReleasePromoter{handoffBlocked: true}
	ah.kargoPromoter = promoter
	ah.argoAppGate = &fakeArgoAppGate{exists: true}
	ah.autoPromoteApp(context.Background(), app)
	if promoter.selected != "" || len(ah.autoPromoteAttempts) != 0 {
		t.Fatal("promoted or consumed cooldown before handoff")
	}
	promoter.handoffBlocked = false
	promoter.handoffErr = errors.New("unavailable")
	ah.autoPromoteApp(context.Background(), app)
	if promoter.selected != "" {
		t.Fatal("promoted on read failure")
	}
	promoter.handoffErr = nil
	ah.autoPromoteApp(context.Background(), app)
	if promoter.selected != "verified-old" {
		t.Fatal("did not automatically resume after handoff")
	}
}
