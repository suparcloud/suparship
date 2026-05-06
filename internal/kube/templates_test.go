package kube_test

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
)

// validTemplateYAML is a minimal, valid template definition used across tests.
const validTemplateYAML = `apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: web-service
  version: "1.0.0"
spec:
  title: Web Service
  description: Deploy a containerised HTTP service.
  category: web
  engine:
    type: helm
    chart: ./chart
`

// templateConfigMap constructs a well-formed template ConfigMap for test setup.
func templateConfigMap(name, templateYAML string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suparship-template-" + name,
			Namespace: "suparship-system",
			Labels:    map[string]string{"suparship.io/type": "template"},
		},
		Data: map[string]string{"template.yaml": templateYAML},
	}
}

func mustCreateCM(t *testing.T, client *k8sfake.Clientset, cm *corev1.ConfigMap) {
	t.Helper()
	_, err := client.CoreV1().ConfigMaps("suparship-system").Create(
		context.Background(), cm, metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("setup: creating configmap %s: %v", cm.Name, err)
	}
}

func TestLoadTemplates_EmptyCluster(t *testing.T) {
	client := k8sfake.NewClientset()
	templates, err := kube.LoadTemplates(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(templates) != 0 {
		t.Errorf("expected empty slice, got %d templates", len(templates))
	}
}

func TestLoadTemplates_SingleValid(t *testing.T) {
	client := k8sfake.NewClientset()
	mustCreateCM(t, client, templateConfigMap("web-service", validTemplateYAML))

	templates, err := kube.LoadTemplates(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
	if got := templates[0].Metadata.Name; got != "web-service" {
		t.Errorf("name: want %q, got %q", "web-service", got)
	}
	if got := templates[0].Spec.Title; got != "Web Service" {
		t.Errorf("title: want %q, got %q", "Web Service", got)
	}
}

func TestLoadTemplates_MissingPayloadKey_Skipped(t *testing.T) {
	client := k8sfake.NewClientset()
	// ConfigMap has the right label but is missing the template.yaml key.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suparship-template-incomplete",
			Namespace: "suparship-system",
			Labels:    map[string]string{"suparship.io/type": "template"},
		},
		Data: map[string]string{"other-key": "irrelevant"},
	}
	mustCreateCM(t, client, cm)

	templates, err := kube.LoadTemplates(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(templates) != 0 {
		t.Errorf("expected 0 templates (missing key skipped), got %d", len(templates))
	}
}

func TestLoadTemplates_InvalidYAML_ReturnsError(t *testing.T) {
	client := k8sfake.NewClientset()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "suparship-template-bad",
			Namespace: "suparship-system",
			Labels:    map[string]string{"suparship.io/type": "template"},
		},
		Data: map[string]string{"template.yaml": ":::not valid yaml:::"},
	}
	mustCreateCM(t, client, cm)

	_, err := kube.LoadTemplates(context.Background(), client)
	if err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestLoadTemplates_SortedByName(t *testing.T) {
	client := k8sfake.NewClientset()

	workerYAML := `apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: worker
  version: "1.0.0"
spec:
  title: Worker
  category: background
  engine:
    type: helm
    chart: ./chart
`
	cronYAML := `apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: cron-job
  version: "1.0.0"
spec:
  title: Cron Job
  category: background
  engine:
    type: helm
    chart: ./chart
`
	mustCreateCM(t, client, templateConfigMap("web-service", validTemplateYAML))
	mustCreateCM(t, client, templateConfigMap("worker", workerYAML))
	mustCreateCM(t, client, templateConfigMap("cron-job", cronYAML))

	templates, err := kube.LoadTemplates(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(templates) != 3 {
		t.Fatalf("expected 3 templates, got %d", len(templates))
	}

	want := []string{"cron-job", "web-service", "worker"}
	for i, w := range want {
		if got := templates[i].Metadata.Name; got != w {
			t.Errorf("position %d: want %q, got %q", i, w, got)
		}
	}
}

func TestLoadTemplates_UnlabelledConfigMap_Ignored(t *testing.T) {
	client := k8sfake.NewClientset()
	// This ConfigMap has no suparship label; it should be invisible.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "some-other-configmap",
			Namespace: "suparship-system",
		},
		Data: map[string]string{"template.yaml": validTemplateYAML},
	}
	mustCreateCM(t, client, cm)

	templates, err := kube.LoadTemplates(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(templates) != 0 {
		t.Errorf("expected 0 templates (unlabelled CM ignored), got %d", len(templates))
	}
}

// --- Versioned naming + dedupe tests (PR1.4) ---

func parseTemplateOrFatal(t *testing.T, src string) *tpl.Template {
	t.Helper()
	tmpl, err := tpl.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	return tmpl
}

