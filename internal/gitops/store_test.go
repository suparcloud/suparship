package gitops

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/envconfig"
)

func TestConfigStore_GetNotFound(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envconfig.SystemNamespace}},
	)
	store := NewConfigStore(client)

	_, err := store.Get(context.Background())
	if !errors.Is(err, ErrConfigNotFound) {
		t.Errorf("expected ErrConfigNotFound, got %v", err)
	}
}

func TestConfigStore_SaveAndGet(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envconfig.SystemNamespace}},
	)
	store := NewConfigStore(client)
	ctx := context.Background()

	cfg := &RepoConfig{
		Provider:       "github",
		RepoURL:        "https://github.com/org/repo",
		Branch:         "main",
		AuthSecretRef:  "my-secret",
		InitializeRepo: true,
		GitHub:         &GitHubConfig{AppID: "12345", InstallationID: "67890"},
	}

	if err := store.Save(ctx, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Provider != "github" {
		t.Errorf("provider = %q, want github", got.Provider)
	}
	if got.RepoURL != cfg.RepoURL {
		t.Errorf("repoURL = %q, want %q", got.RepoURL, cfg.RepoURL)
	}
	if got.AuthSecretRef != "my-secret" {
		t.Errorf("authSecretRef = %q, want my-secret", got.AuthSecretRef)
	}
	if got.GitHub == nil || got.GitHub.AppID != "12345" {
		t.Errorf("github.appId = %v, want 12345", got.GitHub)
	}
}

func TestConfigStore_Update(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: envconfig.SystemNamespace}},
	)
	store := NewConfigStore(client)
	ctx := context.Background()

	cfg := &RepoConfig{
		Provider: "github",
		RepoURL:  "https://github.com/org/repo",
		Branch:   "main",
	}
	if err := store.Save(ctx, cfg); err != nil {
		t.Fatalf("initial save: %v", err)
	}

	cfg.Branch = "develop"
	cfg.SubPath = "gitops/"
	if err := store.Save(ctx, cfg); err != nil {
		t.Fatalf("update save: %v", err)
	}

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Branch != "develop" {
		t.Errorf("branch = %q, want develop", got.Branch)
	}
	if got.SubPath != "gitops/" {
		t.Errorf("subPath = %q, want gitops/", got.SubPath)
	}
}

func TestConfigStore_SaveValidation(t *testing.T) {
	client := fake.NewSimpleClientset()
	store := NewConfigStore(client)

	err := store.Save(context.Background(), &RepoConfig{})
	if !errors.Is(err, ErrMissingRepoURL) {
		t.Errorf("expected ErrMissingRepoURL, got %v", err)
	}
}

func TestConfigStore_GetCredentials(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gitops-creds",
			Namespace: envconfig.SystemNamespace,
		},
		Data: map[string][]byte{
			"token": []byte("ghp_abc123"),
		},
	}
	client := fake.NewSimpleClientset(secret)
	store := NewConfigStore(client)

	cfg := &RepoConfig{
		Provider:      "github",
		RepoURL:       "https://github.com/org/repo",
		AuthSecretRef: "gitops-creds",
	}

	user, pass, err := store.GetCredentials(context.Background(), cfg)
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if user != "x-access-token" {
		t.Errorf("username = %q, want x-access-token", user)
	}
	if pass != "ghp_abc123" {
		t.Errorf("password = %q, want ghp_abc123", pass)
	}
}

func TestConfigStore_GetCredentials_Bitbucket(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "bb-creds",
			Namespace: envconfig.SystemNamespace,
		},
		Data: map[string][]byte{
			"username":    []byte("myuser"),
			"appPassword": []byte("secret-app-pw"),
		},
	}
	client := fake.NewSimpleClientset(secret)
	store := NewConfigStore(client)

	cfg := &RepoConfig{
		Provider:      "bitbucket",
		RepoURL:       "https://bitbucket.org/org/repo",
		AuthSecretRef: "bb-creds",
	}

	user, pass, err := store.GetCredentials(context.Background(), cfg)
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if user != "myuser" {
		t.Errorf("username = %q, want myuser", user)
	}
	if pass != "secret-app-pw" {
		t.Errorf("password = %q, want secret-app-pw", pass)
	}
}

func TestConfigStore_GetCredentials_NoRef(t *testing.T) {
	client := fake.NewSimpleClientset()
	store := NewConfigStore(client)

	cfg := &RepoConfig{
		Provider: "github",
		RepoURL:  "https://github.com/org/repo",
	}

	user, pass, err := store.GetCredentials(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user != "" || pass != "" {
		t.Errorf("expected empty creds, got user=%q pass=%q", user, pass)
	}
}
