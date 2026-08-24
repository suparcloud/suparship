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
	// Mirrors the generic examples/charts catalog the real dev loop imports
	// through the template registry — plain BYO Helm charts, no built-ins.
	for _, t := range []*domain.Template{
		{
			Name:        "web",
			Version:     "1.0.0",
			Title:       "Web",
			Description: "Plain web-service Helm chart: Deployment, Service, optional Ingress.",
			Category:    "web",
		},
		{
			Name:        "worker",
			Version:     "1.0.0",
			Title:       "Worker",
			Description: "Plain background-worker Helm chart (headless Deployment).",
			Category:    "worker",
		},
		{
			Name:        "cronjob",
			Version:     "1.0.0",
			Title:       "Cron Job",
			Description: "Plain CronJob Helm chart for scheduled tasks.",
			Category:    "cron",
		},
		{
			Name:        "postgres",
			Version:     "1.0.0",
			Title:       "Postgres",
			Description: "Single-instance PostgreSQL for demo stacks (stateful component).",
			Category:    "database",
		},
	} {
		r.templates[t.Name] = t
	}
}

func seedProjects(r *DevRuntime) {
	demo := &domain.Project{
		Name:        "demo",
		DisplayName: "Demo Project",
		Description: "Explore suparship with a pre-seeded project.",
		Environments: []domain.Environment{
			{
				Name:             "staging",
				DisplayName:      "Staging",
				Order:            1,
				ClusterRefs:      []string{"staging-cluster"},
				ActiveClusterRef: "staging-cluster",
				BaseDomain:       "localhost",
				NamespacePattern: "{app}-{env}",
			},
			{
				Name:             "prod",
				DisplayName:      "Production",
				Order:            2,
				ClusterRefs:      []string{"prod-cluster"},
				ActiveClusterRef: "prod-cluster",
				BaseDomain:       "localhost",
				NamespacePattern: "{app}-{env}",
			},
		},
	}
	r.projects[demo.Name] = demo
	r.services[demo.Name] = make(map[string]*domain.Service)
}

func seedServices(r *DevRuntime) {
	notes := &domain.Service{
		Name:         "notes-web",
		ProjectName:  "demo",
		TemplateName: "web",
		DisplayName:  "Notes Web",
		Description:  "A simple notes web application.",
	}
	r.services["demo"][notes.Name] = notes
}

