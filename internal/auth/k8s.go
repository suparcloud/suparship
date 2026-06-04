package auth

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// GetAdminSecret reads the admin credentials from the Kubernetes Secret
// located by ref. Returns (nil, nil) if the Secret does not exist.
func GetAdminSecret(ctx context.Context, client kubernetes.Interface, ref SecretRef) (*Credentials, error) {
	ref = ref.WithDefaults()

	secret, err := client.CoreV1().Secrets(ref.Namespace).Get(
		ctx, ref.Name, metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	data := make(map[string]string, len(secret.Data))
	for k, v := range secret.Data {
		data[k] = string(v)
	}

	return CredentialsFromSecretData(data, ref)
}

// CreateAdminSecret writes new admin credentials as a Kubernetes Secret at ref.
func CreateAdminSecret(ctx context.Context, client kubernetes.Interface, ref SecretRef, creds *Credentials) error {
	ref = ref.WithDefaults()

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ref.Name,
			Namespace: ref.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "suparship",
				"app.kubernetes.io/component":  "admin-auth",
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: creds.SecretData(ref),
	}

	_, err := client.CoreV1().Secrets(ref.Namespace).Create(
		ctx, secret, metav1.CreateOptions{},
	)
	if err != nil {
		return fmt.Errorf("creating secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	return nil
}

// UpdateAdminSecret replaces the admin credentials in an existing Secret at ref.
func UpdateAdminSecret(ctx context.Context, client kubernetes.Interface, ref SecretRef, creds *Credentials) error {
	ref = ref.WithDefaults()

	secret, err := client.CoreV1().Secrets(ref.Namespace).Get(
		ctx, ref.Name, metav1.GetOptions{},
	)
	if err != nil {
		return fmt.Errorf("getting secret %s/%s for update: %w", ref.Namespace, ref.Name, err)
	}

	secret.Data = nil
	secret.StringData = creds.SecretData(ref)

	_, err = client.CoreV1().Secrets(ref.Namespace).Update(
		ctx, secret, metav1.UpdateOptions{},
	)
	if err != nil {
		return fmt.Errorf("updating secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}

	return nil
}
