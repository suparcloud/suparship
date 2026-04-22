package config_test

import (
	"testing"

	"github.com/suparcloud/suparship/internal/config"
)

func TestLoad_RuntimeMode(t *testing.T) {
	cases := []struct {
		name        string
		devMode     string // value of SUPARSHIP_DEV_MODE
		clusterMode string // value of SUPARSHIP_CLUSTER_MODE
		wantMode    config.RuntimeMode
		wantTrigger string // expected RuntimeModeTrigger (empty = ModeKubernetes)
	}{
		{
			name:        "no env vars → kubernetes",
			wantMode:    config.ModeKubernetes,
			wantTrigger: "",
		},
		{
			name:        "SUPARSHIP_DEV_MODE=local → fake",
			devMode:     "local",
			wantMode:    config.ModeFake,
			wantTrigger: "SUPARSHIP_DEV_MODE=local",
		},
		{
			name:        "SUPARSHIP_CLUSTER_MODE=fake → fake",
			clusterMode: "fake",
			wantMode:    config.ModeFake,
			wantTrigger: "SUPARSHIP_CLUSTER_MODE=fake",
		},
		{
			name:        "both set → dev mode takes precedence",
			devMode:     "local",
			clusterMode: "fake",
			wantMode:    config.ModeFake,
			wantTrigger: "SUPARSHIP_DEV_MODE=local",
		},
		{
			name:        "SUPARSHIP_DEV_MODE=other value → kubernetes",
			devMode:     "cloud",
			wantMode:    config.ModeKubernetes,
			wantTrigger: "",
		},
		{
			name:        "SUPARSHIP_CLUSTER_MODE=other value → kubernetes",
			clusterMode: "real",
			wantMode:    config.ModeKubernetes,
			wantTrigger: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.devMode != "" {
				t.Setenv("SUPARSHIP_DEV_MODE", tc.devMode)
			}
			if tc.clusterMode != "" {
				t.Setenv("SUPARSHIP_CLUSTER_MODE", tc.clusterMode)
			}

			cfg := config.Load()

			if cfg.RuntimeMode != tc.wantMode {
				t.Errorf("RuntimeMode = %q, want %q", cfg.RuntimeMode, tc.wantMode)
			}
			if cfg.RuntimeModeTrigger != tc.wantTrigger {
				t.Errorf("RuntimeModeTrigger = %q, want %q", cfg.RuntimeModeTrigger, tc.wantTrigger)
			}
		})
	}
}

func TestLoad_KubernetesMode_NoTrigger(t *testing.T) {
	cfg := config.Load()
	if cfg.RuntimeMode == config.ModeFake {
		t.Skip("skipping: env already sets fake mode; run in clean environment")
	}
	if cfg.RuntimeModeTrigger != "" {
		t.Errorf("ModeKubernetes must have empty RuntimeModeTrigger, got %q", cfg.RuntimeModeTrigger)
	}
}

func TestRuntimeMode_Constants(t *testing.T) {
	if config.ModeFake == config.ModeKubernetes {
		t.Error("ModeFake and ModeKubernetes must be distinct values")
	}
	if string(config.ModeFake) == "" {
		t.Error("ModeFake must not be empty string")
	}
	if string(config.ModeKubernetes) == "" {
		t.Error("ModeKubernetes must not be empty string")
	}
}

func TestLoadBootstrapEnv(t *testing.T) {
	t.Run("reads all gitops env vars", func(t *testing.T) {
		t.Setenv("SUPARSHIP_GITOPS_REPO_URL", "https://git.example.com/org/repo")
		t.Setenv("SUPARSHIP_GITOPS_REPO_USER", "deploy-bot")
		t.Setenv("SUPARSHIP_GITOPS_REPO_PASSWORD", "s3cret")
		t.Setenv("SUPARSHIP_ARGOCD_REPO_URL", "http://gitea:3000/org/repo")
		t.Setenv("SUPARSHIP_KARGO_GIT_REPO_URL", "https://gitea:3000/org/repo")
		t.Setenv("SUPARSHIP_INSECURE_REGISTRY", "true")

		env := config.LoadBootstrapEnv()

		if env.GitOpsRepoURL != "https://git.example.com/org/repo" {
			t.Errorf("GitOpsRepoURL = %q", env.GitOpsRepoURL)
		}
		if env.GitOpsRepoUser != "deploy-bot" {
			t.Errorf("GitOpsRepoUser = %q", env.GitOpsRepoUser)
		}
		if env.GitOpsRepoPassword != "s3cret" {
			t.Errorf("GitOpsRepoPassword = %q", env.GitOpsRepoPassword)
		}
		if env.ArgoCDRepoURL != "http://gitea:3000/org/repo" {
			t.Errorf("ArgoCDRepoURL = %q", env.ArgoCDRepoURL)
		}
		if env.KargoGitRepoURL != "https://gitea:3000/org/repo" {
			t.Errorf("KargoGitRepoURL = %q", env.KargoGitRepoURL)
		}
		if !env.InsecureRegistry {
			t.Error("InsecureRegistry should be true")
		}
		if !env.HasGitOps() {
			t.Error("HasGitOps should be true when repo URL is set")
		}
	})

	t.Run("empty env vars", func(t *testing.T) {
		env := config.LoadBootstrapEnv()

		if env.GitOpsRepoURL != "" {
			t.Errorf("GitOpsRepoURL should be empty, got %q", env.GitOpsRepoURL)
		}
		if env.InsecureRegistry {
			t.Error("InsecureRegistry should be false by default")
		}
		if env.HasGitOps() {
			t.Error("HasGitOps should be false when repo URL is empty")
		}
	})

	t.Run("insecure registry requires true", func(t *testing.T) {
		t.Setenv("SUPARSHIP_INSECURE_REGISTRY", "false")
		env := config.LoadBootstrapEnv()
		if env.InsecureRegistry {
			t.Error("InsecureRegistry should be false for value 'false'")
		}
	})
}

func TestLoad_NoGitOpsField(t *testing.T) {
	cfg := config.Load()
	_ = cfg.RuntimeMode
	_ = cfg.RuntimeModeTrigger
}
