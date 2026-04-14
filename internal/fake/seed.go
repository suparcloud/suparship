package fake

import (
	"time"

	"github.com/suparcloud/suparship/internal/domain"
)

// seedCreatedAt is a fixed timestamp used for all seeded records so output
// is byte-identical across runs (important for snapshot tests and diffs).
var seedCreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// seed populates r with static demo data. Keep values obvious and
// developer-friendly — they will appear in the UI during local development.
func seed(r *DevRuntime) {
	seedOrg(r)
	seedClusters(r)
	seedTemplates(r)
	seedProjects(r)
	seedServices(r)
	seedApps(r)
	seedAppEnvironments(r)
	seedPreviews(r)
	seedStatuses(r)
	seedLogs(r)
}

func seedOrg(r *DevRuntime) {
	r.org = &domain.Org{
		Name:        "default",
		DisplayName: "My Organization",
		CreatedAt:   seedCreatedAt,
	}
}

func seedClusters(r *DevRuntime) {
	for _, c := range []*domain.Cluster{
		{
			Name:        "staging-cluster",
			DisplayName: "Staging",
			// In local dev both envs share the single kind/k3d cluster.
			// The API server URL is placeholder only; the fake never dials it.
			APIServer: "https://kubernetes.default.svc",
			Status:    "ready",
		},
		{
			Name:        "prod-cluster",
			DisplayName: "Production",
			APIServer:   "https://kubernetes.default.svc",
			Status:      "ready",
		},
	} {
		r.clusters[c.Name] = c
	}
}

func seedTemplates(r *DevRuntime) {
	for _, t := range []*domain.Template{
		{
			Name:        "web-service",
			Version:     "1.0.0",
			Title:       "Web Service",
			Description: "Deploy a containerized HTTP service with ingress.",
			Category:    "web",
		},
		{
			Name:        "worker",
			Version:     "1.0.0",
			Title:       "Worker",
			Description: "Deploy a long-running background worker process.",
			Category:    "worker",
		},
		{
			Name:        "cron-job",
			Version:     "1.0.0",
			Title:       "Cron Job",
			Description: "Run a containerized task on a cron schedule.",
			Category:    "cron",
		},
		{
			Name:        "color-app",
			Version:     "1.0.0",
			Title:       "Color App (Demo)",
			Description: "A demo app that displays a color — for Kargo promotion demos.",
			Category:    "demo",
		},
	} {
		r.templates[t.Name] = t
	}
}

func seedProjects(r *DevRuntime) {
	demo := &domain.Project{
		Name:        "demo",
		DisplayName: "Demo Project",
		Description: "Explore suparShip with a pre-seeded project.",
		Environments: []domain.Environment{
			{
				Name:             "staging",
				DisplayName:      "Staging",
				Order:            1,
				ClusterRef:       "staging-cluster",
				BaseDomain:       "localhost",
				NamespacePattern: "{app}-{env}",
			},
			{
				Name:             "prod",
				DisplayName:      "Production",
				Order:            2,
				ClusterRef:       "prod-cluster",
				BaseDomain:       "localhost",
				NamespacePattern: "{app}-{env}",
			},
		},
	}
	r.projects[demo.Name] = demo
	r.services[demo.Name] = make(map[string]*domain.Service)
}

func seedServices(r *DevRuntime) {
	hello := &domain.Service{
		Name:         "hello",
		ProjectName:  "demo",
		TemplateName: "web-service",
		DisplayName:  "Hello Service",
		Description:  "A simple hello-world HTTP service.",
	}
	r.services["demo"][hello.Name] = hello
}

