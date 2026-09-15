package main

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
)

// The restore recreates ONLY what the cluster lost: a missing registry comes
// back (rows empty — the sync rebuilds them), missing overrides come back,
// and existing cluster objects are left exactly as they are.
func TestRestoreTemplateConfigFromMirror(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	// The cluster still has one override, with a value that differs from git.
	if err := kube.SaveTemplateOverride(ctx, client, "kept", &domain.TemplateOverride{DefaultValues: map[string]any{"replicaCount": 9}}); err != nil {
		t.Fatal(err)
	}
	mirror := gitops.TemplateConfigMirror{
		BuiltIn:  []string{},
		External: []tpl.ExternalTemplateRepo{{Name: "acme", Type: "gitcharts", RepoURL: "https://example.com/a.git", Ref: "main", Namespaced: true}},
		Overrides: map[string]*domain.TemplateOverride{
			"kept": {DefaultValues: map[string]any{"replicaCount": 1}},
			"lost": {Disabled: true},
		},
	}

	reg, n, err := restoreTemplateConfigFromMirror(ctx, client, mirror)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !reg || n != 1 {
		t.Errorf("restored registry=%v overrides=%d, want true/1", reg, n)
	}
	got, err := tpl.NewRegistryStore(client).Get(ctx)
	if err != nil {
		t.Fatalf("registry not restored: %v", err)
	}
	if len(got.External) != 1 || got.External[0].Name != "acme" || !got.External[0].Namespaced || len(got.Sources) != 0 {
		t.Errorf("restored registry = %+v", got)
	}
	kept, _ := kube.LoadTemplateOverride(ctx, client, "kept")
	if kept == nil || kept.DefaultValues["replicaCount"] != 9 {
		t.Errorf("existing override must not be overwritten by the mirror: %+v", kept)
	}
	lost, _ := kube.LoadTemplateOverride(ctx, client, "lost")
	if lost == nil || !lost.Disabled {
		t.Errorf("missing override must be restored: %+v", lost)
	}

	// Second run: nothing is missing any more → nothing restored, nothing changed.
	reg, n, err = restoreTemplateConfigFromMirror(ctx, client, mirror)
	if err != nil || reg || n != 0 {
		t.Errorf("idempotent rerun: registry=%v overrides=%d err=%v", reg, n, err)
	}
}
