// Package k8s provides helpers for building Kubernetes clients.
package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NewClientset builds a Kubernetes clientset using the following resolution
// order:
//
//  1. Explicit kubeconfig path (--kubeconfig flag / KUBECONFIG env var).
//  2. Default kubeconfig discovery: KUBECONFIG env var, then ~/.kube/config.
//  3. In-cluster config — used automatically when the binary runs inside a
//     Kubernetes Pod and no kubeconfig is provided or found.
//
// An error is returned only when all three paths fail, and the message
// describes what was tried so the caller can produce an actionable log.
func NewClientset(kubeconfig, kubecontext string) (kubernetes.Interface, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}

	overrides := &clientcmd.ConfigOverrides{}
	if kubecontext != "" {
		overrides.CurrentContext = kubecontext
	}

	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, overrides,
	).ClientConfig()
	if err != nil {
		// No kubeconfig found via the standard paths. If the caller did not
		// specify an explicit file or context, try in-cluster config — this
		// is the expected path when the server runs inside a Pod.
		if kubeconfig == "" && kubecontext == "" {
			inClusterCfg, inErr := rest.InClusterConfig()
			if inErr == nil {
				restConfig = inClusterCfg
				err = nil
			} else {
				return nil, fmt.Errorf(
					"no kubeconfig found (%v) and in-cluster config unavailable (%v)",
					err, inErr,
				)
			}
		} else {
			return nil, fmt.Errorf("building kubeconfig: %w", err)
		}
	}

	cs, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	return cs, nil
}

// NewRestConfig builds a *rest.Config from the given kubeconfig path and
// context using the same resolution order as NewClientset. Use this when
// you need to construct additional client types (e.g. dynamic.Interface)
// from the same configuration.
func NewRestConfig(kubeconfig, kubecontext string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}

	overrides := &clientcmd.ConfigOverrides{}
	if kubecontext != "" {
		overrides.CurrentContext = kubecontext
	}

	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		rules, overrides,
	).ClientConfig()
	if err != nil {
		if kubeconfig == "" && kubecontext == "" {
			inClusterCfg, inErr := rest.InClusterConfig()
			if inErr == nil {
				return inClusterCfg, nil
			}
			return nil, fmt.Errorf(
				"no kubeconfig found (%v) and in-cluster config unavailable (%v)",
				err, inErr,
			)
		}
		return nil, fmt.Errorf("building rest config: %w", err)
	}
	return cfg, nil
}

// NewDynamicClient builds a dynamic.Interface from the given kubeconfig path
// and context. The dynamic client is required for interacting with CRDs such
// as ArgoCD Application and Kargo Stage/Promotion.
func NewDynamicClient(kubeconfig, kubecontext string) (dynamic.Interface, error) {
	cfg, err := NewRestConfig(kubeconfig, kubecontext)
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}
	return dyn, nil
}

// EnsureNamespace creates the namespace if it does not already exist.
func EnsureNamespace(ctx context.Context, client kubernetes.Interface, ns string) error {
	_, err := client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking namespace %q: %w", ns, err)
	}

	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %q: %w", ns, err)
	}

	return nil
}