func seedApps(r *DevRuntime) {
	hello := &domain.App{
		Name:        "hello",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			DisplayName: "Hello App",
			Description: "A simple hello-world web application.",
			Template: domain.AppTemplateRef{
				Name:    "web-service",
				Version: "1.0.0",
			},
			Values: map[string]any{
				"image_repository": "ghcr.io/suparcloud/hello",
				"image_tag":        "v1.0.0",
			},
			Components: []domain.ComponentSpec{
				{
					Name:           "web",
					Type:           domain.ComponentWeb,
					Enabled:        true,
					Expose:         true,
					PreviewEnabled: true,
				},
			},
		},
	}

	// api-gateway demonstrates a multi-component app (web + worker + cron).
	apiGateway := &domain.App{
		Name:        "api-gateway",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			DisplayName: "API Gateway",
			Description: "REST API with background worker and scheduled jobs.",
			Template: domain.AppTemplateRef{
				Name:    "web-service",
				Version: "1.0.0",
			},
			Values: map[string]any{
				"image_repository": "ghcr.io/suparcloud/api-gateway",
				"image_tag":        "v2.3.1",
				"port":             "3000",
			},
			Components: []domain.ComponentSpec{
				{
					Name:           "web",
					Type:           domain.ComponentWeb,
					Enabled:        true,
					Expose:         true,
					PreviewEnabled: true,
				},
				{
					Name:           "worker",
					Type:           domain.ComponentWorker,
					Enabled:        true,
					PreviewEnabled: false,
				},
				{
					Name:           "scheduler",
					Type:           domain.ComponentCron,
					Enabled:        true,
					PreviewEnabled: false,
				},
			},
		},
	}

	colorApp := &domain.App{
		Name:        "color-app",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			DisplayName: "Color App",
			Description: "Demo app that displays a color — for Kargo promotion demos.",
			Template: domain.AppTemplateRef{
				Name:    "color-app",
				Version: "1.0.0",
			},
			Values: map[string]any{
				"image_repository": "kind-registry:5000/demo/color-app",
				"image_tag":        "0.1.0",
				"color":            "blue",
			},
			Components: []domain.ComponentSpec{
				{
					Name:           "web",
					Type:           domain.ComponentWeb,
					Enabled:        true,
					Expose:         true,
					PreviewEnabled: true,
				},
			},
		},
	}

	r.apps["demo"] = map[string]*domain.App{
		hello.Name:      hello,
		apiGateway.Name: apiGateway,
		colorApp.Name:   colorApp,
	}
}

func seedAppEnvironments(r *DevRuntime) {
	lastDeployed := seedCreatedAt.Format("2006-01-02T15:04:05Z")

	healthyFull := domain.AppRuntimeStatus{
		Phase:        domain.StatusHealthy,
		Replicas:     2,
		Available:    2,
		LastDeployed: lastDeployed,
	}
	healthyPreview := domain.AppRuntimeStatus{
		Phase:        domain.StatusHealthy,
		Replicas:     1,
		Available:    1,
		LastDeployed: lastDeployed,
	}
	progressingStatus := domain.AppRuntimeStatus{
		Phase:        domain.StatusProgressing,
		Replicas:     3,
		Available:    2,
		LastDeployed: lastDeployed,
	}

	helloRel := &domain.AppReleaseRef{
		Image: "ghcr.io/suparcloud/hello:v1.0.0",
		Tag:   "v1.0.0",
	}
	gwRel := &domain.AppReleaseRef{
		Image: "ghcr.io/suparcloud/api-gateway:v2.3.1",
		Tag:   "v2.3.1",
	}

	helloEnvs := []*domain.AppEnvironment{
		{
			AppName:     "hello",
			ProjectName: "demo",
			EnvName:     "staging",
			EnvType:     domain.AppEnvStaging,
			Namespace:   "hello-staging",
			Release:     helloRel,
			URLs:        []string{"http://hello.staging.localhost"},
			Status:      healthyFull,
		},
		{
			AppName:     "hello",
			ProjectName: "demo",
			EnvName:     "prod",
			EnvType:     domain.AppEnvProd,
			Namespace:   "hello-prod",
			Release:     helloRel,
			URLs:        []string{"http://hello.prod.localhost"},
			Status:      healthyFull,
		},
		{
			AppName:     "hello",
			ProjectName: "demo",
			EnvName:     "pr-42",
			EnvType:     domain.AppEnvPreview,
			Namespace:   "hello-pr-42",
			Release:     helloRel,
			URLs:        []string{"http://pr-42.hello.preview.localhost"},
			Status:      healthyPreview,
		},
	}

	gwEnvs := []*domain.AppEnvironment{
		{
			AppName:     "api-gateway",
			ProjectName: "demo",
			EnvName:     "staging",
			EnvType:     domain.AppEnvStaging,
			Namespace:   "api-gateway-staging",
			Release:     gwRel,
			URLs:        []string{"http://api-gateway.staging.localhost"},
			Status:      progressingStatus,
		},
		{
			AppName:     "api-gateway",
			ProjectName: "demo",
			EnvName:     "prod",
			EnvType:     domain.AppEnvProd,
			Namespace:   "api-gateway-prod",
			Release:     gwRel,
			URLs:        []string{"http://api-gateway.prod.localhost"},
			Status:      healthyFull,
		},
	}

	colorRel := &domain.AppReleaseRef{
		Image: "kind-registry:5000/demo/color-app:0.1.0",
		Tag:   "0.1.0",
	}
	colorEnvs := []*domain.AppEnvironment{
		{
			AppName:     "color-app",
			ProjectName: "demo",
			EnvName:     "staging",
			EnvType:     domain.AppEnvStaging,
			Namespace:   "color-app-staging",
			Release:     colorRel,
			URLs:        []string{"http://color-app.staging.localhost:8880"},
			Status:      healthyFull,
		},
		{
			AppName:     "color-app",
			ProjectName: "demo",
			EnvName:     "prod",
			EnvType:     domain.AppEnvProd,
			Namespace:   "color-app-prod",
			Release:     colorRel,
			URLs:        []string{"http://color-app.prod.localhost:8880"},
			Status:      healthyFull,
		},
	}

	r.appEnvs["demo"] = map[string]map[string]*domain.AppEnvironment{
		"hello":       {},
		"api-gateway": {},
		"color-app":   {},
	}
	for _, e := range helloEnvs {
		r.appEnvs["demo"]["hello"][e.EnvName] = e
	}
	for _, e := range gwEnvs {
		r.appEnvs["demo"]["api-gateway"][e.EnvName] = e
	}
	for _, e := range colorEnvs {
		r.appEnvs["demo"]["color-app"][e.EnvName] = e
	}
}

