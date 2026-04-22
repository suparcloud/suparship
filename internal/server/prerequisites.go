package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/kube"
)

// PrerequisiteStatus represents the state of a required cluster component.
type PrerequisiteStatus struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Namespace string `json:"namespace,omitempty"`
	Message   string `json:"message,omitempty"`
}

// PrerequisitesResponse is the API response for the prerequisites check.
type PrerequisitesResponse struct {
	Ready         bool                 `json:"ready"`
	Prerequisites []PrerequisiteStatus `json:"prerequisites"`
}

// prerequisitesHandler checks for required cluster components.
type prerequisitesHandler struct {
	client    kubernetes.Interface
	dynClient dynamic.Interface
	logger    *slog.Logger
}

func (h *prerequisitesHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/prerequisites", h.handleGetPrerequisites)
}

func (h *prerequisitesHandler) handleGetPrerequisites(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	results := []PrerequisiteStatus{
		h.checkSealedSecrets(ctx),
		h.checkESO(ctx),
		h.checkArgoCDSystemProject(ctx),
		h.checkRootArgoApp(ctx),
	}

	allReady := true
	for _, p := range results {
		if !p.Installed {
			allReady = false
			break
		}
	}

	writeJSON(w, http.StatusOK, PrerequisitesResponse{
		Ready:         allReady,
		Prerequisites: results,
	})
}

func (h *prerequisitesHandler) checkSealedSecrets(ctx context.Context) PrerequisiteStatus {
	status := PrerequisiteStatus{
		Name:      "sealed-secrets",
		Namespace: "kube-system",
	}

	if h.client == nil {
		status.Message = "cluster client not available"
		return status
	}

	for _, ns := range []string{"kube-system", "sealed-secrets"} {
		deploys, err := h.client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=sealed-secrets",
		})
		if err != nil {
			continue
		}
		if len(deploys.Items) > 0 {
			status.Installed = true
			status.Namespace = ns
			status.Message = fmt.Sprintf("found deployment %s/%s", ns, deploys.Items[0].Name)
			return status
		}
	}

	_, err := h.client.Discovery().ServerResourcesForGroupVersion("bitnami.com/v1alpha1")
	if err == nil {
		status.Installed = true
		status.Message = "CRD bitnami.com/v1alpha1 found"
		return status
	}

	status.Message = "sealed-secrets controller not found"
	return status
}

func (h *prerequisitesHandler) checkESO(ctx context.Context) PrerequisiteStatus {
	status := PrerequisiteStatus{
		Name:      "external-secrets",
		Namespace: "external-secrets",
	}

	if h.client == nil {
		status.Message = "cluster client not available"
		return status
	}

	for _, ns := range []string{"external-secrets", "external-secrets-system"} {
		deploys, err := h.client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=external-secrets",
		})
		if err != nil {
			continue
		}
		if len(deploys.Items) > 0 {
			status.Installed = true
			status.Namespace = ns
			status.Message = fmt.Sprintf("found deployment %s/%s", ns, deploys.Items[0].Name)
			return status
		}
	}

	_, err := h.client.Discovery().ServerResourcesForGroupVersion("external-secrets.io/v1")
	if err == nil {
		status.Installed = true
		status.Message = "CRD external-secrets.io/v1 found"
		return status
	}

	status.Message = "external-secrets operator not found"
	return status
}

func (h *prerequisitesHandler) checkArgoCDSystemProject(ctx context.Context) PrerequisiteStatus {
	status := PrerequisiteStatus{
		Name:      "argocd-system-project",
		Namespace: "argocd",
	}

	if h.dynClient == nil {
		status.Message = "dynamic client not available"
		return status
	}

	exists, err := kube.ArgoCDSystemProjectExists(ctx, h.dynClient, "argocd")
	if err != nil {
		status.Message = fmt.Sprintf("error checking suparship-system AppProject: %v", err)
		return status
	}
	if exists {
		status.Installed = true
		status.Message = "suparship-system AppProject found in argocd namespace"
		return status
	}

	status.Message = "suparship-system AppProject not found — run 'suparship admin bootstrap' or upgrade the Helm release"
	return status
}

func (h *prerequisitesHandler) checkRootArgoApp(ctx context.Context) PrerequisiteStatus {
	status := PrerequisiteStatus{
		Name:      "root-argoapp",
		Namespace: "argocd",
	}

	if h.dynClient == nil {
		status.Message = "dynamic client not available"
		return status
	}

	exists, err := kube.RootArgoAppExists(ctx, h.dynClient, "argocd")
	if err != nil {
		status.Message = fmt.Sprintf("error checking suparship-apps Application: %v", err)
		return status
	}
	if exists {
		status.Installed = true
		status.Message = "suparship-apps root Application found in argocd namespace"
		return status
	}

	status.Message = "suparship-apps Application not found — configure gitops repo to trigger auto-creation"
	return status
}

// placeholderPrerequisitesHandler returns a static response when no cluster client is available.
type placeholderPrerequisitesHandler struct{}

func (h *placeholderPrerequisitesHandler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/prerequisites", h.handleGetPrerequisites)
}

func (h *placeholderPrerequisitesHandler) handleGetPrerequisites(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, PrerequisitesResponse{
		Ready: false,
		Prerequisites: []PrerequisiteStatus{
			{Name: "sealed-secrets", Message: "cluster client not configured"},
			{Name: "external-secrets", Message: "cluster client not configured"},
			{Name: "argocd-system-project", Message: "cluster client not configured"},
			{Name: "root-argoapp", Message: "cluster client not configured"},
		},
	})
}

// ArgoInstallerApp generates an ArgoCD Application YAML for installing a component.
func buildInstallerArgoApp(name, chartRepo, chartName, chartVersion, namespace, argoCDNS string) string {
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: suparship
    suparship.io/component: %s
spec:
  project: suparship-system
  source:
    repoURL: %s
    chart: %s
    targetRevision: %s
    helm:
      releaseName: %s
  destination:
    server: https://kubernetes.default.svc
    namespace: %s
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
`, name, argoCDNS, name, chartRepo, chartName, chartVersion, name, namespace)
}

// SealedSecretsInstallerYAML returns the ArgoCD Application for sealed-secrets.
func SealedSecretsInstallerYAML() string {
	return buildInstallerArgoApp(
		"sealed-secrets",
		"https://bitnami-labs.github.io/sealed-secrets",
		"sealed-secrets",
		"2.16.2",
		"kube-system",
		"argocd",
	)
}

// ESOInstallerYAML returns the ArgoCD Application for External Secrets Operator.
func ESOInstallerYAML() string {
	return buildInstallerArgoApp(
		"external-secrets",
		"https://charts.external-secrets.io",
		"external-secrets",
		"0.10.7",
		"external-secrets",
		"argocd",
	)
}
