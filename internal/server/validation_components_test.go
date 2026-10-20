package server

import (
	"context"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/tpl"
	"strings"
	"testing"
)

func TestComponentValidationSelectionRoundTrip(t *testing.T) {
	no, yes := false, true
	for _, flag := range []*bool{nil, &no, &yes} {
		pub := &updatePublisher{}
		mux, auth, store := newTestAppUpdateMuxWithTemplates(testProject, pub, []*tpl.Template{templateAt("web-service", "2.0.0")})
		store.addApp(pinnedComponentApp(testProject))
		response := patchAppJSON(mux, sessionCookieFor(auth, "alice", "org_admin"), testProject, "my-app", updateAppRequest{Components: []ComponentCreateDTO{{Name: "web", Type: "web", Enabled: true, Template: &ComponentTemplateDTO{Name: "web-service"}, ReleaseValidationEnabled: flag}}})
		if response.Code != 200 {
			t.Fatalf("save: %d %s", response.Code, response.Body.String())
		}
		saved, _ := store.GetApp(context.Background(), testProject, "my-app")
		want := flag == nil || *flag
		if saved.Spec.Components[0].EnabledInReleaseValidation() != want {
			t.Fatal("selection not persisted")
		}
		dto := componentDTOs(saved.Spec.Components, saved.Spec.EnvironmentDefaults)
		if dto[0].EnabledInReleaseValidation != want {
			t.Fatal("effective inclusion missing from response")
		}
	}
}

func TestExcludedStatefulValidationDoesNotRequireDisposablePolicy(t *testing.T) {
	ah, store, _, _ := autoPromoteFixture(t, true)
	app := storedApp(t, store)
	no := false
	app.Spec.CD.ReleaseValidationEnvironment = "validation"
	app.Spec.CD.ReleaseValidationMode = "ephemeral"
	app.Spec.Components = []domain.ComponentSpec{{Name: "web"}, {Name: "redis", Stateful: true, ReleaseValidationEnabled: &no}}
	err := ah.validateReleaseValidation(context.Background(), app, nil)
	// It must progress to environment validation, not reject retained Redis data.
	if err == nil || !strings.Contains(err.Error(), "dedicated namespace") {
		t.Fatalf("excluded database blocked validation: %v", err)
	}
	app.Spec.Components[0].ReleaseValidationEnabled = &no
	if err := ah.validateReleaseValidation(context.Background(), app, nil); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("empty selection accepted: %v", err)
	}
}
