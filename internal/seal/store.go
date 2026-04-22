package seal

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ErrCertNotCached is returned when no sealing certificate has been
// uploaded for the requested cluster.
var ErrCertNotCached = errors.New("seal: no sealing certificate cached for cluster")

// CertCache stores the sealed-secrets controller public certificate for
// each registered target cluster. The cert is uploaded once by the org
// admin (via UI/CLI) and reused on every token-seal operation.
type CertCache interface {
	// Get returns the cached cert PEM for the given cluster name.
	// Returns ErrCertNotCached when nothing is stored.
	Get(ctx context.Context, cluster string) ([]byte, error)
	// Put stores the cert PEM for the given cluster name, overwriting any
	// previous value.
	Put(ctx context.Context, cluster string, pem []byte) error
	// List returns the names of all clusters with a cached cert.
	List(ctx context.Context) ([]string, error)
}

// MemCertCache is an in-memory CertCache for tests and the fake profile.
type MemCertCache struct {
	mu    sync.RWMutex
	certs map[string][]byte
}

func NewMemCertCache() *MemCertCache {
	return &MemCertCache{certs: make(map[string][]byte)}
}

func (m *MemCertCache) Get(_ context.Context, cluster string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pem, ok := m.certs[cluster]
	if !ok {
		return nil, ErrCertNotCached
	}
	return pem, nil
}

func (m *MemCertCache) Put(_ context.Context, cluster string, pem []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.certs[cluster] = pem
	return nil
}

func (m *MemCertCache) List(_ context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.certs))
	for k := range m.certs {
		out = append(out, k)
	}
	return out, nil
}

// ── Kubernetes-backed implementation ───────────────────────────────────────

const (
	// CertConfigMapName is the ConfigMap in suparship-system that stores
	// per-cluster sealing certs (key = cluster name, value = PEM).
	CertConfigMapName = "suparship-sealing-certs"
	certConfigMapNS   = "suparship-system"
)

// K8sCertCache stores per-cluster certs in a single ConfigMap.
type K8sCertCache struct {
	client kubernetes.Interface
}

func NewK8sCertCache(client kubernetes.Interface) *K8sCertCache {
	return &K8sCertCache{client: client}
}

func (k *K8sCertCache) Get(ctx context.Context, cluster string) ([]byte, error) {
	cm, err := k.client.CoreV1().ConfigMaps(certConfigMapNS).Get(ctx, CertConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, ErrCertNotCached
	}
	if err != nil {
		return nil, fmt.Errorf("seal: reading cert ConfigMap: %w", err)
	}
	pem, ok := cm.Data[cluster]
	if !ok {
		return nil, ErrCertNotCached
	}
	return []byte(pem), nil
}

func (k *K8sCertCache) Put(ctx context.Context, cluster string, pem []byte) error {
	if cluster == "" {
		return errors.New("seal: cluster name is required")
	}
	if _, err := LoadCertFromPEM(pem); err != nil {
		return fmt.Errorf("seal: invalid cert PEM: %w", err)
	}

	cm, err := k.client.CoreV1().ConfigMaps(certConfigMapNS).Get(ctx, CertConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = k.client.CoreV1().ConfigMaps(certConfigMapNS).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      CertConfigMapName,
				Namespace: certConfigMapNS,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "suparship",
				},
			},
			Data: map[string]string{cluster: string(pem)},
		}, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("seal: creating cert ConfigMap: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("seal: reading cert ConfigMap: %w", err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[cluster] = string(pem)
	_, err = k.client.CoreV1().ConfigMaps(certConfigMapNS).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("seal: updating cert ConfigMap: %w", err)
	}
	return nil
}

func (k *K8sCertCache) List(ctx context.Context) ([]string, error) {
	cm, err := k.client.CoreV1().ConfigMaps(certConfigMapNS).Get(ctx, CertConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("seal: reading cert ConfigMap: %w", err)
	}
	out := make([]string, 0, len(cm.Data))
	for k := range cm.Data {
		out = append(out, k)
	}
	return out, nil
}

// LoadCachedCert is a convenience helper that fetches and parses the cert
// for the given cluster in one call.
func LoadCachedCert(ctx context.Context, cache CertCache, cluster string) (*rsa.PublicKey, error) {
	pemBytes, err := cache.Get(ctx, cluster)
	if err != nil {
		return nil, err
	}
	return LoadCertFromPEM(pemBytes)
}
