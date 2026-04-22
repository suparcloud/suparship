package gitops

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// ArgoCDNamespace is the namespace where ArgoCD is installed and where
	// the repository credential Secret must live.
	ArgoCDNamespace = "argocd"

	// argoCDRepoSecretName is the name of the managed ArgoCD repository Secret.
	argoCDRepoSecretName = "suparship-gitops-repo"
)

// RegisterArgoCDRepo creates or updates the ArgoCD repository credential
// Secret so ArgoCD can clone the GitOps repo for sync operations.
//
// ArgoCD discovers repositories from Secrets with the label
// `argocd.argoproj.io/secret-type: repository` in its namespace.
//
// The URL registered is cfg.ArgoCDRepoURL when set (the in-cluster URL ArgoCD
// uses to reach the repo), falling back to cfg.RepoURL. Credentials are the
// pre-resolved username and password/token values — callers should retrieve
// them via ConfigStore.GetCredentials before calling this function.
//
// The function is idempotent: calling it again with the same values is a
// no-op, and calling it with updated credentials replaces the previous ones.
func RegisterArgoCDRepo(ctx context.Context, client kubernetes.Interface, cfg *RepoConfig, username, password string) error {
	repoURL := cfg.ArgoCDRepoURL
	if repoURL == "" {
		repoURL = cfg.RepoURL
	}
	if repoURL == "" {
		return fmt.Errorf("cannot register ArgoCD repo: no repoURL configured")
	}

	data := map[string][]byte{
		"type": []byte("git"),
		"url":  []byte(repoURL),
	}
	if username != "" {
		data["username"] = []byte(username)
	}
	if password != "" {
		data["password"] = []byte(password)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      argoCDRepoSecretName,
			Namespace: ArgoCDNamespace,
			Labels: map[string]string{
				"argocd.argoproj.io/secret-type": "repository",
				"suparship.io/managed-by":         "suparship",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}

	existing, err := client.CoreV1().Secrets(ArgoCDNamespace).Get(ctx, argoCDRepoSecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, createErr := client.CoreV1().Secrets(ArgoCDNamespace).Create(ctx, secret, metav1.CreateOptions{})
			if createErr != nil {
				return fmt.Errorf("create argocd repo secret: %w", createErr)
			}
			return nil
		}
		return fmt.Errorf("get existing argocd repo secret: %w", err)
	}

	existing.Data = data
	existing.Labels = secret.Labels
	if _, err := client.CoreV1().Secrets(ArgoCDNamespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update argocd repo secret: %w", err)
	}
	return nil
}
