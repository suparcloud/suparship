package server

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

func TestValidationDataPolicyIsExplicit(t *testing.T) {
	ah, store, _, _ := autoPromoteFixture(t, true)
	app := storedApp(t, store)
	app.Spec.Components = []domain.ComponentSpec{{Name: "redis", Stateful: true}}
	for _, policy := range []string{"", "retain", "unknown", "disposable"} {
		app.Spec.Components[0].ReleaseValidationDataPolicy = policy
		err := ah.validateReleaseValidation(context.Background(), app, nil)
		// With validation disabled only invalid spellings fail. Stored protection is unchanged.
		if (err != nil) != (policy == "unknown") {
			t.Fatalf("policy %q: %v", policy, err)
		}
	}
	app.Spec.CD.ReleaseValidationEnvironment = "validation"
	app.Spec.CD.ReleaseValidationMode = "ephemeral"
	app.Spec.Components[0].ReleaseValidationDataPolicy = ""
	if err := ah.validateReleaseValidation(context.Background(), app, nil); err == nil {
		t.Fatal("retained stateful data accepted for ephemeral validation")
	}
}
func TestValidationDataPolicyPatchPreservesOtherComponentsAndRollsBack(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmtBool(fail), func(t *testing.T) {
			pub := &updatePublisher{}
			if fail {
				pub.failPublish = true
			}
			mux, auth, store := newTestAppUpdateMuxWithTemplates(testProject, pub, nil)
			app := pinnedComponentApp(testProject)
			app.Spec.Components = append(app.Spec.Components, domain.ComponentSpec{Name: "redis", Stateful: true})
			store.addApp(app)
			response := patchAppJSON(mux, sessionCookieFor(auth, "alice", "org_admin"), testProject, "my-app", updateAppRequest{ComponentValidationPolicies: map[string]string{"redis": "disposable"}})
			saved, _ := store.GetApp(context.Background(), testProject, "my-app")
			wantStatus, wantPolicy := 200, "disposable"
			if fail {
				wantStatus, wantPolicy = 500, ""
			}
			if response.Code != wantStatus || saved.Spec.Components[1].ReleaseValidationDataPolicy != wantPolicy || saved.Spec.Components[0].Name != app.Spec.Components[0].Name {
				t.Fatalf("status=%d policy=%s", response.Code, saved.Spec.Components[1].ReleaseValidationDataPolicy)
			}
		})
	}
}
