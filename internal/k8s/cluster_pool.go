package k8s

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// KubeconfigGetter retrieves the raw kubeconfig bytes for a named cluster.
// Implemented by kube.K8sClusterStore.GetKubeconfig.
type KubeconfigGetter interface {
	GetKubeconfig(ctx context.Context, clusterName string) ([]byte, error)
}

// ClusterClientPool maintains a cache of per-cluster Kubernetes clients built
// from kubeconfigs stored in the cluster registry. It is safe for concurrent
// use and builds each client lazily on first access.
//
// Use ClusterClientPool to stream pod logs from workload clusters without
// routing through ArgoCD (which does not proxy log streams). Status and sync
// state should be read from ArgoCD Application CRs on the tooling cluster
// (see ArgoCDStatusReader).
type ClusterClientPool struct {
	mu      sync.RWMutex
	clients map[string]kubernetes.Interface
	getter  KubeconfigGetter
}

// NewClusterClientPool returns a ClusterClientPool backed by getter for
// kubeconfig retrieval.
func NewClusterClientPool(getter KubeconfigGetter) *ClusterClientPool {
	return &ClusterClientPool{
		clients: make(map[string]kubernetes.Interface),
		getter:  getter,
	}
}

// Get returns a Kubernetes client for clusterName, building and caching it
// on first call. Subsequent calls for the same cluster name return the cached
// client without hitting the API server.
func (p *ClusterClientPool) Get(ctx context.Context, clusterName string) (kubernetes.Interface, error) {
	p.mu.RLock()
	if c, ok := p.clients[clusterName]; ok {
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	// Build client — must hold write lock while updating map.
	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock (another goroutine may have built it).
	if c, ok := p.clients[clusterName]; ok {
		return c, nil
	}

	kc, err := p.getter.GetKubeconfig(ctx, clusterName)
	if err != nil {
		return nil, fmt.Errorf("cluster %q: fetching kubeconfig: %w", clusterName, err)
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(kc)
	if err != nil {
		return nil, fmt.Errorf("cluster %q: building REST config: %w", clusterName, err)
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("cluster %q: creating kubernetes client: %w", clusterName, err)
	}

	p.clients[clusterName] = client
	return client, nil
}

// Invalidate removes the cached client for clusterName so the next Get call
// rebuilds it from the current kubeconfig. Call this after a cluster's
// credentials are rotated.
func (p *ClusterClientPool) Invalidate(clusterName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.clients, clusterName)
}
