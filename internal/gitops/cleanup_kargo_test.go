package gitops_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

// Switching an app to direct delivery must remove its Kargo Warehouse + Stage
// files and drop its promotion policies from the shared ProjectConfig, while
// leaving a sibling app's Kargo CRs intact.
func TestCleanupKargoCRs_RemovesAppKargoArtifacts(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)

	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true},
	}
	// Real image sources so each app produces a Warehouse (no placeholder).
	valkey := &domain.App{Name: "valkey", ProjectName: "demo", Spec: domain.AppSpec{
		Values: map[string]any{"image_repository": "ghcr.io/demo/valkey"},
	}}
	api := &domain.App{Name: "api", ProjectName: "demo", Spec: domain.AppSpec{ // sibling pipeline app
		Values: map[string]any{"image_repository": "ghcr.io/demo/api"},
	}}

	if err := p.PublishKargoCRsForTest(dir, valkey, envs); err != nil {
		t.Fatalf("publish valkey kargo: %v", err)
	}
	if err := p.PublishKargoCRsForTest(dir, api, envs); err != nil {
		t.Fatalf("publish api kargo: %v", err)
	}

	kargoDir := filepath.Join(dir, "_infra", "kargo")
	mustExist := func(name string) {
		if _, err := os.Stat(filepath.Join(kargoDir, name)); err != nil {
			t.Fatalf("expected %q to exist: %v", name, err)
		}
	}
	mustExist("kargo-demo-valkey-warehouse.yaml")
	mustExist("kargo-demo-valkey-staging-stage.yaml")

	// Switch valkey to direct.
	if err := p.CleanupKargoCRsForTest(dir, valkey); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// valkey's Kargo files are gone.
	for _, name := range []string{
		"kargo-demo-valkey-warehouse.yaml",
		"kargo-demo-valkey-staging-stage.yaml",
		"kargo-demo-valkey-prod-stage.yaml",
	} {
		if _, err := os.Stat(filepath.Join(kargoDir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %q removed, stat err = %v", name, err)
		}
	}

	// The sibling app's Kargo files survive.
	mustExist("kargo-demo-api-warehouse.yaml")
	mustExist("kargo-demo-api-staging-stage.yaml")

	// ProjectConfig no longer references valkey's stages, but keeps api's.
	pcData, err := os.ReadFile(filepath.Join(kargoDir, "kargo-demo-projectconfig.yaml"))
	if err != nil {
		t.Fatalf("read projectconfig: %v", err)
	}
	pc := string(pcData)
	if strings.Contains(pc, "valkey-") {
		t.Errorf("projectconfig still references valkey policies:\n%s", pc)
	}
	if !strings.Contains(pc, "api-") {
		t.Errorf("projectconfig must keep sibling api policies:\n%s", pc)
	}
}

// Cleanup is a no-op (no error) for an app that never had Kargo CRs.
func TestCleanupKargoCRs_NoopWhenNoKargo(t *testing.T) {
	dir := t.TempDir()
	p := newTestPublisher(t)
	app := &domain.App{Name: "fresh", ProjectName: "demo"}
	if err := p.CleanupKargoCRsForTest(dir, app); err != nil {
		t.Fatalf("expected no-op cleanup, got %v", err)
	}
}
