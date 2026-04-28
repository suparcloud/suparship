package secrets

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestMigrateUpperLevelSecrets_CopiesAllScopes(t *testing.T) {
	// Source: K8s UpperLevelSecretWriter pre-loaded with secrets at each scope.
	src := fake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: OrgSecretName(), Namespace: SystemNamespace},
			Data:       map[string][]byte{"GLOBAL_KEY": []byte("global")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: EnvTypeSecretName("staging"), Namespace: SystemNamespace},
			Data:       map[string][]byte{"DB_URL": []byte("staging-db")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: ProjectSecretName("demo"), Namespace: SystemNamespace},
			Data:       map[string][]byte{"PROJ_KEY": []byte("proj-val")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: ClusterSecretName("kind-staging"), Namespace: SystemNamespace},
			Data:       map[string][]byte{"FEATURE_FLAG": []byte("off")},
		},
	)
	srcWriter := NewUpperLevelSecretWriter(src)
	dst := NewMemUpperLevelWriter(NewMemBackend())

	res, err := MigrateUpperLevelSecrets(context.Background(), srcWriter, dst, MigrateUpperLevelInput{
		EnvTypes: []string{"staging"},
		Projects: []string{"demo"},
		Clusters: []string{"kind-staging"},
	})
	if err != nil {
		t.Fatalf("MigrateUpperLevelSecrets: %v", err)
	}

	if res.OrgKeys != 1 {
		t.Errorf("OrgKeys = %d, want 1", res.OrgKeys)
	}
	if res.EnvTypeKeys["staging"] != 1 {
		t.Errorf("EnvTypeKeys[staging] = %d, want 1", res.EnvTypeKeys["staging"])
	}
	if res.ProjectKeys["demo"] != 1 {
		t.Errorf("ProjectKeys[demo] = %d, want 1", res.ProjectKeys["demo"])
	}
	if res.ClusterKeys["kind-staging"] != 1 {
		t.Errorf("ClusterKeys[kind-staging] = %d, want 1", res.ClusterKeys["kind-staging"])
	}

	// Verify dst received the data.
	keys, _ := dst.ReadOrgSecretKeys(context.Background())
	if len(keys) != 1 || keys[0].Key != "GLOBAL_KEY" {
		t.Errorf("dst org keys = %+v, want one GLOBAL_KEY", keys)
	}
	keys, _ = dst.ReadClusterSecretKeys(context.Background(), "kind-staging")
	if len(keys) != 1 || keys[0].Key != "FEATURE_FLAG" {
		t.Errorf("dst cluster keys = %+v, want one FEATURE_FLAG", keys)
	}
}

func TestMigrateUpperLevelSecrets_NoOpWhenSourceEmpty(t *testing.T) {
	srcWriter := NewUpperLevelSecretWriter(fake.NewSimpleClientset())
	dst := NewMemUpperLevelWriter(NewMemBackend())

	res, err := MigrateUpperLevelSecrets(context.Background(), srcWriter, dst, MigrateUpperLevelInput{
		EnvTypes: []string{"staging", "prod"},
		Projects: []string{"demo"},
	})
	if err != nil {
		t.Fatalf("MigrateUpperLevelSecrets: %v", err)
	}
	if res.OrgKeys != 0 || len(res.EnvTypeKeys) != 0 || len(res.ProjectKeys) != 0 {
		t.Errorf("expected zero counts on empty source, got %+v", res)
	}
}

func TestMigrateUpperLevelSecrets_ReturnsPartialOnError(t *testing.T) {
	// Source has org but write to dst will fail mid-stream.
	src := fake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: OrgSecretName(), Namespace: SystemNamespace},
			Data:       map[string][]byte{"K1": []byte("v")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: EnvTypeSecretName("staging"), Namespace: SystemNamespace},
			Data:       map[string][]byte{"K2": []byte("v")},
		},
	)
	srcWriter := NewUpperLevelSecretWriter(src)
	dst := &failingWriter{
		base:        NewMemUpperLevelWriter(NewMemBackend()),
		failOnLevel: "envtype",
	}

	res, err := MigrateUpperLevelSecrets(context.Background(), srcWriter, dst, MigrateUpperLevelInput{
		EnvTypes: []string{"staging"},
	})
	if err == nil {
		t.Fatal("expected error from failing writer")
	}
	if res.OrgKeys != 1 {
		t.Errorf("expected partial progress to record 1 org key copied, got %d", res.OrgKeys)
	}
	if res.EnvTypeKeys["staging"] != 0 {
		t.Errorf("expected env-type to be zero (failed), got %d", res.EnvTypeKeys["staging"])
	}
}

