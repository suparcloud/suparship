package backup

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func seedCluster() *fake.Clientset {
	return fake.NewClientset(
		// suparship-owned ConfigMap — captured.
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "suparship-org-config", Namespace: SystemNamespace,
				Labels:          map[string]string{"suparship.io/type": "org"},
				ResourceVersion: "123", // server-managed; must not break restore
				Annotations: map[string]string{
					"kubectl.kubernetes.io/last-applied-configuration": "{...}",
					"keep.me": "yes",
				},
			},
			Data: map[string]string{"org.yaml": "name: acme"},
		},
		// suparship-owned Secret — captured.
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "suparship-admin-auth", Namespace: SystemNamespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"username": []byte("admin"), "password-hash": []byte("$2a$abc")},
		},
		// custom-named admin secret (no prefix) — only via extraNames.
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "my-admin", Namespace: SystemNamespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"username": []byte("ops")},
		},
		// unrelated resource — must be ignored.
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "kube-root-ca.crt", Namespace: SystemNamespace},
			Data:       map[string]string{"ca.crt": "x"},
		},
		// SA-token secret — must be skipped even if prefixed.
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "suparship-sa-xyz", Namespace: SystemNamespace},
			Type:       corev1.SecretTypeServiceAccountToken,
			Data:       map[string][]byte{"token": []byte("jwt")},
		},
	)
}

func TestCreate_SelectsSuparshipResources(t *testing.T) {
	ctx := context.Background()
	arch, err := Create(ctx, seedCluster(), SystemNamespace, nil, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	names := map[string]bool{}
	for _, r := range arch.Resources {
		names[r.Name] = true
	}
	if !names["suparship-org-config"] || !names["suparship-admin-auth"] {
		t.Errorf("expected suparship-prefixed resources, got %v", names)
	}
	if names["kube-root-ca.crt"] {
		t.Error("non-suparship configmap should not be captured")
	}
	if names["my-admin"] {
		t.Error("custom-named secret should require extraNames")
	}
	if names["suparship-sa-xyz"] {
		t.Error("service-account-token secret should be skipped")
	}
	// Server-managed annotation stripped, real one kept.
	for _, r := range arch.Resources {
		if r.Name == "suparship-org-config" {
			if _, bad := r.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; bad {
				t.Error("last-applied-configuration annotation should be dropped")
			}
			if r.Annotations["keep.me"] != "yes" {
				t.Error("real annotation should be preserved")
			}
		}
	}
}

func TestCreate_ExtraNamesIncludesRenamedAdmin(t *testing.T) {
	arch, err := Create(context.Background(), seedCluster(), SystemNamespace, []string{"my-admin"}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range arch.Resources {
		if r.Name == "my-admin" {
			found = true
		}
	}
	if !found {
		t.Error("extraNames should include the renamed admin secret")
	}
}

// Round-trip: back up, wipe the cluster, restore, and confirm state returns.
func TestRestore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	arch, err := Create(ctx, seedCluster(), SystemNamespace, nil, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}

	empty := fake.NewClientset()
	res, err := Restore(ctx, empty, arch, SystemNamespace)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(res.Created) == 0 || len(res.Updated) != 0 {
		t.Errorf("expected all-created on empty cluster, got created=%v updated=%v", res.Created, res.Updated)
	}

	cm, err := empty.CoreV1().ConfigMaps(SystemNamespace).Get(ctx, "suparship-org-config", metav1.GetOptions{})
	if err != nil || cm.Data["org.yaml"] != "name: acme" {
		t.Errorf("org config not restored: %v / %+v", err, cm)
	}
	sec, err := empty.CoreV1().Secrets(SystemNamespace).Get(ctx, "suparship-admin-auth", metav1.GetOptions{})
	if err != nil || string(sec.Data["password-hash"]) != "$2a$abc" {
		t.Errorf("admin secret not restored: %v / %+v", err, sec)
	}
}

// Restore onto an existing resource updates it in place (idempotent).
func TestRestore_UpdatesExisting(t *testing.T) {
	ctx := context.Background()
	arch, err := Create(ctx, seedCluster(), SystemNamespace, nil, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	// Restore back onto the same (seeded) cluster → everything already exists.
	res, err := Restore(ctx, seedCluster(), arch, SystemNamespace)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(res.Created) != 0 {
		t.Errorf("expected no creates restoring onto existing state, got %v", res.Created)
	}
}

func TestRestore_RejectsUnknownVersion(t *testing.T) {
	_, err := Restore(context.Background(), fake.NewClientset(), &Archive{APIVersion: "bogus/v9"}, SystemNamespace)
	if err == nil {
		t.Error("expected error on unknown archive apiVersion")
	}
}
