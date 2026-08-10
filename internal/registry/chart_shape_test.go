package registry

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The Helm chart writes registry.yaml as raw YAML
// (charts/suparship/templates/configmap-registry.yaml) and Store.Get decodes
// it non-strictly — a key that doesn't match its struct tag is dropped WITHOUT
// error. For `insecure` that failure is nasty: the flag silently reads false,
// the publisher renders Warehouses with TLS verification on, and Kargo never
// resolves a tag from a plain-HTTP registry. Pin the chart's literal output
// against the real decoder. Keep in sync with configmap-registry.yaml.
func TestStoreGet_ChartShapeDecodes(t *testing.T) {
	chartYAML := `enabled: true
url: "kind-registry:5000"
insecure: true
username: "gitops"
authSecretRef: "reg-cred"
environments:
  - "staging"
`
	client := fake.NewClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: namespace},
		Data:       map[string]string{configMapKey: chartYAML},
	})

	cfg, err := NewStore(client).Get(context.Background())
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !cfg.Enabled || cfg.URL != "kind-registry:5000" {
		t.Errorf("base fields: %+v", cfg)
	}
	if !cfg.Insecure {
		t.Error("insecure did not decode — the chart key no longer matches the struct tag")
	}
	if cfg.Username != "gitops" || cfg.AuthSecretRef != "reg-cred" {
		t.Errorf("credential refs: %+v", cfg)
	}
	if len(cfg.Environments) != 1 || cfg.Environments[0] != "staging" {
		t.Errorf("environments: %v", cfg.Environments)
	}
}
