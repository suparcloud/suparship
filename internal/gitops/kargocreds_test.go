package gitops_test

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/gitops"
)

func TestEnsureKargoGitCred(t *testing.T) {
	client := fake.NewSimpleClientset()
	store := gitops.NewConfigStore(client)

	// Stored gitops repo config + credentials (in suparship-system).
	if err := store.SaveCredentials(context.Background(), "generic", "", "gituser", "gitpass"); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if err := store.Save(context.Background(), &gitops.RepoConfig{
		Provider:      "generic",
		RepoURL:       "https://github.com/org/gitops.git",
		Branch:        "main",
		AuthSecretRef: gitops.ManagedCredentialSecretName,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.EnsureKargoGitCred(context.Background(), "voiceai"); err != nil {
		t.Fatalf("EnsureKargoGitCred: %v", err)
	}

	sec, err := client.CoreV1().Secrets("voiceai").Get(context.Background(), gitops.KargoGitCredSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get kargo git cred secret: %v", err)
	}
	if sec.Labels["kargo.akuity.io/cred-type"] != "git" {
		t.Errorf("cred-type label = %q, want git", sec.Labels["kargo.akuity.io/cred-type"])
	}
	if string(sec.Data["repoURL"]) != "https://github.com/org/gitops.git" ||
		string(sec.Data["username"]) != "gituser" ||
		string(sec.Data["password"]) != "gitpass" {
		t.Errorf("git cred secret data = %v, want gitops repo/gituser/gitpass", sec.Data)
	}
}

func TestEnsureKargoGitCred_NoConfigIsNoop(t *testing.T) {
	client := fake.NewSimpleClientset()
	store := gitops.NewConfigStore(client)

	// No gitops config saved → no-op, no secret.
	if err := store.EnsureKargoGitCred(context.Background(), "voiceai"); err != nil {
		t.Fatalf("EnsureKargoGitCred: %v", err)
	}
	_, err := client.CoreV1().Secrets("voiceai").Get(context.Background(), gitops.KargoGitCredSecretName, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected no git cred secret when gitops unconfigured, got err=%v", err)
	}
}
