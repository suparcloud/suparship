package server

import (
	"context"
	"github.com/suparcloud/suparship/internal/domain"
	"testing"
)

func TestUnpinRetriesPersistedRestoreAfterPublishFailure(t *testing.T) {
	ctx := context.Background()
	store := newMemAppStore()
	store.addApp(&domain.App{Name: "web", ProjectName: "demo", Spec: domain.AppSpec{EnvironmentDefaults: map[string]domain.EnvironmentOverride{"staging": {PinnedImageTag: "main-release"}}}})
	if err := store.SaveAppEnvironment(ctx, "demo", &domain.AppEnvironment{AppName: "web", ProjectName: "demo", EnvName: "staging"}); err != nil {
		t.Fatal(err)
	}
	handler := &appHandler{appStore: store}
	app, env, tag, retry, err := handler.unpinAppEnvSpec(ctx, "demo", "web", "staging")
	if err != nil || !retry || env == nil || tag != "main-release" {
		t.Fatalf("lost pending restore: %v %v %q %v", env, retry, tag, err)
	}
	handler.unpinClearRestoreFlag(ctx, app, "staging", tag)
	_, _, _, retry, err = handler.unpinAppEnvSpec(ctx, "demo", "web", "staging")
	if err != nil || retry {
		t.Fatalf("completed restore retried: %v %v", retry, err)
	}
}
