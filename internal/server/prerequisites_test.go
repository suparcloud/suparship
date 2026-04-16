package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestPrerequisitesHandler_AllPresent(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "argocd"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "argocd-server", Namespace: "argocd"},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "argocd-server"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "argocd-server"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "argocd-server", Image: "quay.io/argoproj/argocd:v2.9.3"}},
					},
				},
			},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "nginx"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "ingress-nginx-controller", Namespace: "ingress-nginx"},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "ingress-nginx"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "ingress-nginx"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "controller", Image: "registry.k8s.io/ingress-nginx/controller:v1.9.0"}},
					},
				},
			},
			Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "external-secrets", Namespace: "external-secrets"},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "external-secrets"}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "external-secrets"}},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Name: "external-secrets", Image: "ghcr.io/external-secrets/external-secrets:v0.9.0"}},
					},
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

	if !resp.ArgoCD.Installed || !resp.ArgoCD.Healthy {
		t.Errorf("argocd: installed=%v healthy=%v", resp.ArgoCD.Installed, resp.ArgoCD.Healthy)
	}
	if resp.ArgoCD.Version != "v2.9.3" {
		t.Errorf("argocd version: got %q, want v2.9.3", resp.ArgoCD.Version)
	}

	if !resp.IngressController.Installed || !resp.IngressController.Healthy {
		t.Errorf("ingress: installed=%v healthy=%v", resp.IngressController.Installed, resp.IngressController.Healthy)
	}

	if !resp.ESO.Installed || !resp.ESO.Healthy {
		t.Errorf("eso: installed=%v healthy=%v", resp.ESO.Installed, resp.ESO.Healthy)
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

	if resp.ArgoCD.Installed {
		t.Error("argocd should not be installed")
	}
	if resp.IngressController.Installed {
		t.Error("ingress should not be installed")
	}
	if resp.ESO.Installed {
		t.Error("eso should not be installed")
	}
}

func TestExtractImageTag(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"quay.io/argoproj/argocd:v2.9.3", "v2.9.3"},
		{"registry.k8s.io/ingress-nginx/controller:v1.9.0", "v1.9.0"},
		{"ghcr.io/external-secrets/external-secrets:v0.9.0", "v0.9.0"},
		{"myimage", ""},
		{"myimage:latest", "latest"},
		{"localhost:5000/myimage:v1", "v1"},
	}

	for _, tt := range tests {
		deploy := &appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{{Image: tt.image}},
					},
				},
			},
		}
		got := extractImageTag(deploy)
		if got != tt.want {
			t.Errorf("extractImageTag(%q) = %q, want %q", tt.image, got, tt.want)
		}
	}
}

func TestContainsAny(t *testing.T) {
	tests := []struct {
		s      string
		subs   []string
		expect bool
	}{
		{"ghcr.io/external-secrets/external-secrets:v0.9.0", []string{"external-secrets"}, true},
		{"registry.k8s.io/ingress-nginx/controller:v1.9.0", []string{"ingress-nginx", "traefik"}, true},
		{"my-random-image:latest", []string{"ingress-nginx", "traefik"}, false},
	}

	for _, tt := range tests {
		got := containsAny(tt.s, tt.subs...)
		if got != tt.expect {
			t.Errorf("containsAny(%q, %v) = %v, want %v", tt.s, tt.subs, got, tt.expect)
		}
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

	if !resp.ArgoCD.Installed || !resp.ArgoCD.Healthy {
		t.Error("placeholder should report argocd as installed and healthy")
	}
	if !resp.IngressController.Installed || !resp.IngressController.Healthy {
		t.Error("placeholder should report ingress as installed and healthy")
	}
	if !resp.ESO.Installed || !resp.ESO.Healthy {
		t.Error("placeholder should report eso as installed and healthy")
	}
}
