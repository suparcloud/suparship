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

const (
	// SystemNamespace is where upper-level (org/envtype/project) secrets live.
	SystemNamespace = "suparship-system"

	replicatorAnnotation         = "replicator.v1.mittwald.de/replicate-to"
	replicatorMatchingAnnotation = "replicator.v1.mittwald.de/replicate-to-matching"

	labelManagedBy = "suparship.io/managed-by"
	labelType      = "suparship.io/type"
)

// K8sBackend implements Backend by writing to native Kubernetes Secrets.
type K8sBackend struct {
	client kubernetes.Interface
}

// NewK8sBackend creates a K8sBackend backed by client.
func NewK8sBackend(client kubernetes.Interface) *K8sBackend {
	return &K8sBackend{client: client}
}

func (b *K8sBackend) Upsert(ctx context.Context, ns, name string, data map[string][]byte) error {
	existing, err := b.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					labelManagedBy: "suparship",
					labelType:      "app-secrets",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}
		_, err = b.client.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
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
	_, err = b.client.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating secret %s/%s: %w", ns, name, err)
	}
	return nil
}

func (b *K8sBackend) ListKeys(ctx context.Context, ns, name string) ([]SecretEntry, error) {
	secret, err := b.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
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

func (b *K8sBackend) DeleteKey(ctx context.Context, ns, name, key string) error {
	secret, err := b.client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
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
	_, err = b.client.CoreV1().Secrets(ns).Update(ctx, secret, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating secret %s/%s: %w", ns, name, err)
	}
	return nil
}

// ── UpperLevelSecretWriter ──────────────────────────────────────────────────

// UpperLevelSecretWriter writes upper-level (Org / EnvType / Project) secrets
// to suparship-system with Stakater Replicator annotations so they are
// automatically replicated into app-env namespaces.
type UpperLevelSecretWriter struct {
	client kubernetes.Interface
}

// NewUpperLevelSecretWriter creates an UpperLevelSecretWriter backed by client.
func NewUpperLevelSecretWriter(client kubernetes.Interface) *UpperLevelSecretWriter {
	return &UpperLevelSecretWriter{client: client}
}

// WriteOrgSecrets upserts org-level secrets. Replicated to all namespaces.
func (w *UpperLevelSecretWriter) WriteOrgSecrets(ctx context.Context, data map[string][]byte) error {
	annotations := map[string]string{replicatorAnnotation: ".*"}
	return w.upsertSecret(ctx, OrgSecretName(), annotations, data)
}

// ReadOrgSecretKeys returns key names for org-level secrets.
func (w *UpperLevelSecretWriter) ReadOrgSecretKeys(ctx context.Context) ([]SecretEntry, error) {
	return w.readSecretKeys(ctx, OrgSecretName())
}

// DeleteOrgSecretKey removes a single key from org-level secrets.
func (w *UpperLevelSecretWriter) DeleteOrgSecretKey(ctx context.Context, key string) error {
	return w.deleteSecretKey(ctx, OrgSecretName(), key)
}

// WriteEnvTypeSecrets upserts env-type-level secrets. Replicated to
// namespaces matching ".*-{envType}".
func (w *UpperLevelSecretWriter) WriteEnvTypeSecrets(ctx context.Context, envType string, data map[string][]byte) error {
	annotations := map[string]string{
		replicatorAnnotation: fmt.Sprintf(".*-%s", envType),
	}
	return w.upsertSecret(ctx, EnvTypeSecretName(envType), annotations, data)
}

// ReadEnvTypeSecretKeys returns key names for env-type-level secrets.
func (w *UpperLevelSecretWriter) ReadEnvTypeSecretKeys(ctx context.Context, envType string) ([]SecretEntry, error) {
	return w.readSecretKeys(ctx, EnvTypeSecretName(envType))
}

// DeleteEnvTypeSecretKey removes a single key from env-type-level secrets.
func (w *UpperLevelSecretWriter) DeleteEnvTypeSecretKey(ctx context.Context, envType, key string) error {
	return w.deleteSecretKey(ctx, EnvTypeSecretName(envType), key)
}

// WriteProjectSecrets upserts project-level secrets. Replicated to namespaces
// with label suparship.io/project={project}.
func (w *UpperLevelSecretWriter) WriteProjectSecrets(ctx context.Context, project string, data map[string][]byte) error {
	annotations := map[string]string{
		replicatorMatchingAnnotation: fmt.Sprintf("suparship.io/project=%s", project),
	}
	return w.upsertSecret(ctx, ProjectSecretName(project), annotations, data)
}

// ReadProjectSecretKeys returns key names for project-level secrets.
func (w *UpperLevelSecretWriter) ReadProjectSecretKeys(ctx context.Context, project string) ([]SecretEntry, error) {
	return w.readSecretKeys(ctx, ProjectSecretName(project))
}

// DeleteProjectSecretKey removes a single key from project-level secrets.
func (w *UpperLevelSecretWriter) DeleteProjectSecretKey(ctx context.Context, project, key string) error {
	return w.deleteSecretKey(ctx, ProjectSecretName(project), key)
}

func (w *UpperLevelSecretWriter) upsertSecret(
	ctx context.Context,
	name string,
	annotations map[string]string,
	data map[string][]byte,
) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: SystemNamespace,
			Labels: map[string]string{
				labelManagedBy: "suparship",
				labelType:      "secrets",
			},
			Annotations: annotations,
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}

	existing, err := w.client.CoreV1().Secrets(SystemNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = w.client.CoreV1().Secrets(SystemNamespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating secret %s: %w", name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading secret %s: %w", name, err)
	}

	// Merge: preserve existing keys not in data.
	if existing.Data == nil {
		existing.Data = make(map[string][]byte)
	}
	for k, v := range data {
		existing.Data[k] = v
	}
	// Ensure our annotations and labels are always present.
	if existing.Annotations == nil {
		existing.Annotations = make(map[string]string)
	}
	for k, v := range annotations {
		existing.Annotations[k] = v
	}
	existing.Labels = secret.Labels

	_, err = w.client.CoreV1().Secrets(SystemNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating secret %s: %w", name, err)
	}
	return nil
}

func (w *UpperLevelSecretWriter) readSecretKeys(ctx context.Context, name string) ([]SecretEntry, error) {
	secret, err := w.client.CoreV1().Secrets(SystemNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading secret %s: %w", name, err)
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

func (w *UpperLevelSecretWriter) deleteSecretKey(ctx context.Context, name, key string) error {
	secret, err := w.client.CoreV1().Secrets(SystemNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading secret %s: %w", name, err)
	}
	if _, exists := secret.Data[key]; !exists {
		return nil
	}
	delete(secret.Data, key)
	_, err = w.client.CoreV1().Secrets(SystemNamespace).Update(ctx, secret, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating secret %s: %w", name, err)
	}
	return nil
}