func seedPreviews(r *DevRuntime) {
	for _, p := range []*domain.Preview{
		{
			Name:        "pr-42",
			ProjectName: "demo",
			ServiceName: "hello",
			Namespace:   "demo-preview-pr-42",
			Status:      domain.StatusHealthy,
			URL:         "http://hello.demo-preview-pr-42.localhost",
			CreatedAt:   seedCreatedAt,
		},
	} {
		r.previews[p.Name] = p
	}
}

func seedStatuses(r *DevRuntime) {
	for _, s := range []*domain.ServiceStatus{
		{
			Status:       domain.StatusHealthy,
			Environment:  "staging",
			Replicas:     2,
			Available:    2,
			Image:        "ghcr.io/suparcloud/hello:v1.0.0",
			IngressURLs:  []string{"http://hello.staging.demo.localhost"},
			Namespace:    "demo-staging",
			LastDeployed: "2026-01-01T00:00:00Z",
		},
		{
			Status:       domain.StatusHealthy,
			Environment:  "prod",
			Replicas:     2,
			Available:    2,
			Image:        "ghcr.io/suparcloud/hello:v1.0.0",
			IngressURLs:  []string{"http://hello.prod.demo.localhost"},
			Namespace:    "demo-prod",
			LastDeployed: "2026-01-01T00:00:00Z",
		},
	} {
		r.statuses[statusKey("demo", "hello", s.Environment)] = s
	}
}

func seedLogs(r *DevRuntime) {
	r.logLines = []domain.LogLine{
		{Timestamp: "2026-01-01T00:00:00Z", Text: `INFO  starting hello service...`, Pod: "hello-abc12", Container: "hello"},
		{Timestamp: "2026-01-01T00:00:01Z", Text: `INFO  listening on :8080`, Pod: "hello-abc12", Container: "hello"},
		{Timestamp: "2026-01-01T00:00:02Z", Text: `INFO  GET /healthz → 200 OK (0ms)`, Pod: "hello-abc12", Container: "hello"},
		{Timestamp: "2026-01-01T00:00:03Z", Text: `INFO  GET / → 200 OK (2ms)`, Pod: "hello-abc12", Container: "hello"},
		{Timestamp: "2026-01-01T00:00:04Z", Text: `INFO  GET /api/items → 200 OK (5ms)`, Pod: "hello-abc12", Container: "hello"},
		{Timestamp: "2026-01-01T00:00:05Z", Text: `INFO  GET /healthz → 200 OK (0ms)`, Pod: "hello-abc12", Container: "hello"},
		{Timestamp: "2026-01-01T00:00:06Z", Text: `INFO  GET /api/items → 200 OK (4ms)`, Pod: "hello-abc12", Container: "hello"},
		{Timestamp: "2026-01-01T00:00:07Z", Text: `WARN  slow query detected (120ms)`, Pod: "hello-abc12", Container: "hello"},
		{Timestamp: "2026-01-01T00:00:08Z", Text: `INFO  GET /healthz → 200 OK (0ms)`, Pod: "hello-abc12", Container: "hello"},
		{Timestamp: "2026-01-01T00:00:09Z", Text: `INFO  GET /api/items → 200 OK (6ms)`, Pod: "hello-abc12", Container: "hello"},
	}
}
