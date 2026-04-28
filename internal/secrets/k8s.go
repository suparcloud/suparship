package secrets

import (
	"context"
	"fmt"
	"regexp"
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

// WriteClusterSecrets upserts cluster-level secrets. Replicated to namespaces
// labelled "suparship.io/cluster={cluster}" so apps deployed onto that cluster
// receive the override.
func (w *UpperLevelSecretWriter) WriteClusterSecrets(ctx context.Context, cluster string, data map[string][]byte) error {
	annotations := map[string]string{
		replicatorMatchingAnnotation: fmt.Sprintf("suparship.io/cluster=%s", cluster),
	}
	return w.upsertSecret(ctx, ClusterSecretName(cluster), annotations, data)
}

// ReadClusterSecretKeys returns key names for cluster-level secrets.
func (w *UpperLevelSecretWriter) ReadClusterSecretKeys(ctx context.Context, cluster string) ([]SecretEntry, error) {
	return w.readSecretKeys(ctx, ClusterSecretName(cluster))
}

// DeleteClusterSecretKey removes a single key from cluster-level secrets.
func (w *UpperLevelSecretWriter) DeleteClusterSecretKey(ctx context.Context, cluster, key string) error {
	return w.deleteSecretKey(ctx, ClusterSecretName(cluster), key)
}

// ── App scope (shared across every env of one app) ────────────────────────
//
// App-level secrets used to be written into the per-env Kubernetes namespace
// returned by resolveAnyAppNamespace. That broke when the namespace did not
// exist yet (apps in "Not deployed" state), and also meant the secret only
// reached one env's namespace despite being labelled "shared across all
// envs of this app". They now live in suparship-system with a Stakater
// Replicator annotation matching app namespaces — the same pattern used by
// project-level secrets.
//
// Replicator label match: suparship.io/project={project},suparship.io/app={app}
// (label conjunction). Namespaces created by the suparship publisher carry
// the project label; the app label needs to be added by the chart or
// manually for the replicator to copy across.

// WriteAppSecrets upserts app-level secrets to suparship-system.
func (w *UpperLevelSecretWriter) WriteAppSecrets(ctx context.Context, project, app string, data map[string][]byte) error {
	annotations := map[string]string{
		replicatorMatchingAnnotation: fmt.Sprintf("suparship.io/project=%s,suparship.io/app=%s", project, app),
	}
	return w.upsertSecret(ctx, AppLevelSecretName(project, app), annotations, data)
}

// ReadAppSecretKeys returns key names for app-level secrets.
func (w *UpperLevelSecretWriter) ReadAppSecretKeys(ctx context.Context, project, app string) ([]SecretEntry, error) {
	return w.readSecretKeys(ctx, AppLevelSecretName(project, app))
}

// DeleteAppSecretKey removes a single key from app-level secrets.
func (w *UpperLevelSecretWriter) DeleteAppSecretKey(ctx context.Context, project, app, key string) error {
	return w.deleteSecretKey(ctx, AppLevelSecretName(project, app), key)
}

// ── App-env scope (one env of one app) ────────────────────────────────────
//
// App-env secrets used to be written directly into env.Namespace, which fails
// when the namespace doesn't exist yet (apps in "Not deployed" state). They
// now live in suparship-system with a replicate-to annotation matching the
// resolved env namespace by name. Once ArgoCD creates the namespace, the
// replicator copies the Secret over.

// WriteAppEnvSecrets upserts app-env secrets to suparship-system. namespace is
// the resolved env namespace name; the replicator will copy the Secret into
// that namespace once it exists.
func (w *UpperLevelSecretWriter) WriteAppEnvSecrets(ctx context.Context, project, app, env, namespace string, data map[string][]byte) error {
	pattern := "^$" // match nothing if no namespace resolved yet
	if namespace != "" {
		pattern = fmt.Sprintf("^%s$", regexp.QuoteMeta(namespace))
	}
	annotations := map[string]string{
		replicatorAnnotation: pattern,
	}
	return w.upsertSecret(ctx, AppEnvSecretName(project, app, env), annotations, data)
}

// ReadAppEnvSecretKeys returns key names for app-env secrets.
func (w *UpperLevelSecretWriter) ReadAppEnvSecretKeys(ctx context.Context, project, app, env string) ([]SecretEntry, error) {
	return w.readSecretKeys(ctx, AppEnvSecretName(project, app, env))
}

// DeleteAppEnvSecretKey removes a single key from app-env secrets.
func (w *UpperLevelSecretWriter) DeleteAppEnvSecretKey(ctx context.Context, project, app, env, key string) error {
	return w.deleteSecretKey(ctx, AppEnvSecretName(project, app, env), key)
}

// ── Per-key value readers (used by MigrateUpperLevelSecrets) ─────────────

// ReadOrgSecretValue returns the raw value for a single key in the org-level
// secret. Returns nil bytes (and no error) when the secret or key does not
// exist — callers should treat this as "missing" and skip.
func (w *UpperLevelSecretWriter) ReadOrgSecretValue(ctx context.Context, key string) ([]byte, error) {
	return w.readSecretValue(ctx, OrgSecretName(), key)
}

// ReadEnvTypeSecretValue returns the raw value for a key in an env-type Secret.
func (w *UpperLevelSecretWriter) ReadEnvTypeSecretValue(ctx context.Context, envType, key string) ([]byte, error) {
	return w.readSecretValue(ctx, EnvTypeSecretName(envType), key)
}

// ReadProjectSecretValue returns the raw value for a key in a project Secret.
func (w *UpperLevelSecretWriter) ReadProjectSecretValue(ctx context.Context, project, key string) ([]byte, error) {
	return w.readSecretValue(ctx, ProjectSecretName(project), key)
}

// ReadClusterSecretValue returns the raw value for a key in a cluster Secret.
func (w *UpperLevelSecretWriter) ReadClusterSecretValue(ctx context.Context, cluster, key string) ([]byte, error) {
	return w.readSecretValue(ctx, ClusterSecretName(cluster), key)
}

func (w *UpperLevelSecretWriter) readSecretValue(ctx context.Context, name, key string) ([]byte, error) {
	secret, err := w.client.CoreV1().Secrets(SystemNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading secret %s: %w", name, err)
	}
	return secret.Data[key], nil
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
