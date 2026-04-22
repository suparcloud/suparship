package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// prereqAppProjectGVR mirrors the GVR used by EnsureArgoCDSystemProject.
var prereqAppProjectGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "appprojects",
}

func newTestDynClient() *dynfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			prereqAppProjectGVR: "AppProjectList",
		},
	)
}

// seedSystemProject creates the suparship-system AppProject in the fake client
// so the prerequisite check reports it as installed.
func seedSystemProject(t *testing.T, dyn *dynfake.FakeDynamicClient) {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("argoproj.io/v1alpha1")
	obj.SetKind("AppProject")
	obj.SetName("suparship-system")
	obj.SetNamespace("argocd")
	if _, err := dyn.Resource(prereqAppProjectGVR).Namespace("argocd").
		Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed system project: %v", err)
	}
}

func TestPrerequisitesHandler_AllPresent(t *testing.T) {
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sealed-secrets-controller",
				Namespace: "kube-system",
				Labels:    map[string]string{"app.kubernetes.io/name": "sealed-secrets"},
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sealed-secrets"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "sealed-secrets"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "sealed-secrets", Image: "bitnami/sealed-secrets-controller:v0.25.0"}}},
				},
			},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "external-secrets",
				Namespace: "external-secrets",
				Labels:    map[string]string{"app.kubernetes.io/name": "external-secrets"},
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "external-secrets"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "external-secrets"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "external-secrets", Image: "ghcr.io/external-secrets/external-secrets:v0.9.0"}}},
				},
			},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
	)

	mux := http.NewServeMux()
	dyn := newTestDynClient()
	seedSystemProject(t, dyn)
	h := &prerequisitesHandler{client: client, dynClient: dyn}
	h.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/prerequisites", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PrerequisitesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if !resp.Ready {
		t.Error("expected Ready=true when all prerequisites are present")
	}
	if len(resp.Prerequisites) != 3 {
		t.Fatalf("expected 3 prerequisites, got %d", len(resp.Prerequisites))
	}

	ss := resp.Prerequisites[0]
	if ss.Name != "sealed-secrets" {
		t.Errorf("first prerequisite name: got %q, want sealed-secrets", ss.Name)
	}
	if !ss.Installed {
		t.Errorf("sealed-secrets should be installed")
	}
	if ss.Namespace != "kube-system" {
		t.Errorf("sealed-secrets namespace: got %q, want kube-system", ss.Namespace)
	}

	eso := resp.Prerequisites[1]
	if eso.Name != "external-secrets" {
		t.Errorf("second prerequisite name: got %q, want external-secrets", eso.Name)
	}
	if !eso.Installed {
		t.Errorf("external-secrets should be installed")
	}
	if eso.Namespace != "external-secrets" {
		t.Errorf("external-secrets namespace: got %q, want external-secrets", eso.Namespace)
	}

	argoCDProj := resp.Prerequisites[2]
	if argoCDProj.Name != "argocd-system-project" {
		t.Errorf("third prerequisite name: got %q, want argocd-system-project", argoCDProj.Name)
	}
	if !argoCDProj.Installed {
		t.Errorf("argocd-system-project should be installed")
	}
}

func TestPrerequisitesHandler_NothingInstalled(t *testing.T) {
	client := fake.NewSimpleClientset()

	mux := http.NewServeMux()
	h := &prerequisitesHandler{client: client}
	h.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/prerequisites", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PrerequisitesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Ready {
		t.Error("expected Ready=false when nothing is installed")
	}
	for _, p := range resp.Prerequisites {
		if p.Installed {
			t.Errorf("%s should not be installed", p.Name)
		}
	}
}

func TestPrerequisitesHandler_PartialInstall(t *testing.T) {
	client := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "external-secrets",
				Namespace: "external-secrets",
				Labels:    map[string]string{"app.kubernetes.io/name": "external-secrets"},
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "external-secrets"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "external-secrets"}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "external-secrets", Image: "ghcr.io/external-secrets/external-secrets:v0.9.0"}}},
				},
			},
		},
	)

	mux := http.NewServeMux()
	h := &prerequisitesHandler{client: client}
	h.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/prerequisites", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var resp PrerequisitesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Ready {
		t.Error("expected Ready=false when only ESO is present")
	}

	ss := resp.Prerequisites[0]
	if ss.Installed {
		t.Error("sealed-secrets should not be installed")
	}

	eso := resp.Prerequisites[1]
	if !eso.Installed {
		t.Error("external-secrets should be installed")
	}
}

func TestPlaceholderPrerequisitesHandler(t *testing.T) {
	mux := http.NewServeMux()
	h := &placeholderPrerequisitesHandler{}
	h.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/prerequisites", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp PrerequisitesResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Ready {
		t.Error("placeholder should report not ready")
	}
	if len(resp.Prerequisites) != 3 {
		t.Fatalf("expected 3 prerequisites, got %d", len(resp.Prerequisites))
	}
	for _, p := range resp.Prerequisites {
		if p.Installed {
			t.Errorf("placeholder %s should not be installed", p.Name)
		}
	}
}

func TestInstallerYAML(t *testing.T) {
	t.Run("SealedSecrets", func(t *testing.T) {
		yaml := SealedSecretsInstallerYAML()
		if yaml == "" {
			t.Fatal("empty YAML")
		}
		for _, want := range []string{"sealed-secrets", "bitnami-labs.github.io", "2.16.2"} {
			if !strings.Contains(yaml, want) {
				t.Errorf("YAML missing %q", want)
			}
		}
	})

	t.Run("ESO", func(t *testing.T) {
		yaml := ESOInstallerYAML()
		if yaml == "" {
			t.Fatal("empty YAML")
		}
		for _, want := range []string{"external-secrets", "charts.external-secrets.io", "0.10.7"} {
			if !strings.Contains(yaml, want) {
				t.Errorf("YAML missing %q", want)
			}
		}
	})
}
