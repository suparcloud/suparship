package secrets

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// K8sVaultWriter implements VaultWriter by writing to native Kubernetes
// Secrets. This is the demo/dev fallback when no external vault is configured.
// It ignores VaultBinding.VaultID (the "vault" is the K8s namespace) and uses
// Scope to derive a deterministic Secret name.
type K8sVaultWriter struct {
	client kubernetes.Interface
	ns     string // target namespace, typically SystemNamespace
}

// NewK8sVaultWriter creates a K8sVaultWriter that writes to ns.
func NewK8sVaultWriter(client kubernetes.Interface, ns string) *K8sVaultWriter {
	return &K8sVaultWriter{client: client, ns: ns}
}

func (w *K8sVaultWriter) secretName(scope Scope) string {
	switch scope.Level {
	case LevelOrg:
		return OrgSecretName()
	case LevelEnvironment:
		return EnvTypeSecretName(scope.Env)
	case LevelProject:
		return ProjectSecretName(scope.Project)
	case LevelApp:
		return AppLevelSecretName(scope.Project, scope.App)
	case LevelAppEnv:
		return AppEnvSecretName(scope.Project, scope.App, scope.Env)
	default:
		return fmt.Sprintf("secrets-%s", scope.Level)
	}
}

func (w *K8sVaultWriter) Upsert(ctx context.Context, _ EnvBinding, scope Scope, _ string, data map[string][]byte) (ItemMeta, error) {
	name := w.secretName(scope)
	existing, err := w.client.CoreV1().Secrets(w.ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: w.ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "suparship",
					labelType:                      "app-secrets",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}
		created, createErr := w.client.CoreV1().Secrets(w.ns).Create(ctx, secret, metav1.CreateOptions{})
		if createErr != nil {
			return ItemMeta{}, fmt.Errorf("creating secret %s/%s: %w", w.ns, name, createErr)
		}
		return ItemMeta{Version: created.ResourceVersion}, nil
	}
	if err != nil {
		return ItemMeta{}, fmt.Errorf("reading secret %s/%s: %w", w.ns, name, err)
	}

	if existing.Data == nil {
		existing.Data = make(map[string][]byte)
	}
	for k, v := range data {
		existing.Data[k] = v
	}
	updated, err := w.client.CoreV1().Secrets(w.ns).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return ItemMeta{}, fmt.Errorf("updating secret %s/%s: %w", w.ns, name, err)
	}
	return ItemMeta{Version: updated.ResourceVersion}, nil
}

func (w *K8sVaultWriter) ListKeys(ctx context.Context, _ EnvBinding, scope Scope) ([]SecretEntry, ItemMeta, error) {
	name := w.secretName(scope)
	secret, err := w.client.CoreV1().Secrets(w.ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, ItemMeta{}, nil
	}
	if err != nil {
		return nil, ItemMeta{}, fmt.Errorf("reading secret %s/%s: %w", w.ns, name, err)
	}

	keys := make([]string, 0, len(secret.Data))
	for k := range secret.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	entries := make([]SecretEntry, len(keys))
	for i, k := range keys {
		entries[i] = SecretEntry{Key: k}
	}
	return entries, ItemMeta{Version: secret.ResourceVersion}, nil
}

func (w *K8sVaultWriter) DeleteKey(ctx context.Context, _ EnvBinding, scope Scope, key, _ string) (ItemMeta, error) {
	name := w.secretName(scope)
	secret, err := w.client.CoreV1().Secrets(w.ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return ItemMeta{}, nil
	}
	if err != nil {
		return ItemMeta{}, fmt.Errorf("reading secret %s/%s: %w", w.ns, name, err)
	}
	if _, exists := secret.Data[key]; !exists {
		return ItemMeta{Version: secret.ResourceVersion}, nil
	}
	delete(secret.Data, key)
	updated, err := w.client.CoreV1().Secrets(w.ns).Update(ctx, secret, metav1.UpdateOptions{})
	if err != nil {
		return ItemMeta{}, fmt.Errorf("updating secret %s/%s: %w", w.ns, name, err)
	}
	return ItemMeta{Version: updated.ResourceVersion}, nil
}

func (w *K8sVaultWriter) DeleteItem(ctx context.Context, _ EnvBinding, scope Scope) error {
	name := w.secretName(scope)
	err := w.client.CoreV1().Secrets(w.ns).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (w *K8sVaultWriter) Probe(ctx context.Context, _ EnvBinding) error {
	canary := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suparship-vault-probe",
			Namespace: w.ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "suparship",
				labelType:                      "probe",
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"probe": "ok"},
	}

	created, err := w.client.CoreV1().Secrets(w.ns).Create(ctx, canary, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("probe create failed: %w", err)
	}
	name := canary.Name
	if created != nil {
		name = created.Name
	}
	_, err = w.client.CoreV1().Secrets(w.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("probe read failed: %w", err)
	}
	err = w.client.CoreV1().Secrets(w.ns).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("probe delete failed: %w", err)
	}
	return nil
}
