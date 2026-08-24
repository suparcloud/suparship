package fake_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/fake"
)

var ctx = context.Background()

// ── Org ───────────────────────────────────────────────────────────────────────

func TestGetOrg(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	org := r.GetOrg()
	if org == nil {
		t.Fatal("GetOrg returned nil")
	}
	if org.Name != "default" {
		t.Errorf("org.Name = %q, want %q", org.Name, "default")
	}
	if org.DisplayName == "" {
		t.Error("org.DisplayName must not be empty")
	}
	if org.CreatedAt.IsZero() {
		t.Error("org.CreatedAt must not be zero")
	}
}

// ── ProjectStore ──────────────────────────────────────────────────────────────

func TestListProjectsSeeded(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	projects, err := r.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) == 0 {
		t.Fatal("expected at least one seeded project")
	}
	names := projectNames(projects)
	if !contains(names, "demo") {
		t.Errorf("expected seeded project %q, got %v", "demo", names)
	}
}

func TestGetProjectFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	p, err := r.GetProject(ctx, "demo")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.Name != "demo" {
		t.Errorf("p.Name = %q, want %q", p.Name, "demo")
	}
	if len(p.Environments) == 0 {
		t.Error("demo project must have at least one environment")
	}
}

func TestGetProjectNotFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	_, err := r.GetProject(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing project")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should contain 'not found', got: %v", err)
	}
}

func TestSaveAndGetProject(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	p := &domain.Project{
		Name:        "newproj",
		DisplayName: "New Project",
		Environments: []domain.Environment{
			{Name: "staging", Order: 1},
		},
	}
	if err := r.SaveProject(ctx, p); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}
	got, err := r.GetProject(ctx, "newproj")
	if err != nil {
		t.Fatalf("GetProject after Save: %v", err)
	}
	if got.DisplayName != "New Project" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "New Project")
	}
}

func TestSaveProjectUpdate(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	p, _ := r.GetProject(ctx, "demo")
	p.DisplayName = "Updated Demo"
	if err := r.SaveProject(ctx, p); err != nil {
		t.Fatalf("SaveProject update: %v", err)
	}
	got, _ := r.GetProject(ctx, "demo")
	if got.DisplayName != "Updated Demo" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "Updated Demo")
	}
}

func TestDeleteProjectFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	if err := r.DeleteProject(ctx, "demo"); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	_, err := r.GetProject(ctx, "demo")
	if err == nil {
		t.Fatal("expected error after deletion")
	}
}

func TestDeleteProjectNotFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	err := r.DeleteProject(ctx, "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// ── ServiceStore ──────────────────────────────────────────────────────────────

func TestListServicesSeeded(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	services, err := r.ListServices(ctx, "demo")
	if err != nil {
		t.Fatalf("ListServices: %v", err)
	}
	if len(services) == 0 {
		t.Fatal("expected at least one seeded service in demo")
	}
	names := serviceNames(services)
	if !contains(names, "notes-web") {
		t.Errorf("expected seeded service %q, got %v", "notes-web", names)
	}
}

func TestListServicesUnknownProject(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	_, err := r.ListServices(ctx, "nope")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestGetServiceFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	svc, err := r.GetService(ctx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if svc.TemplateName == "" {
		t.Error("notes-web service should have a TemplateName")
	}
}

func TestGetServiceNotFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	_, err := r.GetService(ctx, "demo", "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestSaveAndDeleteService(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	svc := &domain.Service{
		Name:         "backend",
		ProjectName:  "demo",
		TemplateName: "web-service",
	}
	if err := r.SaveService(ctx, "demo", svc); err != nil {
		t.Fatalf("SaveService: %v", err)
	}
	got, err := r.GetService(ctx, "demo", "backend")
	if err != nil {
		t.Fatalf("GetService after Save: %v", err)
	}
	if got.Name != "backend" {
		t.Errorf("Name = %q, want %q", got.Name, "backend")
	}

	if err := r.DeleteService(ctx, "demo", "backend"); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	_, err = r.GetService(ctx, "demo", "backend")
	if err == nil {
		t.Fatal("expected error after deletion")
	}
}

func TestDeleteServiceNotFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	err := r.DeleteService(ctx, "demo", "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// ── AppStore ──────────────────────────────────────────────────────────────────

func TestListAppsSeeded(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	apps, err := r.ListApps(ctx, "demo")
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if len(apps) == 0 {
		t.Fatal("expected at least one seeded app in demo")
	}
	names := appNames(apps)
	if !contains(names, "notes-web") {
		t.Errorf("expected seeded app %q, got %v", "notes-web", names)
	}
}

func TestListAppsUnknownProject(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	_, err := r.ListApps(ctx, "nope")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestGetAppFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	app, err := r.GetApp(ctx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if app.Spec.Template.Name == "" {
		t.Error("notes-web app should have a template name")
	}
	// A plain single-chart app declares no components; the display row is
	// synthesized.
	if len(app.Spec.Components) != 0 {
		t.Errorf("notes-web app should declare no components, got %+v", app.Spec.Components)
	}
	if eff := app.EffectiveComponents(); len(eff) != 1 || eff[0].Type != domain.ComponentWeb {
		t.Errorf("EffectiveComponents = %+v, want one synthesized web row", eff)
	}
}

func TestGetAppNotFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	_, err := r.GetApp(ctx, "demo", "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestListAppEnvironmentsSeeded(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	envs, err := r.ListAppEnvironments(ctx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("ListAppEnvironments: %v", err)
	}
	if len(envs) < 2 {
		t.Fatalf("expected at least 2 seeded environments, got %d", len(envs))
	}
	envNames := make([]string, len(envs))
	for i, e := range envs {
		envNames[i] = e.EnvName
	}
	for _, want := range []string{"staging", "prod"} {
		if !contains(envNames, want) {
			t.Errorf("expected environment %q, got %v", want, envNames)
		}
	}
}

func TestListAppEnvironmentsUnknownApp(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	envs, err := r.ListAppEnvironments(ctx, "demo", "ghost")
	if err != nil {
		t.Fatalf("ListAppEnvironments for unknown app should not error: %v", err)
	}
	if len(envs) != 0 {
		t.Errorf("expected empty slice for unknown app, got %d entries", len(envs))
	}
}

func TestGetAppEnvironmentFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	for _, envName := range []string{"staging", "prod"} {
		t.Run(envName, func(t *testing.T) {
			env, err := r.GetAppEnvironment(ctx, "demo", "notes-web", envName)
			if err != nil {
				t.Fatalf("GetAppEnvironment(%q): %v", envName, err)
			}
			if env.Namespace == "" {
				t.Error("Namespace must not be empty")
			}
			if len(env.URLs) == 0 {
				t.Error("expected at least one URL for seeded app environment")
			}
			if env.Release == nil {
				t.Error("Release must not be nil for seeded app environment")
			}
			if env.Status.Phase != domain.StatusHealthy {
				t.Errorf("Status.Phase = %q, want %q", env.Status.Phase, domain.StatusHealthy)
			}
			if env.EnvType != domain.AppEnvironmentType(envName) {
				t.Errorf("EnvType = %q, want %q", env.EnvType, envName)
			}
		})
	}
}

func TestGetAppEnvironmentNotFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	_, err := r.GetAppEnvironment(ctx, "demo", "notes-web", "dev")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestAppSeedIsDeterministic(t *testing.T) {
	r1 := fake.NewSeededDevRuntime()
	r2 := fake.NewSeededDevRuntime()

	a1, _ := r1.GetApp(ctx, "demo", "notes-web")
	a2, _ := r2.GetApp(ctx, "demo", "notes-web")
	if a1.Spec.Template.Name != a2.Spec.Template.Name {
		t.Error("app seed data should be identical across runs")
	}

	e1, _ := r1.GetAppEnvironment(ctx, "demo", "notes-web", "staging")
	e2, _ := r2.GetAppEnvironment(ctx, "demo", "notes-web", "staging")
	if e1.Namespace != e2.Namespace || e1.Status.Phase != e2.Status.Phase {
		t.Error("app environment seed data should be identical across runs")
	}
}

func TestListAppPreviewsAll(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	previews, err := r.ListAppPreviews(ctx, "", "")
	if err != nil {
		t.Fatalf("ListAppPreviews: %v", err)
	}
	if len(previews) == 0 {
		t.Fatal("expected at least one seeded preview environment")
	}
	for _, p := range previews {
		if p.EnvType != domain.AppEnvPreview {
			t.Errorf("expected EnvType %q, got %q", domain.AppEnvPreview, p.EnvType)
		}
	}
}

func TestListAppPreviewsByProject(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	previews, err := r.ListAppPreviews(ctx, "demo", "")
	if err != nil {
		t.Fatalf("ListAppPreviews(demo, ''): %v", err)
	}
	for _, p := range previews {
		if p.ProjectName != "demo" {
			t.Errorf("expected ProjectName %q, got %q", "demo", p.ProjectName)
		}
		if p.EnvType != domain.AppEnvPreview {
			t.Errorf("expected EnvType %q, got %q", domain.AppEnvPreview, p.EnvType)
		}
	}
}

func TestListAppPreviewsByApp(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	previews, err := r.ListAppPreviews(ctx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("ListAppPreviews(demo, notes-web): %v", err)
	}
	if len(previews) == 0 {
		t.Fatal("expected at least one seeded preview for notes-web")
	}
	for _, p := range previews {
		if p.AppName != "notes-web" {
			t.Errorf("expected AppName %q, got %q", "notes-web", p.AppName)
		}
		if p.EnvType != domain.AppEnvPreview {
			t.Errorf("expected EnvType %q, got %q", domain.AppEnvPreview, p.EnvType)
		}
	}
}

func TestListAppPreviewsNoMatchProject(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	previews, err := r.ListAppPreviews(ctx, "nonexistent", "")
	if err != nil {
		t.Fatalf("ListAppPreviews should not error for unknown project: %v", err)
	}
	if len(previews) != 0 {
		t.Errorf("expected empty slice for unknown project, got %d", len(previews))
	}
}

func TestListAppPreviewsNoMatchApp(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	previews, err := r.ListAppPreviews(ctx, "demo", "ghost")
	if err != nil {
		t.Fatalf("ListAppPreviews should not error for unknown app: %v", err)
	}
	if len(previews) != 0 {
		t.Errorf("expected empty slice for unknown app, got %d", len(previews))
	}
}

func TestListAppPreviewsIsDeterministic(t *testing.T) {
	r1 := fake.NewSeededDevRuntime()
	r2 := fake.NewSeededDevRuntime()

	p1, err1 := r1.ListAppPreviews(ctx, "", "")
	p2, err2 := r2.ListAppPreviews(ctx, "", "")
	if err1 != nil || err2 != nil {
		t.Fatalf("ListAppPreviews errors: %v / %v", err1, err2)
	}
	if len(p1) != len(p2) {
		t.Fatalf("preview counts differ: %d vs %d", len(p1), len(p2))
	}
	for i := range p1 {
		if p1[i].EnvName != p2[i].EnvName || p1[i].Namespace != p2[i].Namespace {
			t.Errorf("preview[%d] differs: %+v vs %+v", i, p1[i], p2[i])
		}
	}
}

func TestListAppPreviewsOnlyPreviewType(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	all, _ := r.ListAppEnvironments(ctx, "demo", "notes-web")
	var nonPreviews int
	for _, e := range all {
		if e.EnvType != domain.AppEnvPreview {
			nonPreviews++
		}
	}
	if nonPreviews == 0 {
		t.Fatal("seed data must contain non-preview environments to make this test meaningful")
	}

	previews, err := r.ListAppPreviews(ctx, "demo", "notes-web")
	if err != nil {
		t.Fatalf("ListAppPreviews: %v", err)
	}
	if len(previews) >= len(all) {
		t.Errorf("ListAppPreviews should return fewer entries than ListAppEnvironments (%d vs %d)", len(previews), len(all))
	}
}

// ── TemplateStore ─────────────────────────────────────────────────────────────

func TestListTemplatesSeeded(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	templates, err := r.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(templates) == 0 {
		t.Fatal("expected at least one seeded template")
	}
	names := templateNames(templates)
	for _, want := range []string{"web", "worker", "cronjob", "postgres"} {
		if !contains(names, want) {
			t.Errorf("expected template %q in list, got %v", want, names)
		}
	}
}

func TestGetTemplateFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	tmpl, err := r.GetTemplate(ctx, "web")
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if tmpl.Title == "" {
		t.Error("web template should have a Title")
	}
}

func TestGetTemplateNotFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	_, err := r.GetTemplate(ctx, "does-not-exist")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// ── PreviewStore ──────────────────────────────────────────────────────────────

func TestListPreviewsSeeded(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	previews, err := r.ListPreviews(ctx)
	if err != nil {
		t.Fatalf("ListPreviews: %v", err)
	}
	if len(previews) == 0 {
		t.Fatal("expected at least one seeded preview")
	}
	names := previewNames(previews)
	if !contains(names, "pr-42") {
		t.Errorf("expected seeded preview %q, got %v", "pr-42", names)
	}
}

func TestGetPreviewFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	p, err := r.GetPreview(ctx, "pr-42")
	if err != nil {
		t.Fatalf("GetPreview: %v", err)
	}
	if p.ProjectName != "demo" {
		t.Errorf("ProjectName = %q, want %q", p.ProjectName, "demo")
	}
	if p.URL == "" {
		t.Error("seeded preview should have a URL")
	}
}

func TestGetPreviewNotFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	_, err := r.GetPreview(ctx, "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestCreatePreview(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	p := &domain.Preview{
		Name:        "pr-99",
		ProjectName: "demo",
		ServiceName: "notes-web",
		Namespace:   "demo-preview-pr-99",
		Status:      domain.StatusNotDeployed,
		CreatedAt:   time.Now().UTC(),
	}
	if err := r.CreatePreview(ctx, p); err != nil {
		t.Fatalf("CreatePreview: %v", err)
	}
	got, err := r.GetPreview(ctx, "pr-99")
	if err != nil {
		t.Fatalf("GetPreview after Create: %v", err)
	}
	if got.ServiceName != "notes-web" {
		t.Errorf("ServiceName = %q, want %q", got.ServiceName, "notes-web")
	}
}

