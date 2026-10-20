package main

import (
	"context"
	"github.com/suparcloud/suparship/internal/domain"
	"reflect"
	"testing"
)

type validationRefreshRecorder struct{ apps, parents, sets []string }

func (r *validationRefreshRecorder) RefreshApps(_ context.Context, _ string, n []string) error {
	r.apps = append(r.apps, n...)
	return nil
}
func (r *validationRefreshRecorder) RefreshAppsByName(_ context.Context, n []string) error {
	r.parents = append(r.parents, n...)
	return nil
}
func (r *validationRefreshRecorder) RefreshAppSets(_ context.Context, n []string) error {
	r.sets = append(r.sets, n...)
	return nil
}
func TestRefreshValidationTargetsManifestOwner(t *testing.T) {
	for _, composed := range []bool{false, true} {
		r := &validationRefreshRecorder{}
		a := &gitOpsPublisherAdapter{argoRefresher: r}
		app := &domain.App{Name: "web", ProjectName: "demo"}
		if composed {
			app.Spec.Components = []domain.ComponentSpec{{Name: "web"}, {Name: "cache"}}
		}
		a.RefreshValidation(context.Background(), app, &domain.AppEnvironment{EnvName: "release-validation"})
		if !reflect.DeepEqual(r.apps, []string{"web"}) {
			t.Fatal("workloads not refreshed")
		}
		if composed {
			if !reflect.DeepEqual(r.parents, []string{"release-validation-composed"}) || len(r.sets) != 0 {
				t.Fatalf("wrong parent: %+v", r)
			}
		} else if !reflect.DeepEqual(r.sets, []string{"release-validation", "release-validation-external"}) || len(r.parents) != 0 {
			t.Fatalf("wrong generators: %+v", r)
		}
	}
}
