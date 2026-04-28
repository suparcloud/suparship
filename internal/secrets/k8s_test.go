package secrets

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// ── K8sBackend tests ────────────────────────────────────────────────────────

func TestK8sBackend_UpsertCreatesSecret(t *testing.T) {
	client := fake.NewSimpleClientset()
	b := NewK8sBackend(client)
	ctx := context.Background()

	err := b.Upsert(ctx, "app-ns", "my-secret", map[string][]byte{
		"DB_URL": []byte("postgres://localhost"),
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	secret, err := client.CoreV1().Secrets("app-ns").Get(ctx, "my-secret", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get secret: %v", err)
	}
	if string(secret.Data["DB_URL"]) != "postgres://localhost" {
		t.Errorf("got %q, want %q", string(secret.Data["DB_URL"]), "postgres://localhost")
	}
	if secret.Labels[labelManagedBy] != "suparship" {
		t.Error("missing managed-by label")
	}
}

func TestK8sBackend_UpsertMergesExisting(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "app-ns"},
		Data:       map[string][]byte{"OLD_KEY": []byte("old")},
	})
	b := NewK8sBackend(client)
	ctx := context.Background()

	err := b.Upsert(ctx, "app-ns", "my-secret", map[string][]byte{
		"NEW_KEY": []byte("new"),
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	secret, err := client.CoreV1().Secrets("app-ns").Get(ctx, "my-secret", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(secret.Data["OLD_KEY"]) != "old" {
		t.Error("existing key was lost")
	}
	if string(secret.Data["NEW_KEY"]) != "new" {
		t.Error("new key was not added")
	}
}

func TestK8sBackend_ListKeys(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "app-ns"},
		Data: map[string][]byte{
			"B_KEY": []byte("b"),
			"A_KEY": []byte("a"),
		},
	})
	b := NewK8sBackend(client)
	ctx := context.Background()

	entries, err := b.ListKeys(ctx, "app-ns", "my-secret")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Key != "A_KEY" || entries[1].Key != "B_KEY" {
		t.Errorf("keys not sorted: %v", entries)
	}
}

func TestK8sBackend_ListKeysNotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	b := NewK8sBackend(client)
	ctx := context.Background()

	entries, err := b.ListKeys(ctx, "app-ns", "nonexistent")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}

func TestK8sBackend_DeleteKey(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "app-ns"},
		Data: map[string][]byte{
			"KEEP":   []byte("keep"),
			"DELETE": []byte("delete"),
		},
	})
	b := NewK8sBackend(client)
	ctx := context.Background()

	err := b.DeleteKey(ctx, "app-ns", "my-secret", "DELETE")
	if err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}

	secret, err := client.CoreV1().Secrets("app-ns").Get(ctx, "my-secret", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, exists := secret.Data["DELETE"]; exists {
		t.Error("key should have been deleted")
	}
	if string(secret.Data["KEEP"]) != "keep" {
		t.Error("unrelated key was lost")
	}
}

func TestK8sBackend_DeleteKeyNotFound(t *testing.T) {
	client := fake.NewSimpleClientset()
	b := NewK8sBackend(client)
	ctx := context.Background()

	err := b.DeleteKey(ctx, "app-ns", "nonexistent", "KEY")
	if err != nil {
		t.Fatalf("DeleteKey on nonexistent secret should be no-op: %v", err)
	}
}

func TestK8sBackend_DeleteKeyMissing(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "app-ns"},
		Data:       map[string][]byte{"OTHER": []byte("x")},
	})
	b := NewK8sBackend(client)
	ctx := context.Background()

	err := b.DeleteKey(ctx, "app-ns", "my-secret", "NONEXISTENT")
	if err != nil {
		t.Fatalf("DeleteKey on missing key should be no-op: %v", err)
	}
}

// ── UpperLevelSecretWriter tests ────────────────────────────────────────────

func TestUpperLevelWriter_OrgSecrets(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := NewUpperLevelSecretWriter(client)
	ctx := context.Background()

	err := w.WriteOrgSecrets(ctx, map[string][]byte{
		"GLOBAL_KEY": []byte("global-value"),
	})
	if err != nil {
		t.Fatalf("WriteOrgSecrets: %v", err)
	}

	// Verify secret was created in suparship-system.
	secret, err := client.CoreV1().Secrets(SystemNamespace).Get(ctx, OrgSecretName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(secret.Data["GLOBAL_KEY"]) != "global-value" {
		t.Errorf("got %q, want %q", string(secret.Data["GLOBAL_KEY"]), "global-value")
	}
	if secret.Annotations[replicatorAnnotation] != ".*" {
		t.Errorf("replicator annotation = %q, want %q", secret.Annotations[replicatorAnnotation], ".*")
	}
	if secret.Labels[labelType] != "secrets" {
		t.Errorf("type label = %q, want %q", secret.Labels[labelType], "secrets")
	}

	// Read keys.
	keys, err := w.ReadOrgSecretKeys(ctx)
	if err != nil {
		t.Fatalf("ReadOrgSecretKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].Key != "GLOBAL_KEY" {
		t.Errorf("unexpected keys: %v", keys)
	}

	// Delete key.
	if err := w.DeleteOrgSecretKey(ctx, "GLOBAL_KEY"); err != nil {
		t.Fatalf("DeleteOrgSecretKey: %v", err)
	}
	keys, _ = w.ReadOrgSecretKeys(ctx)
	if len(keys) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(keys))
	}
}