func seedApps(r *DevRuntime) {
	// notes-web: a plain single-chart app on the generic `web` chart. All
	// workload shape lives in the chart's own values (RawValues overlay).
	notesWeb := &domain.App{
		Name:        "notes-web",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			DisplayName: "Notes Web",
			Description: "A simple notes web application.",
			Template: domain.AppTemplateRef{
				Name:    "web",
				Version: "1.0.0",
			},
			RawValues: map[string]any{
				"image":         map[string]any{"repository": "ghcr.io/suparcloud/notes-web", "tag": "v1.0.0"},
				"containerPort": 8080,
			},
			Images:          []domain.AppImageBinding{{Name: "web", TagKey: "image.tag"}},
			PreviewsEnabled: true,
		},
	}

	// api-gateway demonstrates a COMPOSED app: three components, each its own
	// generic chart source with its own values.
	apiGateway := &domain.App{
		Name:        "api-gateway",
		ProjectName: "demo",
		Spec: domain.AppSpec{
			DisplayName: "API Gateway",
			Description: "REST API with background worker and scheduled jobs.",
			Template: domain.AppTemplateRef{
				Name:    "web",
				Version: "1.0.0",
			},
			Components: []domain.ComponentSpec{
				{
					Name:       "api",
					Type:       domain.ComponentWeb,
					Enabled:    true,
					ExposeMode: domain.ExposeExternal,
					Template:   &domain.AppTemplateRef{Name: "web", Version: "1.0.0"},
					Values: map[string]any{
						"image":         map[string]any{"repository": "ghcr.io/suparcloud/api-gateway", "tag": "v2.3.1"},
						"containerPort": 3000,
					},
				},
				{
					Name:     "worker",
					Type:     domain.ComponentWorker,
					Enabled:  true,
					Template: &domain.AppTemplateRef{Name: "worker", Version: "1.0.0"},
					Values: map[string]any{
						"image": map[string]any{"repository": "ghcr.io/suparcloud/api-gateway", "tag": "v2.3.1"},
					},
				},
				{
					Name:     "scheduler",
					Type:     domain.ComponentCron,
					Enabled:  true,
					Template: &domain.AppTemplateRef{Name: "cronjob", Version: "1.0.0"},
					Values: map[string]any{
						"image":    map[string]any{"repository": "ghcr.io/suparcloud/api-gateway", "tag": "v2.3.1"},
						"schedule": "*/15 * * * *",
					},
				},
			},
			PreviewsEnabled: true,
		},
	}

	r.apps["demo"] = map[string]*domain.App{
		notesWeb.Name:   notesWeb,
		apiGateway.Name: apiGateway,
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

	notesRel := &domain.AppReleaseRef{
		Image: "ghcr.io/suparcloud/notes-web:v1.0.0",
		Tag:   "v1.0.0",
	}
	gwRel := &domain.AppReleaseRef{
		Image: "ghcr.io/suparcloud/api-gateway:v2.3.1",
		Tag:   "v2.3.1",
	}

	notesEnvs := []*domain.AppEnvironment{
		{
			AppName:     "notes-web",
			ProjectName: "demo",
			EnvName:     "staging",
			EnvType:     domain.AppEnvStaging,
			Namespace:   "notes-web-staging",
			Release:     notesRel,
			URLs:        []string{"http://notes-web.staging.localhost"},
			Status:      healthyFull,
		},
		{
			AppName:     "notes-web",
			ProjectName: "demo",
			EnvName:     "prod",
			EnvType:     domain.AppEnvProd,
			Namespace:   "notes-web-prod",
			Release:     notesRel,
			URLs:        []string{"http://notes-web.prod.localhost"},
			Status:      healthyFull,
		},
		{
			AppName:     "notes-web",
			ProjectName: "demo",
			EnvName:     "pr-42",
			EnvType:     domain.AppEnvPreview,
			Namespace:   "notes-web-pr-42",
			Release:     notesRel,
			URLs:        []string{"http://pr-42.notes-web.preview.localhost"},
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

	r.appEnvs["demo"] = map[string]map[string]*domain.AppEnvironment{
		"notes-web":   {},
		"api-gateway": {},
	}
	for _, e := range notesEnvs {
		r.appEnvs["demo"]["notes-web"][e.EnvName] = e
	}
	for _, e := range gwEnvs {
		r.appEnvs["demo"]["api-gateway"][e.EnvName] = e
	}
}

func seedPreviews(r *DevRuntime) {
	for _, p := range []*domain.Preview{
		{
			Name:        "pr-42",
			ProjectName: "demo",
			ServiceName: "notes-web",
			Namespace:   "demo-preview-pr-42",
			Status:      domain.StatusHealthy,
			URL:         "http://notes-web.demo-preview-pr-42.localhost",
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
			Image:        "ghcr.io/suparcloud/notes-web:v1.0.0",
			IngressURLs:  []string{"http://notes-web.staging.demo.localhost"},
			Namespace:    "demo-staging",
			LastDeployed: "2026-01-01T00:00:00Z",
		},
		{
			Status:       domain.StatusHealthy,
			Environment:  "prod",
			Replicas:     2,
			Available:    2,
			Image:        "ghcr.io/suparcloud/notes-web:v1.0.0",
			IngressURLs:  []string{"http://notes-web.prod.demo.localhost"},
			Namespace:    "demo-prod",
			LastDeployed: "2026-01-01T00:00:00Z",
		},
	} {
		r.statuses[statusKey("demo", "notes-web", s.Environment)] = s
	}
}

func seedLogs(r *DevRuntime) {
	r.logLines = []domain.LogLine{
		{Timestamp: "2026-01-01T00:00:00Z", Text: `INFO  starting notes-web service...`, Pod: "notes-web-abc12", Container: "notes-web"},
		{Timestamp: "2026-01-01T00:00:01Z", Text: `INFO  listening on :8080`, Pod: "notes-web-abc12", Container: "notes-web"},
		{Timestamp: "2026-01-01T00:00:02Z", Text: `INFO  GET /healthz → 200 OK (0ms)`, Pod: "notes-web-abc12", Container: "notes-web"},
		{Timestamp: "2026-01-01T00:00:03Z", Text: `INFO  GET / → 200 OK (2ms)`, Pod: "notes-web-abc12", Container: "notes-web"},
		{Timestamp: "2026-01-01T00:00:04Z", Text: `INFO  GET /api/items → 200 OK (5ms)`, Pod: "notes-web-abc12", Container: "notes-web"},
		{Timestamp: "2026-01-01T00:00:05Z", Text: `INFO  GET /healthz → 200 OK (0ms)`, Pod: "notes-web-abc12", Container: "notes-web"},
		{Timestamp: "2026-01-01T00:00:06Z", Text: `INFO  GET /api/items → 200 OK (4ms)`, Pod: "notes-web-abc12", Container: "notes-web"},
		{Timestamp: "2026-01-01T00:00:07Z", Text: `WARN  slow query detected (120ms)`, Pod: "notes-web-abc12", Container: "notes-web"},
		{Timestamp: "2026-01-01T00:00:08Z", Text: `INFO  GET /healthz → 200 OK (0ms)`, Pod: "notes-web-abc12", Container: "notes-web"},
		{Timestamp: "2026-01-01T00:00:09Z", Text: `INFO  GET /api/items → 200 OK (6ms)`, Pod: "notes-web-abc12", Container: "notes-web"},
	}
}
