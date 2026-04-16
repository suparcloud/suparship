package bootstrap

import (
	"context"
	"log/slog"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const systemNS = "suparship-system"

func TestReconcile_EmptyCluster(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNS}},
	)
	logger := slog.Default()

	result := Reconcile(context.Background(), client, logger)

	if result.GitOpsConfigured {
		t.Error("expected gitops not configured on empty cluster")
	}
	if result.RegistryConfigured {
		t.Error("expected registry not configured on empty cluster")
	}
	if len(result.Warnings) == 0 {
		t.Error("expected at least one warning for missing gitops")
	}
}

func TestReconcile_WithGitOpsConfig(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNS}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "suparship-gitops-config",
				Namespace: systemNS,
				Labels:    map[string]string{"suparship.io/type": "gitops-config"},
			},
			Data: map[string]string{
				"gitops.yaml": `provider: "github"
repoURL: "https://github.com/org/repo"
branch: "main"
authSecretRef: "gitops-creds"
initializeRepo: true
`,
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gitops-creds",
				Namespace: systemNS,
			},
			Data: map[string][]byte{
				"token": []byte("ghp_test123"),
			},
		},
	)
	logger := slog.Default()

	result := Reconcile(context.Background(), client, logger)

	if !result.GitOpsConfigured {
		t.Error("expected gitops to be configured")
	}
	if result.GitOpsProvider != "github" {
		t.Errorf("provider = %q, want github", result.GitOpsProvider)
	}
	if result.GitOpsRepoURL != "https://github.com/org/repo" {
		t.Errorf("repoURL = %q", result.GitOpsRepoURL)
	}
}

func TestReconcile_WithRegistryConfig(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNS}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "suparship-registry-config",
				Namespace: systemNS,
				Labels:    map[string]string{"suparship.io/type": "registry-config"},
			},
			Data: map[string]string{
				"registry.yaml": `enabled: true
url: "ghcr.io"
username: "robot"
`,
			},
		},
	)
	logger := slog.Default()

	result := Reconcile(context.Background(), client, logger)

	if !result.RegistryConfigured {
		t.Error("expected registry to be configured")
	}
	if result.RegistryURL != "ghcr.io" {
		t.Errorf("registryURL = %q, want ghcr.io", result.RegistryURL)
	}
}

func TestReconcile_RegistryDisabled(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNS}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "suparship-registry-config",
				Namespace: systemNS,
				Labels:    map[string]string{"suparship.io/type": "registry-config"},
			},
			Data: map[string]string{
				"registry.yaml": `enabled: false
url: ""
`,
			},
		},
	)
	logger := slog.Default()

	result := Reconcile(context.Background(), client, logger)

	if result.RegistryConfigured {
		t.Error("expected registry not configured when disabled")
	}
}

func TestReconcile_GitOpsMissingSecret(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: systemNS}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "suparship-gitops-config",
				Namespace: systemNS,
				Labels:    map[string]string{"suparship.io/type": "gitops-config"},
			},
			Data: map[string]string{
				"gitops.yaml": `provider: "github"
repoURL: "https://github.com/org/repo"
branch: "main"
authSecretRef: "missing-secret"
`,
			},
		},
	)
	logger := slog.Default()

	result := Reconcile(context.Background(), client, logger)

	if !result.GitOpsConfigured {
		t.Error("expected gitops to be configured even with missing secret")
	}

	found := false
	for _, w := range result.Warnings {
		if len(w) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected a warning about missing secret")
	}
}

func TestFormatSummary(t *testing.T) {
	r := Result{
		GitOpsConfigured:   true,
		GitOpsProvider:     "github",
		GitOpsRepoURL:      "https://github.com/org/repo",
		RegistryConfigured: true,
		RegistryURL:        "ghcr.io",
	}

	summary := FormatSummary(r)
	if summary == "" {
		t.Error("expected non-empty summary")
	}
}

func TestFormatSummary_NotConfigured(t *testing.T) {
	r := Result{Warnings: []string{"test warning"}}
	summary := FormatSummary(r)
	if summary == "" {
		t.Error("expected non-empty summary")
	}
}