func TestSaveTemplate_WritesAliasAndArchive(t *testing.T) {
	client := k8sfake.NewSimpleClientset()
	tmpl := parseTemplateOrFatal(t, validTemplateYAML)

	if err := kube.SaveTemplate(context.Background(), client, tmpl, []byte("chart-bytes")); err != nil {
		t.Fatalf("SaveTemplate: %v", err)
	}

	// Both ConfigMaps should exist.
	cms, err := client.CoreV1().ConfigMaps("suparship-system").List(
		context.Background(), metav1.ListOptions{LabelSelector: "suparship.io/template-name=web-service"},
	)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cms.Items) != 2 {
		t.Fatalf("expected 2 ConfigMaps (alias + archive), got %d: %+v", len(cms.Items), cmNames(cms.Items))
	}

	var sawAlias, sawArchive bool
	for _, cm := range cms.Items {
		switch cm.Labels["suparship.io/template-role"] {
		case "current":
			sawAlias = true
			if cm.Name != "suparship-template-web-service" {
				t.Errorf("alias name = %q, want suparship-template-web-service", cm.Name)
			}
		case "archive":
			sawArchive = true
			if cm.Name != "suparship-template-web-service-1.0.0" {
				t.Errorf("archive name = %q, want suparship-template-web-service-1.0.0", cm.Name)
			}
			if cm.Labels["suparship.io/template-version"] != "1.0.0" {
				t.Errorf("archive version label = %q, want 1.0.0", cm.Labels["suparship.io/template-version"])
			}
		}
	}
	if !sawAlias || !sawArchive {
		t.Errorf("missing alias=%v or archive=%v", sawAlias, sawArchive)
	}
}

func TestLoadTemplates_SkipsArchives(t *testing.T) {
	client := k8sfake.NewSimpleClientset()
	tmpl := parseTemplateOrFatal(t, validTemplateYAML)
	if err := kube.SaveTemplate(context.Background(), client, tmpl, []byte("chart-bytes")); err != nil {
		t.Fatalf("SaveTemplate: %v", err)
	}

	// Both alias and archive exist; LoadTemplates should surface only one.
	templates, err := kube.LoadTemplates(context.Background(), client)
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("expected 1 template (archive filtered), got %d", len(templates))
	}
	if templates[0].Metadata.Name != "web-service" {
		t.Errorf("name = %q, want web-service", templates[0].Metadata.Name)
	}
}

func TestLoadTemplates_LegacyUnlabeledAliasStillListed(t *testing.T) {
	// Pre-versioning ConfigMaps lack the role label entirely. They must
	// continue to surface in the gallery (treated as "current").
	client := k8sfake.NewSimpleClientset()
	mustCreateCM(t, client, templateConfigMap("legacy", validTemplateYAML))

	templates, err := kube.LoadTemplates(context.Background(), client)
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("expected 1 template, got %d", len(templates))
	}
}

func TestLoadChartBundleVersion_PrefersArchive(t *testing.T) {
	client := k8sfake.NewSimpleClientset()
	tmpl := parseTemplateOrFatal(t, validTemplateYAML)
	if err := kube.SaveTemplate(context.Background(), client, tmpl, []byte("v1-bytes")); err != nil {
		t.Fatalf("SaveTemplate: %v", err)
	}

	bytes, err := kube.LoadChartBundleVersion(context.Background(), client, "web-service", "1.0.0")
	if err != nil {
		t.Fatalf("LoadChartBundleVersion: %v", err)
	}
	if string(bytes) != "v1-bytes" {
		t.Errorf("bytes = %q, want v1-bytes", bytes)
	}
}

func TestLoadChartBundleVersion_FallsBackToAlias(t *testing.T) {
	// Legacy alias-only template: archive doesn't exist, version-blind
	// fallback should still find the bytes.
	client := k8sfake.NewSimpleClientset()
	cm := templateConfigMap("web-service", validTemplateYAML)
	cm.BinaryData = map[string][]byte{"chart.tgz": []byte("legacy-bytes")}
	mustCreateCM(t, client, cm)

	bytes, err := kube.LoadChartBundleVersion(context.Background(), client, "web-service", "9.9.9")
	if err != nil {
		t.Fatalf("LoadChartBundleVersion: %v", err)
	}
	if string(bytes) != "legacy-bytes" {
		t.Errorf("bytes = %q, want legacy-bytes (alias fallback)", bytes)
	}
}

func TestDeleteTemplate_RemovesAliasAndArchives(t *testing.T) {
	client := k8sfake.NewSimpleClientset()
	tmpl := parseTemplateOrFatal(t, validTemplateYAML)
	if err := kube.SaveTemplate(context.Background(), client, tmpl, []byte("v1-bytes")); err != nil {
		t.Fatalf("SaveTemplate: %v", err)
	}

	deleted, err := kube.DeleteTemplate(context.Background(), client, "web-service")
	if err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if !deleted {
		t.Error("DeleteTemplate returned (false, nil); want (true, nil)")
	}
	cms, err := client.CoreV1().ConfigMaps("suparship-system").List(
		context.Background(), metav1.ListOptions{LabelSelector: "suparship.io/template-name=web-service"},
	)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(cms.Items) != 0 {
		t.Errorf("expected 0 ConfigMaps after delete, got %d: %+v", len(cms.Items), cmNames(cms.Items))
	}
}

func TestTemplateConfigMapNameVersioned_SanitisesVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1.2.0", "suparship-template-foo-1.2.0"},
		{"1.2.0-rc.1", "suparship-template-foo-1.2.0-rc.1"},
		{"1.2.0+build.5", "suparship-template-foo-1.2.0-build.5"},
		{"V1.0.0", "suparship-template-foo-v1.0.0"},
		{"", "suparship-template-foo"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := kube.TemplateConfigMapNameVersioned("foo", tc.in)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func cmNames(items []corev1.ConfigMap) []string {
	out := make([]string, 0, len(items))
	for _, cm := range items {
		out = append(out, cm.Name)
	}
	return out
}
