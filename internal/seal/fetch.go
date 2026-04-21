package seal

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
)

// DefaultControllerNamespace and DefaultControllerName mirror the upstream
// sealed-secrets defaults. Both are configurable via FetchOptions when a
// target cluster uses non-default values.
const (
	DefaultControllerNamespace = "kube-system"
	DefaultControllerName      = "sealed-secrets-controller"
	defaultControllerPort      = "http"
)

// ErrControllerNotFound is returned when the sealed-secrets controller
// Service does not exist in the target cluster.
var ErrControllerNotFound = errors.New("seal: sealed-secrets controller Service not found in target cluster")

// FetchOptions tunes the cert-fetch call. Zero values use upstream defaults.
type FetchOptions struct {
	// ControllerNamespace defaults to "kube-system".
	ControllerNamespace string
	// ControllerName defaults to "sealed-secrets-controller".
	ControllerName string
	// PortName defaults to "http". Set to e.g. "https" for installs that
	// only expose the HTTPS port.
	PortName string
}

func (o FetchOptions) namespace() string {
	if o.ControllerNamespace == "" {
		return DefaultControllerNamespace
	}
	return o.ControllerNamespace
}

func (o FetchOptions) name() string {
	if o.ControllerName == "" {
		return DefaultControllerName
	}
	return o.ControllerName
}

func (o FetchOptions) portName() string {
	if o.PortName == "" {
		return defaultControllerPort
	}
	return o.PortName
}

// FetchCert pulls the active sealed-secrets controller public certificate
// from the given target-cluster client by hitting `/v1/cert.pem` through the
// API server's Service proxy. This is the same path `kubeseal --fetch-cert`
// uses, so anything kubeseal can reach is reachable here without shelling
// out to the binary or storing extra credentials in suparship.
//
// Returns the raw PEM bytes; callers should store the result in a CertCache
// so future seals don't need a network hop.
func FetchCert(ctx context.Context, target kubernetes.Interface, opts FetchOptions) ([]byte, error) {
	if target == nil {
		return nil, errors.New("seal: target cluster client is required")
	}
	ns, svc, port := opts.namespace(), opts.name(), opts.portName()

	// /api/v1/namespaces/<ns>/services/<svc>:<port>/proxy/v1/cert.pem
	// Mirrors the request kubeseal makes — the API server proxies the
	// call through to the controller pod, no direct network access needed.
	path := fmt.Sprintf("/api/v1/namespaces/%s/services/%s:%s/proxy/v1/cert.pem", ns, svc, port)
	pem, err := target.CoreV1().RESTClient().Get().AbsPath(path).DoRaw(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s (looked up port %q)", ErrControllerNotFound, ns, svc, port)
		}
		return nil, fmt.Errorf("seal: fetching cert from %s/%s via API proxy: %w", ns, svc, err)
	}
	if _, parseErr := LoadCertFromPEM(pem); parseErr != nil {
		return nil, fmt.Errorf("seal: controller returned invalid cert PEM: %w", parseErr)
	}
	return pem, nil
}

// FetchAndCache fetches the cert from target and persists it in cache for
// future seal operations. Returns the PEM bytes for the caller to use
// immediately.
func FetchAndCache(ctx context.Context, cache CertCache, target kubernetes.Interface, cluster string, opts FetchOptions) ([]byte, error) {
	pem, err := FetchCert(ctx, target, opts)
	if err != nil {
		return nil, err
	}
	if err := cache.Put(ctx, cluster, pem); err != nil {
		return nil, fmt.Errorf("seal: caching fetched cert for %q: %w", cluster, err)
	}
	return pem, nil
}
