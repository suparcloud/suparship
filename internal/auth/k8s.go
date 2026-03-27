package auth

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// GetAdminSecret reads the admin credentials from the Kubernetes Secret.
// Returns (nil, nil) if the Secret does not exist.
func GetAdminSecret(ctx context.Context, client kubernetes.Interface) (*Credentials, error) {
	secret, err := client.CoreV1().Secrets(SecretNamespace).Get(
		ctx, SecretName, metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting secret %s/%s: %w", SecretNamespace, SecretName, err)
	}

	data := make(map[string]string, len(secret.Data))
	for k, v := range secret.Data {
		data[k] = string(v)
	}

	return CredentialsFromSecretData(data)
}

// CreateAdminSecret writes new admin credentials as a Kubernetes Secret.
func CreateAdminSecret(ctx context.Context, client kubernetes.Interface, creds *Credentials) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      SecretName,
			Namespace: SecretNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "suparship",
				"app.kubernetes.io/component":  "admin-auth",
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: creds.SecretData(),
	}

	_, err := client.CoreV1().Secrets(SecretNamespace).Create(
		ctx, secret, metav1.CreateOptions{},
	)
	if err != nil {
		return fmt.Errorf("creating secret %s/%s: %w", SecretNamespace, SecretName, err)
	}

	return nil
}

// UpdateAdminSecret replaces the admin credentials in an existing Secret.
func UpdateAdminSecret(ctx context.Context, client kubernetes.Interface, creds *Credentials) error {
	secret, err := client.CoreV1().Secrets(SecretNamespace).Get(
		ctx, SecretName, metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("getting secret %s/%s for update: %w", SecretNamespace, SecretName, err)
	}

	secret.Data = nil
	secret.StringData = creds.SecretData()

	_, err = client.CoreV1().Secrets(SecretNamespace).Update(
		ctx, secret, metav1.UpdateOptions{},
	)
	if err != nil {
		return fmt.Errorf("updating secret %s/%s: %w", SecretNamespace, SecretName, err)
	}

	return nil
}
