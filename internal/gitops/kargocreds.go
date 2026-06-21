package gitops

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/suparcloud/suparship/internal/k8s"
)

const (
	// KargoGitCredSecretName is the Secret, created in each Kargo Project
	// namespace, that lets Kargo's git-clone / git-push promotion steps
	// authenticate to the gitops repo. Kargo discovers it by the
	// kargo.akuity.io/cred-type=git label and matches it by repoURL.
	KargoGitCredSecretName = "suparship-gitops"
	kargoCredTypeLabel     = "kargo.akuity.io/cred-type"
	kargoCredTypeGit       = "git"
)

// kargoGitURL returns the gitops repo URL Kargo's promotion steps clone/push —
// the same resolution the publisher uses for Stage git-clone (KargoGitRepoURL,
// else ArgoCDRepoURL, else RepoURL).
func (c *RepoConfig) kargoGitURL() string {
	if c.KargoGitRepoURL != "" {
		return c.KargoGitRepoURL
	}
	if c.ArgoCDRepoURL != "" {
		return c.ArgoCDRepoURL
	}
	return c.RepoURL
}

// EnsureKargoGitCred provisions (or refreshes) the Kargo git-credential Secret in
// the given Kargo Project namespace from the gitops repo config, so Kargo's
// git-clone / git-push promotion steps can authenticate. It is a no-op when the
// gitops repo is unconfigured or has no stored credentials. The namespace is
// created if missing; Kargo adopts a pre-existing namespace matching the Project.
// Idempotent — safe to call on every publish / startup.
func (s *ConfigStore) EnsureKargoGitCred(ctx context.Context, projectNamespace string) error {
	cfg, err := s.Get(ctx)
	if err != nil {
		if errors.Is(err, ErrConfigNotFound) {
			return nil
		}
		return fmt.Errorf("read gitops config: %w", err)
	}
	repoURL := cfg.kargoGitURL()
	if repoURL == "" {
		return nil
	}
	username, password, err := s.GetCredentials(ctx, cfg)
	if err != nil {
		return fmt.Errorf("read gitops credentials: %w", err)
	}
	if password == "" {
		// Public repo or no stored creds — nothing to provision.
		return nil
	}

	if err := k8s.EnsureNamespace(ctx, s.client, projectNamespace); err != nil {
		return fmt.Errorf("ensure kargo namespace %q: %w", projectNamespace, err)
	}

	data := map[string][]byte{
		"repoURL":  []byte(repoURL),
		"username": []byte(username),
		"password": []byte(password),
	}
	labels := map[string]string{
		kargoCredTypeLabel:             kargoCredTypeGit,
		"app.kubernetes.io/managed-by": "suparship",
	}
	existing, err := s.client.CoreV1().Secrets(projectNamespace).Get(ctx, KargoGitCredSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, createErr := s.client.CoreV1().Secrets(projectNamespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: KargoGitCredSecretName, Namespace: projectNamespace, Labels: labels},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}, metav1.CreateOptions{})
		if createErr != nil {
			return fmt.Errorf("create kargo git cred secret %s/%s: %w", projectNamespace, KargoGitCredSecretName, createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get kargo git cred secret %s/%s: %w", projectNamespace, KargoGitCredSecretName, err)
	}
	if existing.Labels == nil {
		existing.Labels = map[string]string{}
	}
	for k, v := range labels {
		existing.Labels[k] = v
	}
	existing.Data = data
	existing.Type = corev1.SecretTypeOpaque
	if _, err := s.client.CoreV1().Secrets(projectNamespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update kargo git cred secret %s/%s: %w", projectNamespace, KargoGitCredSecretName, err)
	}
	return nil
}
