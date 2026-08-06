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

// K8sVaultStore implements VaultStore using native Kubernetes Secrets. The
// "vault" for a scope is a dedicated namespace (VaultName(scope)); each item
// is a Secret within it (ItemName(scope, tier, app)). ESO's per-scope
// ClusterSecretStore reads from these namespaces and materializes the
// <app>-global/-env/-cluster Secrets in app namespaces.
type K8sVaultStore struct {
	client kubernetes.Interface
}

// NewK8sVaultStore creates a K8sVaultStore backed by client.
func NewK8sVaultStore(client kubernetes.Interface) *K8sVaultStore {
	return &K8sVaultStore{client: client}
}

var _ VaultStore = (*K8sVaultStore)(nil)

// ensureNamespace creates the vault namespace if it does not already exist.
func (w *K8sVaultStore) ensureNamespace(ctx context.Context, ns string) error {
	_, err := w.client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("reading namespace %s: %w", ns, err)
	}
	_, err = w.client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: map[string]string{labelManagedBy: "suparship", labelType: "secret-vault"},
		},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %s: %w", ns, err)
	}
	return nil
}

func (w *K8sVaultStore) Upsert(ctx context.Context, scope Scope, tier Tier, app string, data map[string][]byte) error {
	ns := VaultName(scope)
	name := ItemName(scope, tier, app)
	if err := w.ensureNamespace(ctx, ns); err != nil {
		return err
	}

	existing, err := w.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					labelManagedBy: "suparship",
					labelType:      "vault-item",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}
		if _, err := w.client.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating secret %s/%s: %w", ns, name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading secret %s/%s: %w", ns, name, err)
	}

	if existing.Data == nil {
		existing.Data = make(map[string][]byte)
	}
	for k, v := range data {
		existing.Data[k] = v
	}
	if _, err := w.client.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating secret %s/%s: %w", ns, name, err)
	}
	return nil
}

func (w *K8sVaultStore) EnsureItem(ctx context.Context, scope Scope, tier Tier, app string) error {
	ns := VaultName(scope)
	name := ItemName(scope, tier, app)
	_, err := w.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil // already exists — leave its data untouched
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("reading secret %s/%s: %w", ns, name, err)
	}
	if err := w.ensureNamespace(ctx, ns); err != nil {
		return err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{labelManagedBy: "suparship", labelType: "vault-item"},
		},
		Type: corev1.SecretTypeOpaque,
	}
	if _, err := w.client.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating empty secret %s/%s: %w", ns, name, err)
	}
	return nil
}

func (w *K8sVaultStore) ListKeys(ctx context.Context, scope Scope, tier Tier, app string) ([]SecretEntry, error) {
	ns := VaultName(scope)
	name := ItemName(scope, tier, app)
	secret, err := w.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading secret %s/%s: %w", ns, name, err)
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
	return entries, nil
}

func (w *K8sVaultStore) DeleteKey(ctx context.Context, scope Scope, tier Tier, app, key string) error {
	ns := VaultName(scope)
	name := ItemName(scope, tier, app)
	secret, err := w.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading secret %s/%s: %w", ns, name, err)
	}
	if _, exists := secret.Data[key]; !exists {
		return nil
	}
	delete(secret.Data, key)
	if _, err := w.client.CoreV1().Secrets(ns).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating secret %s/%s: %w", ns, name, err)
	}
	return nil
}

var _ LegacyItemMigrator = (*K8sVaultStore)(nil)

// CopyItem implements LegacyItemMigrator: copy the source Secret's data into a
// new-named Secret in the same vault namespace (merge). No-op when the source is
// absent.
func (w *K8sVaultStore) CopyItem(ctx context.Context, scope Scope, fromName, toName string) error {
	ns := VaultName(scope)
	src, err := w.client.CoreV1().Secrets(ns).Get(ctx, fromName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading secret %s/%s: %w", ns, fromName, err)
	}
	if err := w.ensureNamespace(ctx, ns); err != nil {
		return err
	}
	dst, err := w.client.CoreV1().Secrets(ns).Get(ctx, toName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      toName,
				Namespace: ns,
				Labels:    map[string]string{labelManagedBy: "suparship", labelType: "vault-item"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: src.Data,
		}
		if _, err := w.client.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating secret %s/%s: %w", ns, toName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading secret %s/%s: %w", ns, toName, err)
	}
	if dst.Data == nil {
		dst.Data = make(map[string][]byte)
	}
	for k, v := range src.Data {
		dst.Data[k] = v
	}
	if _, err := w.client.CoreV1().Secrets(ns).Update(ctx, dst, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating secret %s/%s: %w", ns, toName, err)
	}
	return nil
}

// DeleteItem implements LegacyItemMigrator: delete the whole Secret by name.
func (w *K8sVaultStore) DeleteItem(ctx context.Context, scope Scope, itemName string) error {
	ns := VaultName(scope)
	err := w.client.CoreV1().Secrets(ns).Delete(ctx, itemName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting secret %s/%s: %w", ns, itemName, err)
	}
	return nil
}

var _ ItemExporter = (*K8sVaultStore)(nil)

// ExportItem implements ItemExporter: the Secret's full data, for
// cross-backend migration only. Absent item → (nil, nil).
func (w *K8sVaultStore) ExportItem(ctx context.Context, scope Scope, tier Tier, app string) (map[string][]byte, error) {
	ns := VaultName(scope)
	name := ItemName(scope, tier, app)
	secret, err := w.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading secret %s/%s: %w", ns, name, err)
	}
	out := make(map[string][]byte, len(secret.Data))
	for k, v := range secret.Data {
		out[k] = append([]byte(nil), v...)
	}
	return out, nil
}

func (w *K8sVaultStore) Probe(ctx context.Context, scope Scope) error {
	ns := VaultName(scope)
	if err := w.ensureNamespace(ctx, ns); err != nil {
		return err
	}
	_, err := w.client.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("probe list %s: %w", ns, err)
	}
	return nil
}