// failingWriter wraps an UpperLevelWriter and fails one specified level.
type failingWriter struct {
	base        UpperLevelWriter
	failOnLevel string // "org" | "envtype" | "project" | "cluster"
}

func (f *failingWriter) WriteOrgSecrets(ctx context.Context, data map[string][]byte) error {
	if f.failOnLevel == "org" {
		return errors.New("forced")
	}
	return f.base.WriteOrgSecrets(ctx, data)
}
func (f *failingWriter) ReadOrgSecretKeys(ctx context.Context) ([]SecretEntry, error) {
	return f.base.ReadOrgSecretKeys(ctx)
}
func (f *failingWriter) DeleteOrgSecretKey(ctx context.Context, key string) error {
	return f.base.DeleteOrgSecretKey(ctx, key)
}
func (f *failingWriter) WriteEnvTypeSecrets(ctx context.Context, envType string, data map[string][]byte) error {
	if f.failOnLevel == "envtype" {
		return errors.New("forced")
	}
	return f.base.WriteEnvTypeSecrets(ctx, envType, data)
}
func (f *failingWriter) ReadEnvTypeSecretKeys(ctx context.Context, envType string) ([]SecretEntry, error) {
	return f.base.ReadEnvTypeSecretKeys(ctx, envType)
}
func (f *failingWriter) DeleteEnvTypeSecretKey(ctx context.Context, envType, key string) error {
	return f.base.DeleteEnvTypeSecretKey(ctx, envType, key)
}
func (f *failingWriter) WriteProjectSecrets(ctx context.Context, project string, data map[string][]byte) error {
	if f.failOnLevel == "project" {
		return errors.New("forced")
	}
	return f.base.WriteProjectSecrets(ctx, project, data)
}
func (f *failingWriter) ReadProjectSecretKeys(ctx context.Context, project string) ([]SecretEntry, error) {
	return f.base.ReadProjectSecretKeys(ctx, project)
}
func (f *failingWriter) DeleteProjectSecretKey(ctx context.Context, project, key string) error {
	return f.base.DeleteProjectSecretKey(ctx, project, key)
}
func (f *failingWriter) WriteClusterSecrets(ctx context.Context, cluster string, data map[string][]byte) error {
	if f.failOnLevel == "cluster" {
		return errors.New("forced")
	}
	return f.base.WriteClusterSecrets(ctx, cluster, data)
}
func (f *failingWriter) ReadClusterSecretKeys(ctx context.Context, cluster string) ([]SecretEntry, error) {
	return f.base.ReadClusterSecretKeys(ctx, cluster)
}
func (f *failingWriter) DeleteClusterSecretKey(ctx context.Context, cluster, key string) error {
	return f.base.DeleteClusterSecretKey(ctx, cluster, key)
}
func (f *failingWriter) WriteAppSecrets(ctx context.Context, project, app string, data map[string][]byte) error {
	return f.base.WriteAppSecrets(ctx, project, app, data)
}
func (f *failingWriter) ReadAppSecretKeys(ctx context.Context, project, app string) ([]SecretEntry, error) {
	return f.base.ReadAppSecretKeys(ctx, project, app)
}
func (f *failingWriter) DeleteAppSecretKey(ctx context.Context, project, app, key string) error {
	return f.base.DeleteAppSecretKey(ctx, project, app, key)
}
func (f *failingWriter) WriteAppEnvSecrets(ctx context.Context, project, app, env, namespace string, data map[string][]byte) error {
	return f.base.WriteAppEnvSecrets(ctx, project, app, env, namespace, data)
}
func (f *failingWriter) ReadAppEnvSecretKeys(ctx context.Context, project, app, env string) ([]SecretEntry, error) {
	return f.base.ReadAppEnvSecretKeys(ctx, project, app, env)
}
func (f *failingWriter) DeleteAppEnvSecretKey(ctx context.Context, project, app, env, key string) error {
	return f.base.DeleteAppEnvSecretKey(ctx, project, app, env, key)
}
