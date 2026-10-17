package server

import (
	"context"
	"errors"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/rbac"
	"net/http/httptest"
	"strings"
	"testing"
)

type validationSourcePromoter struct {
	selectedReleasePromoter
	source string
}

func (p *validationSourcePromoter) NextVerifiedRelease(_ context.Context, _, _, source, _ string) (string, error) {
	p.source = source
	return "validated-main", nil
}

func TestAutoAndManualPromotionUseValidationWhileStagingPinned(t *testing.T) {
	ah, store, _, _ := autoPromoteFixture(t, true)
	app := storedApp(t, store)
	app.Spec.CD.ReleaseValidationEnvironment = "validation"
	app.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{"staging": {PinnedFrom: "pr-42"}}
	store.addApp(app)
	store.addEnv(&domain.AppEnvironment{ProjectName: testProject, AppName: app.Name, EnvName: "validation", EnvType: domain.AppEnvStaging, Order: 3})
	promoter := &validationSourcePromoter{}
	ah.kargoPromoter = promoter
	ah.argoAppGate = &fakeArgoAppGate{exists: true}
	ah.autoPromoteApp(context.Background(), app)
	if promoter.source != "validation" || promoter.selected != "validated-main" {
		t.Fatalf("wrong release source: %+v", promoter)
	}
	target, _ := store.GetAppEnvironment(context.Background(), testProject, app.Name, "prod")
	source, err := ah.findPromotionSource(context.Background(), testProject, app.Name, target)
	if err != nil || source.EnvName != "validation" {
		t.Fatalf("manual source: %v %v", source, err)
	}
	envs, _ := store.ListAppEnvironments(context.Background(), testProject, app.Name)
	dto := appToDetailDTO(app, envs)
	if dto.CD.PromotionSources["prod"] != "validation" {
		t.Fatalf("UI routing differs: %v", dto.CD.PromotionSources)
	}
}

func TestReleaseValidationRejectsUnboundEnvironment(t *testing.T) {
	ah, store, _, _ := autoPromoteFixture(t, true)
	app := storedApp(t, store)
	app.Spec.CD.ReleaseValidationEnvironment = "validation"
	envs, _ := store.ListAppEnvironments(context.Background(), testProject, app.Name)
	envs = append(envs, &domain.AppEnvironment{EnvName: "validation", EnvType: domain.AppEnvStaging, Order: 3})
	if err := ah.validateReleaseValidation(context.Background(), app, envs); err == nil {
		t.Fatal("unbound environment accepted")
	}
}

func TestValidationSettingLifecycle(t *testing.T) {
	for _, failPublish := range []bool{false, true} {
		t.Run(fmtBool(failPublish), func(t *testing.T) {
			ah, store, _, pipeline := autoPromoteFixture(t, true)
			app := storedApp(t, store)
			app.Spec.Images = []domain.AppImageBinding{{Name: "main", TagKey: "image.tag"}}
			store.addApp(app)
			ah.orgProvider = &countingOrgProvider{org: &rbac.Org{Name: "test", Environments: []rbac.OrgEnvironment{
				{Name: "staging", Order: 1, ClusterRefs: []string{"local"}},
				{Name: "prod", Order: 2, ClusterRefs: []string{"local"}},
				{Name: "validation", Order: 3, ClusterRefs: []string{"local"}},
			}}}
			patch := func(body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("PATCH", "/", strings.NewReader(body))
				r.SetPathValue("project", testProject)
				r.SetPathValue("app", app.Name)
				w := httptest.NewRecorder()
				ah.handleUpdateApp(w, r)
				return w
			}
			if failPublish {
				ah.gitOpsPublisher = &failedValidationPublisher{}
			}
			response := patch(`{"cd":{"managed":true,"autoPromote":true,"releaseValidationEnvironment":"validation"}}`)
			saved, _ := store.GetApp(context.Background(), testProject, app.Name)
			env, envErr := store.GetAppEnvironment(context.Background(), testProject, app.Name, "validation")
			if failPublish {
				if response.Code != 500 || saved.Spec.CD.ReleaseValidationEnvironment != "" || (envErr == nil && env != nil) {
					t.Fatalf("failed publish not rolled back: %d %v %v", response.Code, saved.Spec.CD, env)
				}
				return
			}
			if response.Code != 200 || envErr != nil || saved.Spec.CD.ReleaseValidationEnvironment != "validation" {
				t.Fatalf("enable failed: %d %s", response.Code, response.Body.String())
			}
			response = patch(`{"cd":{"managed":true,"autoPromote":false}}`)
			saved, _ = store.GetApp(context.Background(), testProject, app.Name)
			if response.Code != 200 || saved.Spec.CD.ReleaseValidationEnvironment != "validation" {
				t.Fatal("older client cleared validation setting")
			}
			pipeline.statuses[0].Phase = "Promoting"
			response = patch(`{"cd":{"managed":true,"releaseValidationEnvironment":""}}`)
			if response.Code != 409 {
				t.Fatalf("changed active pipeline: %d %s", response.Code, response.Body.String())
			}
			saved, _ = store.GetApp(context.Background(), testProject, app.Name)
			if saved.Spec.CD.ReleaseValidationEnvironment != "validation" {
				t.Fatal("rejected update mutated stored routing")
			}
			pipeline.statuses[0].Phase = "Steady"
			response = patch(`{"cd":{"managed":true,"releaseValidationEnvironment":""}}`)
			saved, _ = store.GetApp(context.Background(), testProject, app.Name)
			if response.Code != 200 || saved.Spec.CD.ReleaseValidationEnvironment != "" {
				t.Fatal("explicit disable failed")
			}
			if _, err := store.GetAppEnvironment(context.Background(), testProject, app.Name, "validation"); err != nil {
				t.Fatal("disable deleted environment")
			}
		})
	}
}
func fmtBool(value bool) string {
	if value {
		return "publish failure"
	}
	return "enable preserve disable"
}

type failedValidationPublisher struct{ recordingPublisher }

func (*failedValidationPublisher) PublishApp(context.Context, *domain.App, []*domain.AppEnvironment) error {
	return errors.New("publish unavailable")
}

func TestValidationCannotSilentlyDropAcceptanceChecks(t *testing.T) {
	ah, store, _, _ := autoPromoteFixture(t, true)
	app := storedApp(t, store)
	app.Spec.CD.ReleaseValidationEnvironment = "validation"
	app.Spec.CD.Verification = map[string][]string{"staging": {"acceptance"}}
	envs, _ := store.ListAppEnvironments(context.Background(), testProject, app.Name)
	envs = append(envs, &domain.AppEnvironment{EnvName: "validation", EnvType: domain.AppEnvStaging, Order: 3})
	err := ah.validateReleaseValidation(context.Background(), app, envs)
	if err == nil || !strings.Contains(err.Error(), "acceptance checks") {
		t.Fatalf("lost acceptance gate: %v", err)
	}
}