func TestCreatePreviewDuplicate(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	p := &domain.Preview{Name: "pr-42", ProjectName: "demo", ServiceName: "notes-web", CreatedAt: time.Now().UTC()}
	err := r.CreatePreview(ctx, p)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got: %v", err)
	}
}

func TestDeletePreview(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	if err := r.DeletePreview(ctx, "pr-42"); err != nil {
		t.Fatalf("DeletePreview: %v", err)
	}
	_, err := r.GetPreview(ctx, "pr-42")
	if err == nil {
		t.Fatal("expected error after deletion")
	}
}

func TestDeletePreviewNotFound(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	err := r.DeletePreview(ctx, "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

// ── RuntimeStatusReader ───────────────────────────────────────────────────────

func TestGetServiceStatusSeeded(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	cases := []struct {
		env    string
		status string
	}{
		{"staging", domain.StatusHealthy},
		{"prod", domain.StatusHealthy},
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			s, err := r.GetServiceStatus(ctx, "demo", "notes-web", tc.env)
			if err != nil {
				t.Fatalf("GetServiceStatus: %v", err)
			}
			if s.Status != tc.status {
				t.Errorf("Status = %q, want %q", s.Status, tc.status)
			}
			if len(s.IngressURLs) == 0 {
				t.Error("expected at least one ingress URL for seeded env")
			}
		})
	}
}

