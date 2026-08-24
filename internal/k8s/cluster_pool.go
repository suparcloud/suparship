package k8s

import (
	"context"
	"fmt"
	"sync"

	"k8s.io/client-go/dynamic"
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
	mu         sync.RWMutex
	clients    map[string]kubernetes.Interface
	dynClients map[string]dynamic.Interface
	getter     KubeconfigGetter

	// Local fallback (optional): when a cluster has NO stored kubeconfig but
	// isLocal reports it is the cluster suparship itself runs in (its record's
	// API server is the in-cluster URL), serve the local clients instead of
	// failing. This is what makes the dev-loop seed work, where staging/prod
	// are records on the one kind cluster and no kubeconfig Secret exists.
	// Never applied when a kubeconfig IS stored, and never across clusters.
	local    kubernetes.Interface
	localDyn dynamic.Interface
	isLocal  func(ctx context.Context, clusterName string) bool
}

// NewClusterClientPool returns a ClusterClientPool backed by getter for
// kubeconfig retrieval.
func NewClusterClientPool(getter KubeconfigGetter) *ClusterClientPool {
	return &ClusterClientPool{
		clients:    make(map[string]kubernetes.Interface),
		dynClients: make(map[string]dynamic.Interface),
		getter:     getter,
	}
}

// SetLocalFallback registers the pool's own-cluster clients and the predicate
// deciding whether a named cluster IS this cluster (typically: its registered
// API server equals the in-cluster URL). Used only when no kubeconfig is
// stored for the cluster.
func (p *ClusterClientPool) SetLocalFallback(client kubernetes.Interface, dyn dynamic.Interface, isLocal func(ctx context.Context, clusterName string) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.local = client
	p.localDyn = dyn
	p.isLocal = isLocal
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
		if p.local != nil && p.isLocal != nil && p.isLocal(ctx, clusterName) {
			p.clients[clusterName] = p.local
			return p.local, nil
		}
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

// GetDynamic returns a dynamic client for clusterName (cached), used to read CRDs
// the typed clientset doesn't cover (e.g. Gateway-API HTTPRoutes). Built from the
// same kubeconfig as Get.
func (p *ClusterClientPool) GetDynamic(ctx context.Context, clusterName string) (dynamic.Interface, error) {
	p.mu.RLock()
	if c, ok := p.dynClients[clusterName]; ok {
		p.mu.RUnlock()
		return c, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.dynClients[clusterName]; ok {
		return c, nil
	}

	kc, err := p.getter.GetKubeconfig(ctx, clusterName)
	if err != nil {
		if p.localDyn != nil && p.isLocal != nil && p.isLocal(ctx, clusterName) {
			p.dynClients[clusterName] = p.localDyn
			return p.localDyn, nil
		}
		return nil, fmt.Errorf("cluster %q: fetching kubeconfig: %w", clusterName, err)
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kc)
	if err != nil {
		return nil, fmt.Errorf("cluster %q: building REST config: %w", clusterName, err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("cluster %q: creating dynamic client: %w", clusterName, err)
	}
	p.dynClients[clusterName] = dyn
	return dyn, nil
}

// Invalidate removes the cached clients for clusterName so the next Get/GetDynamic
// call rebuilds them from the current kubeconfig. Call this after a cluster's
// credentials are rotated.
func (p *ClusterClientPool) Invalidate(clusterName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.clients, clusterName)
	delete(p.dynClients, clusterName)
}
