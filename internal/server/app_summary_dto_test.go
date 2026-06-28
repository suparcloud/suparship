package server

import (
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

// A direct app deployed only to staging must report Healthy (not "not deployed")
// even when the store returns envs in [prod, staging] order — the summary picks
// across deployed stable envs, not the first env in store order.
func TestAppToSummaryDTO_StagingOnlyDirectApp(t *testing.T) {
	app := &domain.App{
		Name:        "lk-sh-web",
		ProjectName: "voiceai",
		Spec:        domain.AppSpec{DeliveryMode: domain.DeliveryDirect},
	}
	// Intentionally out of promotion order (prod first), as K8s/compat may return.
	envs := []*domain.AppEnvironment{
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2,
			Status: domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed}},
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1,
			URLs:   []string{"https://web.staging.example.com"},
			Status: domain.AppRuntimeStatus{Phase: domain.StatusHealthy, Replicas: 2, Available: 2}},
	}

	dto := appToSummaryDTO(app, envs)

	if dto.Status.Phase != domain.StatusHealthy {
		t.Fatalf("aggregate phase = %q, want %q", dto.Status.Phase, domain.StatusHealthy)
	}
	if len(dto.Environments) != 2 {
		t.Fatalf("expected 2 environments, got %d", len(dto.Environments))
	}
	// Sorted into promotion order: staging (base) first.
	if dto.Environments[0].EnvName != "staging" || !dto.Environments[0].IsBase || !dto.Environments[0].Deploy {
		t.Errorf("env[0] = %+v, want staging base+deploy", dto.Environments[0])
	}
	// prod is a non-base higher env of a direct app with no opt-in → not deployed.
	if dto.Environments[1].EnvName != "prod" || dto.Environments[1].Deploy {
		t.Errorf("env[1] = %+v, want prod Deploy=false", dto.Environments[1])
	}
	if len(dto.URLs) != 1 || dto.URLs[0] != "https://web.staging.example.com" {
		t.Errorf("URLs = %v, want only staging URL", dto.URLs)
	}
}

// A pipeline app deployed to every env reports Healthy and all envs Deploy=true.
func TestAppToSummaryDTO_PipelineAllHealthy(t *testing.T) {
	app := &domain.App{Name: "api", ProjectName: "voiceai", Spec: domain.AppSpec{}}
	envs := []*domain.AppEnvironment{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1,
			Status: domain.AppRuntimeStatus{Phase: domain.StatusHealthy, Replicas: 1, Available: 1}},
		{EnvName: "prod", EnvType: domain.AppEnvProd, Order: 2,
			Status: domain.AppRuntimeStatus{Phase: domain.StatusHealthy, Replicas: 1, Available: 1}},
	}
	dto := appToSummaryDTO(app, envs)
	if dto.Status.Phase != domain.StatusHealthy {
		t.Fatalf("phase = %q, want healthy", dto.Status.Phase)
	}
	for _, e := range dto.Environments {
		if !e.Deploy {
			t.Errorf("pipeline env %s should have Deploy=true", e.EnvName)
		}
	}
}

// An app with nothing deployed reports not_deployed.
func TestAppToSummaryDTO_NothingDeployed(t *testing.T) {
	app := &domain.App{Name: "api", ProjectName: "voiceai", Spec: domain.AppSpec{DeliveryMode: domain.DeliveryDirect}}
	envs := []*domain.AppEnvironment{
		{EnvName: "staging", EnvType: domain.AppEnvStaging, Order: 1,
			Status: domain.AppRuntimeStatus{Phase: domain.StatusNotDeployed}},
	}
	if got := appToSummaryDTO(app, envs).Status.Phase; got != domain.StatusNotDeployed {
		t.Fatalf("phase = %q, want not_deployed", got)
	}
}
