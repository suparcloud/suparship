package envconfig_test

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/envconfig"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestUpperLevelEnvWriter_WriteAndReadOrg(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := envconfig.NewUpperLevelEnvWriter(client)
	ctx := context.Background()

	cfg := envconfig.EnvConfig{
		Vars: map[string]string{"LOG_LEVEL": "info", "ORG_VAR": "yes"},
	}

	if err := w.WriteOrgEnvConfig(ctx, cfg); err != nil {
		t.Fatalf("WriteOrgEnvConfig: %v", err)
	}

	got, err := w.ReadOrgEnvConfig(ctx)
	if err != nil {
		t.Fatalf("ReadOrgEnvConfig: %v", err)
	}
	if got.Vars["LOG_LEVEL"] != "info" {
		t.Errorf("got LOG_LEVEL=%q, want %q", got.Vars["LOG_LEVEL"], "info")
	}
	if got.Vars["ORG_VAR"] != "yes" {
		t.Errorf("got ORG_VAR=%q, want %q", got.Vars["ORG_VAR"], "yes")
	}

	// Verify Replicator annotation is set to replicate to all namespaces.
	cm, err := client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Get(ctx, envconfig.OrgEnvConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting ConfigMap: %v", err)
	}
	if cm.Annotations[envconfig.ReplicatorAnnotation] != ".*" {
		t.Errorf("expected replicate-to=.*, got %q", cm.Annotations[envconfig.ReplicatorAnnotation])
	}
}

