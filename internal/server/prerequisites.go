package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ComponentStatus describes whether a cluster component is installed and healthy.
type ComponentStatus struct {
	Installed bool   `json:"installed"`
	Namespace string `json:"namespace,omitempty"`
	Version   string `json:"version,omitempty"`
	Healthy   bool   `json:"healthy"`
}

// InClusterInfo describes the cluster suparship is running on.
type InClusterInfo struct {
	APIServer   string `json:"apiServer"`
	ClusterName string `json:"clusterName,omitempty"`
}

// PrerequisitesResponse is returned by GET /api/v1/prerequisites.
type PrerequisitesResponse struct {
	ArgoCD            ComponentStatus `json:"argocd"`
	IngressController ComponentStatus `json:"ingressController"`
	ESO               ComponentStatus `json:"eso"`
	InCluster         InClusterInfo   `json:"inCluster"`
	DetectedDomain    string          `json:"detectedDomain,omitempty"`
}

// prerequisitesHandler detects cluster prerequisites for the settings UI.
type prerequisitesHandler struct {
	client kubernetes.Interface
}

func (h *prerequisitesHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/prerequisites", h.handlePrerequisites)
}

func (h *prerequisitesHandler) handlePrerequisites(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	resp := PrerequisitesResponse{
		ArgoCD:            h.detectArgoCD(ctx),
		IngressController: h.detectIngress(ctx),
		ESO:               h.detectESO(ctx),
		InCluster: InClusterInfo{
			APIServer: "https://kubernetes.default.svc",
		},
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *prerequisitesHandler) detectArgoCD(ctx context.Context) ComponentStatus {
	status := ComponentStatus{}

	_, err := h.client.CoreV1().Namespaces().Get(ctx, "argocd", metav1.GetOptions{})
	if err != nil {
		return status
	}

	deploy, err := h.client.AppsV1().Deployments("argocd").Get(ctx, "argocd-server", metav1.GetOptions{})
	if err != nil {
		return status
	}

	status.Installed = true
	status.Namespace = "argocd"
	status.Version = extractImageTag(deploy)
	status.Healthy = deploy.Status.ReadyReplicas > 0

	return status
}

func (h *prerequisitesHandler) detectIngress(ctx context.Context) ComponentStatus {
	status := ComponentStatus{}

	classes, err := h.client.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{})
	if err != nil || len(classes.Items) == 0 {
		return status
	}

	status.Installed = true
	status.Healthy = true

	for _, ns := range []string{"ingress-nginx", "nginx-ingress", "traefik", "kube-system"} {
		deploys, err := h.client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if err != nil || len(deploys.Items) == 0 {
			continue
		}
		for _, d := range deploys.Items {
			for _, c := range d.Spec.Template.Spec.Containers {
				if containsAny(c.Image, "ingress-nginx", "traefik", "haproxy-ingress", "contour") {
					status.Namespace = ns
					status.Version = extractImageTag(&d)
					status.Healthy = d.Status.ReadyReplicas > 0
					return status
				}
			}
		}
	}

	return status
}

func (h *prerequisitesHandler) detectESO(ctx context.Context) ComponentStatus {
	status := ComponentStatus{}

	for _, ns := range []string{"external-secrets", "external-secrets-operator"} {
		deploys, err := h.client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if err != nil || len(deploys.Items) == 0 {
			continue
		}
		for _, d := range deploys.Items {
			for _, c := range d.Spec.Template.Spec.Containers {
				if containsAny(c.Image, "external-secrets") {
					status.Installed = true
					status.Namespace = ns
					status.Version = extractImageTag(&d)
					status.Healthy = d.Status.ReadyReplicas > 0
					return status
				}
			}
		}
	}

	return status
}

func extractImageTag(deploy *appsv1.Deployment) string {
	if len(deploy.Spec.Template.Spec.Containers) == 0 {
		return ""
	}
	image := deploy.Spec.Template.Spec.Containers[0].Image
	for i := len(image) - 1; i >= 0; i-- {
		if image[i] == ':' {
			return image[i+1:]
		}
		if image[i] == '/' {
			break
		}
	}
	return ""
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// placeholderPrerequisitesHandler returns static data for fake mode.
type placeholderPrerequisitesHandler struct{}

func (h *placeholderPrerequisitesHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/prerequisites", h.handlePrerequisites)
}

func (h *placeholderPrerequisitesHandler) handlePrerequisites(w http.ResponseWriter, _ *http.Request) {
	resp := PrerequisitesResponse{
		ArgoCD:            ComponentStatus{Installed: true, Namespace: "argocd", Version: "v2.9.3", Healthy: true},
		IngressController: ComponentStatus{Installed: true, Namespace: "ingress-nginx", Version: "v1.9.0", Healthy: true},
		ESO:               ComponentStatus{Installed: true, Namespace: "external-secrets", Version: "v0.9.0", Healthy: true},
		InCluster: InClusterInfo{
			APIServer:   "https://kubernetes.default.svc",
			ClusterName: "fake-cluster",
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

// FormatPrerequisitesSummary returns a human-readable summary for logging.
func FormatPrerequisitesSummary(p PrerequisitesResponse) string {
	icon := func(ok bool) string {
		if ok {
			return "ok"
		}
		return "missing"
	}
	return fmt.Sprintf(
		"argocd=%s ingress=%s eso=%s",
		icon(p.ArgoCD.Installed),
		icon(p.IngressController.Installed),
		icon(p.ESO.Installed),
	)
}