func TestGetServiceStatusUnknown(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	s, err := r.GetServiceStatus(ctx, "demo", "notes-web", "unknown-env")
	if err != nil {
		t.Fatalf("GetServiceStatus: %v", err)
	}
	if s.Status != domain.StatusNotDeployed {
		t.Errorf("Status = %q, want %q", s.Status, domain.StatusNotDeployed)
	}
}

func TestGetServiceStatusUnknownProject(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	s, err := r.GetServiceStatus(ctx, "nope", "notes-web", "staging")
	if err != nil {
		t.Fatalf("GetServiceStatus should not error for unknown project: %v", err)
	}
	if s.Status != domain.StatusNotDeployed {
		t.Errorf("Status = %q, want %q", s.Status, domain.StatusNotDeployed)
	}
}

// ── LogReader ─────────────────────────────────────────────────────────────────

func TestGetLogsAll(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	lines, err := r.GetLogs(ctx, "demo", "notes-web", "staging", 0)
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("expected at least one log line")
	}
	for i, l := range lines {
		if l.Text == "" {
			t.Errorf("line[%d].Text must not be empty", i)
		}
		if l.Pod == "" {
			t.Errorf("line[%d].Pod must not be empty", i)
		}
	}
}

func TestGetLogsTailLines(t *testing.T) {
	r := fake.NewSeededDevRuntime()
	all, _ := r.GetLogs(ctx, "demo", "notes-web", "staging", 0)

	cases := []struct {
		tail int
		want int
	}{
		{3, 3},
		{1, 1},
		{999, len(all)}, // more than available → return all
	}
	for _, tc := range cases {
		got, err := r.GetLogs(ctx, "demo", "notes-web", "staging", tc.tail)
		if err != nil {
			t.Fatalf("GetLogs(tail=%d): %v", tc.tail, err)
		}
		if len(got) != tc.want {
			t.Errorf("tail=%d: got %d lines, want %d", tc.tail, len(got), tc.want)
		}
	}
}

func TestGetLogsIsolated(t *testing.T) {
	// logs should be identical regardless of project/service/env arguments
	// (the fake returns the same fixture for any service)
	r := fake.NewSeededDevRuntime()
	a, _ := r.GetLogs(ctx, "demo", "notes-web", "staging", 0)
	b, _ := r.GetLogs(ctx, "other", "svc", "prod", 0)
	if len(a) != len(b) {
		t.Errorf("expected same log count for any service, got %d vs %d", len(a), len(b))
	}
}

// ── Seed determinism ──────────────────────────────────────────────────────────

func TestSeedIsDeterministic(t *testing.T) {
	r1 := fake.NewSeededDevRuntime()
	r2 := fake.NewSeededDevRuntime()

	p1, _ := r1.GetProject(ctx, "demo")
	p2, _ := r2.GetProject(ctx, "demo")
	if p1.Name != p2.Name || p1.DisplayName != p2.DisplayName {
		t.Error("seed data should be identical across runs")
	}

	s1, _ := r1.GetServiceStatus(ctx, "demo", "notes-web", "staging")
	s2, _ := r2.GetServiceStatus(ctx, "demo", "notes-web", "staging")
	if s1.Status != s2.Status || s1.Image != s2.Image {
		t.Error("seeded status should be identical across runs")
	}

	o1 := r1.GetOrg()
	o2 := r2.GetOrg()
	if o1.Name != o2.Name || !o1.CreatedAt.Equal(o2.CreatedAt) {
		t.Error("seeded org should be identical across runs")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func projectNames(ps []*domain.Project) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func appNames(as []*domain.App) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Name
	}
	return out
}

func serviceNames(ss []*domain.Service) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}

func templateNames(ts []*domain.Template) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func previewNames(ps []*domain.Preview) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
