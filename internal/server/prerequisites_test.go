package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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

	if !resp.Ready {
		t.Error("expected Ready=true when all prerequisites are present")
	}
	if len(resp.Prerequisites) != 2 {
		t.Fatalf("expected 2 prerequisites, got %d", len(resp.Prerequisites))
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
	if len(resp.Prerequisites) != 2 {
		t.Fatalf("expected 2 prerequisites, got %d", len(resp.Prerequisites))
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
