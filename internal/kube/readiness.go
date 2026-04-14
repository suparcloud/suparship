package kube

import (
	"context"
	"fmt"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// NewArgoCDReadinessProbe returns a readiness check function that verifies the
// ArgoCD API is reachable by listing Application CRs in the ArgoCD namespace.
//
// The probe is designed to complete quickly — it lists with a limit of 1 and
// relies on the context deadline (set to 3 s by the readyz handler) for
// timeout enforcement.
//
// Typical usage:
//
//	server.Config{
//	    ReadinessProbers: []server.ReadinessProber{
//	        {Name: "argocd", Check: kube.NewArgoCDReadinessProbe(dynClient, "argocd")},
//	    },
//	}
func NewArgoCDReadinessProbe(dyn dynamic.Interface, argoCDNamespace string) func(ctx context.Context) error {
	if argoCDNamespace == "" {
		argoCDNamespace = argoCDNS
	}
	return func(ctx context.Context) error {
		_, err := dyn.Resource(argoCDAppGVR).Namespace(argoCDNamespace).List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil {
			return fmt.Errorf("argocd API unreachable: %w", err)
		}
		return nil
	}
}

// NewKubernetesReadinessProbe returns a readiness check function that verifies
// the Kubernetes API server is reachable by listing namespaces. This is a
// coarse liveness check; it does not verify any specific suparShip resource.
func NewKubernetesReadinessProbe(client kubernetes.Interface) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		_, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
		if err != nil {
			return fmt.Errorf("kubernetes API unreachable: %w", err)
		}
		return nil
	}
}

// NewGitOpsRepoReadinessProbe returns a readiness check function that verifies
// the GitOps repository is reachable via an HTTP HEAD request to repoURL.
//
// It is intentionally lightweight: it does not clone or authenticate — it only
// checks that the host accepts TCP connections and returns an HTTP response.
// Authentication failures (401/403) are treated as reachable (the host is up).
//
// repoURL must be an HTTP/HTTPS URL, e.g.
// "http://gitea-http.gitea.svc.cluster.local:3000/gitops/gitops.git"
func NewGitOpsRepoReadinessProbe(repoURL string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, repoURL, nil)
		if err != nil {
			return fmt.Errorf("build gitops repo request: %w", err)
		}

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("gitops repo unreachable at %s: %w", repoURL, err)
		}
		defer resp.Body.Close()

		// 401/403 mean the host is up but auth is required — that is fine for
		// a connectivity probe. We only fail on network-level errors (handled
		// above) or unexpected server errors.
		if resp.StatusCode >= 500 {
			return fmt.Errorf("gitops repo returned server error %d at %s", resp.StatusCode, repoURL)
		}
		return nil
	}
}