func TestUpperLevelWriter_EnvTypeSecrets(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := NewUpperLevelSecretWriter(client)
	ctx := context.Background()

	err := w.WriteEnvTypeSecrets(ctx, "staging", map[string][]byte{
		"STAGING_KEY": []byte("staging"),
	})
	if err != nil {
		t.Fatalf("WriteEnvTypeSecrets: %v", err)
	}

	secret, err := client.CoreV1().Secrets(SystemNamespace).Get(ctx, EnvTypeSecretName("staging"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if secret.Annotations[replicatorAnnotation] != ".*-staging" {
		t.Errorf("replicator annotation = %q, want %q", secret.Annotations[replicatorAnnotation], ".*-staging")
	}

	keys, _ := w.ReadEnvTypeSecretKeys(ctx, "staging")
	if len(keys) != 1 || keys[0].Key != "STAGING_KEY" {
		t.Errorf("unexpected keys: %v", keys)
	}

	w.DeleteEnvTypeSecretKey(ctx, "staging", "STAGING_KEY")
	keys, _ = w.ReadEnvTypeSecretKeys(ctx, "staging")
	if len(keys) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(keys))
	}
}

func TestUpperLevelWriter_ProjectSecrets(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := NewUpperLevelSecretWriter(client)
	ctx := context.Background()

	err := w.WriteProjectSecrets(ctx, "demo", map[string][]byte{
		"PROJ_KEY": []byte("proj"),
	})
	if err != nil {
		t.Fatalf("WriteProjectSecrets: %v", err)
	}

	secret, err := client.CoreV1().Secrets(SystemNamespace).Get(ctx, ProjectSecretName("demo"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if secret.Annotations[replicatorMatchingAnnotation] != "suparship.io/project=demo" {
		t.Errorf("replicator-matching annotation = %q", secret.Annotations[replicatorMatchingAnnotation])
	}

	keys, _ := w.ReadProjectSecretKeys(ctx, "demo")
	if len(keys) != 1 || keys[0].Key != "PROJ_KEY" {
		t.Errorf("unexpected keys: %v", keys)
	}
}

func TestUpperLevelWriter_ClusterSecrets(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := NewUpperLevelSecretWriter(client)
	ctx := context.Background()

	err := w.WriteClusterSecrets(ctx, "prod-us", map[string][]byte{
		"FEATURE_FLAG": []byte("off"),
	})
	if err != nil {
		t.Fatalf("WriteClusterSecrets: %v", err)
	}

	secret, err := client.CoreV1().Secrets(SystemNamespace).Get(ctx, ClusterSecretName("prod-us"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got, want := secret.Annotations[replicatorMatchingAnnotation], "suparship.io/cluster=prod-us"; got != want {
		t.Errorf("replicator-matching annotation = %q, want %q", got, want)
	}

	keys, _ := w.ReadClusterSecretKeys(ctx, "prod-us")
	if len(keys) != 1 || keys[0].Key != "FEATURE_FLAG" {
		t.Errorf("unexpected keys: %v", keys)
	}

	if err := w.DeleteClusterSecretKey(ctx, "prod-us", "FEATURE_FLAG"); err != nil {
		t.Fatalf("DeleteClusterSecretKey: %v", err)
	}
	keys, _ = w.ReadClusterSecretKeys(ctx, "prod-us")
	if len(keys) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(keys))
	}
}

func TestUpperLevelWriter_MergeExisting(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := NewUpperLevelSecretWriter(client)
	ctx := context.Background()

	w.WriteOrgSecrets(ctx, map[string][]byte{"A": []byte("a")})
	w.WriteOrgSecrets(ctx, map[string][]byte{"B": []byte("b")})

	keys, _ := w.ReadOrgSecretKeys(ctx)
	if len(keys) != 2 {
		t.Errorf("expected 2 keys after merge, got %d", len(keys))
	}
}

func TestUpperLevelWriter_ReadEmpty(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := NewUpperLevelSecretWriter(client)
	ctx := context.Background()

	keys, err := w.ReadOrgSecretKeys(ctx)
	if err != nil {
		t.Fatalf("ReadOrgSecretKeys on empty: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected empty, got %d", len(keys))
	}
}
