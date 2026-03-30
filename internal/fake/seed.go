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
	seedTemplates(r)
	seedProjects(r)
	seedServices(r)
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
			{Name: "staging", DisplayName: "Staging", Order: 1},
			{Name: "prod", DisplayName: "Production", Order: 2},
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
