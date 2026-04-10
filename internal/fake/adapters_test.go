package fake_test

import (
	"context"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/fake"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/runtime"
)

// ── NewDevServerDeps ──────────────────────────────────────────────────────────

func TestNewDevServerDeps_AllFieldsSet(t *testing.T) {
	deps := fake.NewDevServerDeps()
	if deps.Authenticator == nil {
		t.Error("Authenticator must not be nil")
	}
	if deps.OrgProvider == nil {
		t.Error("OrgProvider must not be nil")
	}
	if deps.ProjectStore == nil {
		t.Error("ProjectStore must not be nil")
	}
	if deps.PreviewStore == nil {
		t.Error("PreviewStore must not be nil")
	}
	if deps.RuntimeProvider == nil {
		t.Error("RuntimeProvider must not be nil")
	}
	if deps.LogsProvider == nil {
		t.Error("LogsProvider must not be nil")
	}
	if deps.AdminUsername == "" {
		t.Error("AdminUsername must not be empty")
	}
}

func TestNewDevServerDeps_Independent(t *testing.T) {
	d1 := fake.NewDevServerDeps()
	d2 := fake.NewDevServerDeps()

	// Mutate d1's project store; d2 must not be affected.
	if err := d1.ProjectStore.Delete(context.Background(), "demo"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := d2.ProjectStore.Get(context.Background(), "demo"); err != nil {
		t.Error("d2 should still have 'demo' after d1 mutation")
	}
}

func TestNewDevServerDeps_DefaultCredentials(t *testing.T) {
	// Ensure no env vars are set so we exercise the default path.
	t.Setenv("SUPARSHIP_ADMIN_EMAIL", "")
	t.Setenv("SUPARSHIP_ADMIN_PASSWORD", "")

	deps := fake.NewDevServerDeps()

	if deps.AdminUsername != fake.FakeAdminUsername {
		t.Errorf("AdminUsername = %q, want %q", deps.AdminUsername, fake.FakeAdminUsername)
	}

	creds, err := deps.Authenticator.Authenticate(
		context.Background(), fake.FakeAdminUsername, fake.FakeAdminPassword,
	)
	if err != nil {
		t.Fatalf("default credentials should authenticate: %v", err)
	}
	if creds.Username != fake.FakeAdminUsername {
		t.Errorf("creds.Username = %q, want %q", creds.Username, fake.FakeAdminUsername)
	}
}

func TestNewDevServerDeps_EnvCredentials(t *testing.T) {
	t.Setenv("SUPARSHIP_ADMIN_EMAIL", "dev@example.com")
	t.Setenv("SUPARSHIP_ADMIN_PASSWORD", "devpass99")

	deps := fake.NewDevServerDeps()

	if deps.AdminUsername != "dev@example.com" {
		t.Errorf("AdminUsername = %q, want %q", deps.AdminUsername, "dev@example.com")
	}

	// Env-configured credentials must work.
	_, err := deps.Authenticator.Authenticate(context.Background(), "dev@example.com", "devpass99")
	if err != nil {
		t.Fatalf("env-configured credentials should authenticate: %v", err)
	}

	// Package-default credentials must NOT work when overridden via env.
	_, err = deps.Authenticator.Authenticate(
		context.Background(), fake.FakeAdminUsername, fake.FakeAdminPassword,
	)
	if err == nil {
		t.Error("package defaults should not authenticate when env credentials are set")
	}
}

// ── FakeAuthenticator ─────────────────────────────────────────────────────────

func TestFakeAuthenticator_Success(t *testing.T) {
	a := &fake.FakeAuthenticator{}
	creds, err := a.Authenticate(context.Background(), fake.FakeAdminUsername, fake.FakeAdminPassword)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if creds == nil {
		t.Fatal("credentials must not be nil on success")
	}
	if creds.Username != fake.FakeAdminUsername {
		t.Errorf("Username = %q, want %q", creds.Username, fake.FakeAdminUsername)
	}
}

func TestFakeAuthenticator_CustomCredentials(t *testing.T) {
	a := &fake.FakeAuthenticator{Username: "tester", Password: "secret"}
	creds, err := a.Authenticate(context.Background(), "tester", "secret")
	if err != nil {
		t.Fatalf("Authenticate with custom credentials: %v", err)
	}
	if creds.Username != "tester" {
		t.Errorf("Username = %q, want %q", creds.Username, "tester")
	}

	// Default credentials must not work when custom ones are set.
	_, err = a.Authenticate(context.Background(), fake.FakeAdminUsername, fake.FakeAdminPassword)
	if err == nil {
		t.Error("default credentials should not work when custom credentials are configured")
	}
}

func TestFakeAuthenticator_WrongPassword(t *testing.T) {
	a := &fake.FakeAuthenticator{}
	_, err := a.Authenticate(context.Background(), fake.FakeAdminUsername, "wrong")
	if err == nil {
		t.Fatal("expected error for wrong password")
	}
	if err != auth.ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestFakeAuthenticator_WrongUsername(t *testing.T) {
	a := &fake.FakeAuthenticator{}
	_, err := a.Authenticate(context.Background(), "nobody", fake.FakeAdminPassword)
	if err != auth.ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestFakeAdminCredentialConstants(t *testing.T) {
	// Constants must match .env.example to keep local dev frictionless.
	if fake.FakeAdminUsername != "admin@local" {
		t.Errorf("FakeAdminUsername = %q, want %q (must match .env.example)", fake.FakeAdminUsername, "admin@local")
	}
	if fake.FakeAdminPassword != "admin123" {
		t.Errorf("FakeAdminPassword = %q, want %q (must match .env.example)", fake.FakeAdminPassword, "admin123")
	}
}

// ── FakeOrgProvider ───────────────────────────────────────────────────────────

func TestFakeOrgProvider_GetOrg(t *testing.T) {
	p := fake.NewDevServerDeps().OrgProvider
	org, err := p.GetOrg(context.Background())
	if err != nil {
		t.Fatalf("GetOrg: %v", err)
	}
	if org.Name != "default" {
		t.Errorf("org.Name = %q, want %q", org.Name, "default")
	}
	if org.DisplayName == "" {
		t.Error("org.DisplayName must not be empty")
	}
	if len(org.Teams) == 0 {
		t.Error("org must have at least one team")
	}
	if len(org.RoleBindings) == 0 {
		t.Error("org must have at least one role binding")
	}

	adminInTeam := false
	for _, m := range org.Teams[0].Members {
		if m == fake.FakeAdminUsername {
			adminInTeam = true
		}
	}
	if !adminInTeam {
		t.Errorf("fake admin %q not found in org teams", fake.FakeAdminUsername)
	}
}

// ── FakeProjectStore ──────────────────────────────────────────────────────────

func TestFakeProjectStore_ListSeeded(t *testing.T) {
	deps := fake.NewDevServerDeps()
	projects, err := deps.ProjectStore.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) == 0 {
		t.Fatal("expected at least one seeded project")
	}
	names := projectStoreNames(projects)
	if !sliceContains(names, "demo") {
		t.Errorf("expected seeded project 'demo', got %v", names)
	}
}

func TestFakeProjectStore_GetSeeded(t *testing.T) {
	deps := fake.NewDevServerDeps()
	p, err := deps.ProjectStore.Get(context.Background(), "demo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Metadata.Name != "demo" {
		t.Errorf("Name = %q, want %q", p.Metadata.Name, "demo")
	}
	// Demo project environments are inherited from org defaults; the project-level
	// override list may be empty. Effective environments are resolved via merge.
	_ = p.Spec.Environments
	if len(p.Spec.Services) == 0 {
		t.Error("demo project must have at least one service")
	}
}

func TestFakeProjectStore_GetNotFound(t *testing.T) {
	deps := fake.NewDevServerDeps()
	_, err := deps.ProjectStore.Get(context.Background(), "nonexistent")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

func TestFakeProjectStore_SaveCreate(t *testing.T) {
	deps := fake.NewDevServerDeps()
	p := &project.Project{
		APIVersion: project.CurrentAPIVersion,
		Kind:       project.ProjectKind,
		Metadata:   project.ProjectMeta{Name: "new-project"},
		Spec: project.ProjectSpec{
			Environments: []project.Environment{{Name: "staging", Order: 1}},
		},
	}
	if err := deps.ProjectStore.Save(context.Background(), p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := deps.ProjectStore.Get(context.Background(), "new-project")
	if err != nil {
		t.Fatalf("Get after Save: %v", err)
	}
	if got.Metadata.Name != "new-project" {
		t.Errorf("Name = %q, want %q", got.Metadata.Name, "new-project")
	}
}

func TestFakeProjectStore_SaveUpdate(t *testing.T) {
	deps := fake.NewDevServerDeps()
	p, _ := deps.ProjectStore.Get(context.Background(), "demo")
	svc := project.Service{
		Name:     "api",
		Template: project.TemplateRef{Name: "web-service"},
	}
	p.Spec.Services = append(p.Spec.Services, svc)
	if err := deps.ProjectStore.Save(context.Background(), p); err != nil {
		t.Fatalf("Save update: %v", err)
	}
	got, _ := deps.ProjectStore.Get(context.Background(), "demo")
	found := false
	for _, s := range got.Spec.Services {
		if s.Name == "api" {
			found = true
		}
	}
	if !found {
		t.Error("appended service 'api' not persisted after Save")
	}
}

func TestFakeProjectStore_Delete(t *testing.T) {
	deps := fake.NewDevServerDeps()
	if err := deps.ProjectStore.Delete(context.Background(), "demo"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := deps.ProjectStore.Get(context.Background(), "demo")
	if err == nil {
		t.Fatal("expected error after deletion")
	}
}

func TestFakeProjectStore_DeleteNotFound(t *testing.T) {
	deps := fake.NewDevServerDeps()
	err := deps.ProjectStore.Delete(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

// ── FakePreviewStore ──────────────────────────────────────────────────────────

func TestFakePreviewStore_ListSeeded(t *testing.T) {
	deps := fake.NewDevServerDeps()
	previews, err := deps.PreviewStore.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(previews) == 0 {
		t.Fatal("expected at least one seeded preview")
	}
	names := previewStoreNames(previews)
	if !sliceContains(names, "pr-42") {
		t.Errorf("expected seeded preview 'pr-42', got %v", names)
	}
}

func TestFakePreviewStore_GetSeeded(t *testing.T) {
	deps := fake.NewDevServerDeps()
	p, err := deps.PreviewStore.Get(context.Background(), "pr-42")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Spec.Project != "demo" {
		t.Errorf("Spec.Project = %q, want %q", p.Spec.Project, "demo")
	}
	if p.Spec.Service != "hello" {
		t.Errorf("Spec.Service = %q, want %q", p.Spec.Service, "hello")
	}
	if p.Metadata.CreatedAt == "" {
		t.Error("preview CreatedAt must not be empty")
	}
}

func TestFakePreviewStore_GetNotFound(t *testing.T) {
	deps := fake.NewDevServerDeps()
	_, err := deps.PreviewStore.Get(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

func TestFakePreviewStore_Save(t *testing.T) {
	deps := fake.NewDevServerDeps()
	p := preview.New("pr-99", "demo", "hello")
	if err := deps.PreviewStore.Save(context.Background(), p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := deps.PreviewStore.Get(context.Background(), "pr-99")
	if err != nil {
		t.Fatalf("Get after Save: %v", err)
	}
	if got.Spec.Service != "hello" {
		t.Errorf("Service = %q, want %q", got.Spec.Service, "hello")
	}
}

func TestFakePreviewStore_Delete(t *testing.T) {
	deps := fake.NewDevServerDeps()
	if err := deps.PreviewStore.Delete(context.Background(), "pr-42"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := deps.PreviewStore.Get(context.Background(), "pr-42")
	if err == nil {
		t.Fatal("expected error after deletion")
	}
}

func TestFakePreviewStore_DeleteNotFound(t *testing.T) {
	deps := fake.NewDevServerDeps()
	err := deps.PreviewStore.Delete(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

// ── FakeRuntimeProvider ───────────────────────────────────────────────────────

func TestFakeRuntimeProvider_SeededNamespaces(t *testing.T) {
	deps := fake.NewDevServerDeps()
	cases := []struct {
		ns      string
		service string
	}{
		{"demo-staging", "hello"},
		{"demo-prod", "hello"},
		{"demo-preview-pr-42", "hello"},
	}
	for _, tc := range cases {
		t.Run(tc.ns, func(t *testing.T) {
			info, err := deps.RuntimeProvider.GetServiceRuntime(context.Background(), tc.ns, tc.service)
			if err != nil {
				t.Fatalf("GetServiceRuntime: %v", err)
			}
			if info.Status != runtime.StatusHealthy {
				t.Errorf("Status = %q, want %q", info.Status, runtime.StatusHealthy)
			}
			if len(info.IngressURLs) == 0 {
				t.Error("expected at least one ingress URL for seeded namespace")
			}
			if info.Image == "" {
				t.Error("expected a non-empty image for seeded namespace")
			}
		})
	}
}

func TestFakeRuntimeProvider_UnknownReturnsNotDeployed(t *testing.T) {
	deps := fake.NewDevServerDeps()
	info, err := deps.RuntimeProvider.GetServiceRuntime(context.Background(), "unknown-ns", "svc")
	if err != nil {
		t.Fatalf("GetServiceRuntime: %v", err)
	}
	if info.Status != runtime.StatusNotDeployed {
		t.Errorf("Status = %q, want %q", info.Status, runtime.StatusNotDeployed)
	}
	if info.IngressURLs == nil {
		t.Error("IngressURLs must not be nil")
	}
}

func TestFakeRuntimeProvider_Deterministic(t *testing.T) {
	d1 := fake.NewDevServerDeps()
	d2 := fake.NewDevServerDeps()

	i1, _ := d1.RuntimeProvider.GetServiceRuntime(context.Background(), "demo-staging", "hello")
	i2, _ := d2.RuntimeProvider.GetServiceRuntime(context.Background(), "demo-staging", "hello")

	if i1.Status != i2.Status || i1.Image != i2.Image || i1.LastDeployed != i2.LastDeployed {
		t.Error("runtime seed data must be identical across calls")
	}
}

// ── FakeLogsProvider ──────────────────────────────────────────────────────────

func TestFakeLogsProvider_ListPods(t *testing.T) {
	deps := fake.NewDevServerDeps()
	pods, err := deps.LogsProvider.ListPods(context.Background(), "demo-staging", "hello")
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if len(pods) == 0 {
		t.Fatal("expected at least one pod")
	}
	if pods[0].Name == "" {
		t.Error("pod name must not be empty")
	}
	if len(pods[0].Containers) == 0 {
		t.Error("pod must have at least one container")
	}
}

func TestFakeLogsProvider_GetLogsAll(t *testing.T) {
	deps := fake.NewDevServerDeps()
	result, err := deps.LogsProvider.GetLogs(context.Background(), runtime.LogsRequest{
		Namespace: "demo-staging",
		Pod:       "hello-abc12",
	})
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if result.Logs == "" {
		t.Error("Logs must not be empty")
	}
	if result.Pod == "" {
		t.Error("Pod must not be empty")
	}
	if result.Container == "" {
		t.Error("Container must not be empty")
	}
}

func TestFakeLogsProvider_GetLogsTail(t *testing.T) {
	deps := fake.NewDevServerDeps()

	allResult, _ := deps.LogsProvider.GetLogs(context.Background(), runtime.LogsRequest{
		Namespace: "demo-staging",
	})
	allLines := strings.Split(strings.TrimRight(allResult.Logs, "\n"), "\n")
	total := len(allLines)

	cases := []struct {
		tail int64
		want int
	}{
		{3, 3},
		{1, 1},
		{int64(total) + 999, total}, // more than available → return all
	}
	for _, tc := range cases {
		tail := tc.tail
		got, err := deps.LogsProvider.GetLogs(context.Background(), runtime.LogsRequest{
			Namespace: "demo-staging",
			TailLines: &tail,
		})
		if err != nil {
			t.Fatalf("GetLogs(tail=%d): %v", tc.tail, err)
		}
		gotLines := strings.Split(strings.TrimRight(got.Logs, "\n"), "\n")
		if len(gotLines) != tc.want {
			t.Errorf("tail=%d: got %d lines, want %d", tc.tail, len(gotLines), tc.want)
		}
	}
}

func TestFakeLogsProvider_DefaultPodContainer(t *testing.T) {
	deps := fake.NewDevServerDeps()
	result, err := deps.LogsProvider.GetLogs(context.Background(), runtime.LogsRequest{
		Namespace: "demo-staging",
		// Pod and Container intentionally empty — fake should fill in defaults
	})
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if result.Pod == "" {
		t.Error("Pod must be filled in when not specified")
	}
	if result.Container == "" {
		t.Error("Container must be filled in when not specified")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func projectStoreNames(ps []*project.Project) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Metadata.Name
	}
	return out
}

func previewStoreNames(ps []*preview.Preview) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Metadata.Name
	}
	return out
}

func sliceContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
