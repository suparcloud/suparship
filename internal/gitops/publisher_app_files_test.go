package gitops_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/gitops"
)

func TestPublishAppFiles_BoundEnvsWriteFiles(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: true, BaseDomain: "localhost"},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	for _, envName := range []string{"staging", "prod"} {
		appYAML := filepath.Join(dir, envName, "demo", "hello", "app.yaml")
		valuesYAML := filepath.Join(dir, envName, "demo", "hello", "values.yaml")
		if _, err := os.Stat(appYAML); os.IsNotExist(err) {
			t.Errorf("expected app.yaml for env %q to exist", envName)
		}
		if _, err := os.Stat(valuesYAML); os.IsNotExist(err) {
			t.Errorf("expected values.yaml for env %q to exist", envName)
		}
	}
}

func TestPublishAppFiles_UnboundEnvSkipped(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: true, BaseDomain: "localhost"},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: false}, // unbound
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	// staging is bound → files must exist
	stagingApp := filepath.Join(dir, "staging", "demo", "hello", "app.yaml")
	if _, err := os.Stat(stagingApp); os.IsNotExist(err) {
		t.Error("expected app.yaml for bound staging env to exist")
	}

	// prod is unbound → files must NOT exist
	prodApp := filepath.Join(dir, "prod", "demo", "hello", "app.yaml")
	if _, err := os.Stat(prodApp); !os.IsNotExist(err) {
		t.Error("unbound prod env should NOT produce app.yaml")
	}
	prodValues := filepath.Join(dir, "prod", "demo", "hello", "values.yaml")
	if _, err := os.Stat(prodValues); !os.IsNotExist(err) {
		t.Error("unbound prod env should NOT produce values.yaml")
	}
}

func TestPublishAppFiles_AllUnboundWritesNothing(t *testing.T) {
	dir := t.TempDir()

	app := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec:        domain.AppSpec{Template: domain.AppTemplateRef{Name: "web-service"}},
	}
	envs := []gitops.AppPublishEnv{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1, Bound: false},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2, Bound: false},
	}

	p := newTestPublisher(t)
	if err := p.PublishAppFilesForTest(dir, app, envs); err != nil {
		t.Fatalf("PublishAppFilesForTest: %v", err)
	}

	// gitops-output directory should not have been created for any env
	for _, envName := range []string{"staging", "prod"} {
		envDir := filepath.Join(dir, envName)
		if _, err := os.Stat(envDir); !os.IsNotExist(err) {
			t.Errorf("unbound env %q should not have produced any output directory", envName)
		}
	}
}