func TestUpperLevelEnvWriter_WriteAndReadEnvType(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := envconfig.NewUpperLevelEnvWriter(client)
	ctx := context.Background()

	cfg := envconfig.EnvConfig{Vars: map[string]string{"DB_POOL": "5"}}
	if err := w.WriteEnvTypeEnvConfig(ctx, "staging", cfg); err != nil {
		t.Fatalf("WriteEnvTypeEnvConfig: %v", err)
	}

	got, err := w.ReadEnvTypeEnvConfig(ctx, "staging")
	if err != nil {
		t.Fatalf("ReadEnvTypeEnvConfig: %v", err)
	}
	if got.Vars["DB_POOL"] != "5" {
		t.Errorf("got DB_POOL=%q, want %q", got.Vars["DB_POOL"], "5")
	}

	// Verify replication pattern targets staging namespaces only.
	cm, err := client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Get(ctx, envconfig.EnvTypeEnvConfigMapName("staging"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting ConfigMap: %v", err)
	}
	if cm.Annotations[envconfig.ReplicatorAnnotation] != ".*-staging" {
		t.Errorf("expected replicate-to=.*-staging, got %q", cm.Annotations[envconfig.ReplicatorAnnotation])
	}
}

func TestUpperLevelEnvWriter_WriteAndReadProject(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := envconfig.NewUpperLevelEnvWriter(client)
	ctx := context.Background()

	cfg := envconfig.EnvConfig{Vars: map[string]string{"SENTRY_PROJECT": "myproject"}}
	if err := w.WriteProjectEnvConfig(ctx, "myproject", cfg); err != nil {
		t.Fatalf("WriteProjectEnvConfig: %v", err)
	}

	got, err := w.ReadProjectEnvConfig(ctx, "myproject")
	if err != nil {
		t.Fatalf("ReadProjectEnvConfig: %v", err)
	}
	if got.Vars["SENTRY_PROJECT"] != "myproject" {
		t.Errorf("got SENTRY_PROJECT=%q, want %q", got.Vars["SENTRY_PROJECT"], "myproject")
	}

	cm, err := client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Get(ctx, envconfig.ProjectEnvConfigMapName("myproject"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting ConfigMap: %v", err)
	}
	wantAnnotation := "suparship.io/project=myproject"
	if cm.Annotations[envconfig.ReplicatorMatchingAnnotation] != wantAnnotation {
		t.Errorf("expected replicate-to-matching=%q, got %q", wantAnnotation, cm.Annotations[envconfig.ReplicatorMatchingAnnotation])
	}
}

// TestUpperLevelEnvWriter_CustomBrandingFlipsReplicatorKey locks the
// lockstep between this writer and gitops.BuildProjectNamespaceManifest:
// when the operator white-labels via Branding.LabelDomain, the
// replicator-matching annotation here MUST use the same domain as the
// labels emitted on app/project namespaces. If this test breaks because
// the annotation prefix diverged from the namespace label prefix,
// replication will silently stop matching across the platform.
func TestUpperLevelEnvWriter_CustomBrandingFlipsReplicatorKey(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := envconfig.NewUpperLevelEnvWriter(client)
	w.Branding = branding.Config{Name: "acme-platform", LabelDomain: "platform.acme.io"}
	ctx := context.Background()

	if err := w.WriteProjectEnvConfig(ctx, "myproject", envconfig.EnvConfig{}); err != nil {
		t.Fatalf("WriteProjectEnvConfig: %v", err)
	}
	cm, err := client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Get(ctx, envconfig.ProjectEnvConfigMapName("myproject"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting ConfigMap: %v", err)
	}
	got := cm.Annotations[envconfig.ReplicatorMatchingAnnotation]
	want := "platform.acme.io/project=myproject"
	if got != want {
		t.Errorf("custom branding: replicate-to-matching = %q, want %q", got, want)
	}

	if err := w.WriteClusterEnvConfig(ctx, "kind-staging", envconfig.EnvConfig{}); err != nil {
		t.Fatalf("WriteClusterEnvConfig: %v", err)
	}
	cm, err = client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Get(ctx, envconfig.ClusterEnvConfigMapName("kind-staging"), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting cluster ConfigMap: %v", err)
	}
	got = cm.Annotations[envconfig.ReplicatorMatchingAnnotation]
	want = "platform.acme.io/cluster=kind-staging"
	if got != want {
		t.Errorf("custom branding: cluster replicate-to-matching = %q, want %q", got, want)
	}
}

func TestUpperLevelEnvWriter_ReadMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := envconfig.NewUpperLevelEnvWriter(client)
	ctx := context.Background()

	got, err := w.ReadOrgEnvConfig(ctx)
	if err != nil {
		t.Fatalf("ReadOrgEnvConfig on missing CM: %v", err)
	}
	if !got.IsEmpty() {
		t.Error("expected empty EnvConfig when ConfigMap does not exist")
	}
}

func TestUpperLevelEnvWriter_UpdatePreservesExtraAnnotations(t *testing.T) {
	client := fake.NewSimpleClientset()
	w := envconfig.NewUpperLevelEnvWriter(client)
	ctx := context.Background()

	// Pre-create ConfigMap with an extra annotation (as Replicator would add).
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envconfig.OrgEnvConfigMapName(),
			Namespace: envconfig.SystemNamespace,
			Annotations: map[string]string{
				"replicator.v1.mittwald.de/replicated-at": "2026-01-01T00:00:00Z",
			},
		},
		Data: map[string]string{"OLD": "value"},
	}
	if _, err := client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Create(ctx, existing, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating pre-existing ConfigMap: %v", err)
	}

	// Update with new data.
	if err := w.WriteOrgEnvConfig(ctx, envconfig.EnvConfig{Vars: map[string]string{"NEW": "data"}}); err != nil {
		t.Fatalf("WriteOrgEnvConfig update: %v", err)
	}

	cm, err := client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Get(ctx, envconfig.OrgEnvConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting updated ConfigMap: %v", err)
	}
	// Our annotation must be set.
	if cm.Annotations[envconfig.ReplicatorAnnotation] != ".*" {
		t.Errorf("replicate-to annotation missing after update")
	}
	// Replicator status annotation must be preserved.
	if cm.Annotations["replicator.v1.mittwald.de/replicated-at"] == "" {
		t.Error("extra annotation was removed during update")
	}
	// Data must be replaced.
	if cm.Data["NEW"] != "data" {
		t.Error("new data not written")
	}
	if cm.Data["OLD"] != "" {
		t.Error("old data not cleared")
	}
}
