package secrets

import "testing"

func TestOrgSecretName(t *testing.T) {
	if got := OrgSecretName(); got != "suparship-secrets-org" {
		t.Errorf("OrgSecretName() = %q, want %q", got, "suparship-secrets-org")
	}
}

func TestEnvTypeSecretName(t *testing.T) {
	tests := []struct {
		envType string
		want    string
	}{
		{"staging", "suparship-secrets-envtype-staging"},
		{"prod", "suparship-secrets-envtype-prod"},
		{"preview", "suparship-secrets-envtype-preview"},
	}
	for _, tt := range tests {
		if got := EnvTypeSecretName(tt.envType); got != tt.want {
			t.Errorf("EnvTypeSecretName(%q) = %q, want %q", tt.envType, got, tt.want)
		}
	}
}

func TestProjectSecretName(t *testing.T) {
	tests := []struct {
		project string
		want    string
	}{
		{"demo", "suparship-secrets-project-demo"},
		{"myproject", "suparship-secrets-project-myproject"},
	}
	for _, tt := range tests {
		if got := ProjectSecretName(tt.project); got != tt.want {
			t.Errorf("ProjectSecretName(%q) = %q, want %q", tt.project, got, tt.want)
		}
	}
}

func TestAppLevelSecretName(t *testing.T) {
	tests := []struct {
		project, app string
		want         string
	}{
		{"demo", "hello", "suparship-secrets-app-demo-hello"},
		{"myproject", "api", "suparship-secrets-app-myproject-api"},
	}
	for _, tt := range tests {
		if got := AppLevelSecretName(tt.project, tt.app); got != tt.want {
			t.Errorf("AppLevelSecretName(%q, %q) = %q, want %q", tt.project, tt.app, got, tt.want)
		}
	}
}

func TestAppEnvSecretName(t *testing.T) {
	tests := []struct {
		project, app, env string
		want              string
	}{
		{"demo", "hello", "staging", "suparship-secrets-demo-hello-staging"},
		{"demo", "hello", "prod", "suparship-secrets-demo-hello-prod"},
		{"demo", "hello", "pr-42", "suparship-secrets-demo-hello-pr-42"},
		{"myproject", "api", "staging", "suparship-secrets-myproject-api-staging"},
	}
	for _, tt := range tests {
		got := AppEnvSecretName(tt.project, tt.app, tt.env)
		if got != tt.want {
			t.Errorf("AppEnvSecretName(%q, %q, %q) = %q, want %q",
				tt.project, tt.app, tt.env, got, tt.want)
		}
	}
}

func TestAppSecretName_BackwardCompat(t *testing.T) {
	got := AppSecretName("demo", "hello", "staging")
	want := AppEnvSecretName("demo", "hello", "staging")
	if got != want {
		t.Errorf("AppSecretName should equal AppEnvSecretName: got %q, want %q", got, want)
	}
}

func TestAppConfigName(t *testing.T) {
	tests := []struct {
		project, app, env string
		want              string
	}{
		{"demo", "hello", "staging", "suparship-config-demo-hello-staging"},
		{"demo", "hello", "prod", "suparship-config-demo-hello-prod"},
	}
	for _, tt := range tests {
		got := AppConfigName(tt.project, tt.app, tt.env)
		if got != tt.want {
			t.Errorf("AppConfigName(%q, %q, %q) = %q, want %q",
				tt.project, tt.app, tt.env, got, tt.want)
		}
	}
}

func TestAppEnvSecretNameIsDeterministic(t *testing.T) {
	for i := 0; i < 5; i++ {
		got := AppEnvSecretName("demo", "hello", "staging")
		if got != "suparship-secrets-demo-hello-staging" {
			t.Errorf("run %d: not deterministic, got %q", i, got)
		}
	}
}
