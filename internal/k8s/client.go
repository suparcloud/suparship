// Package k8s provides helpers for building Kubernetes clients.
package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
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

// KargoProjectNamespaceLabel marks a namespace as a Kargo Project namespace.
// Kargo's Project admission webhook refuses to initialize a Project over a
// pre-existing namespace that lacks this label (so it never hijacks an arbitrary
// namespace), so suparship stamps it whenever it ensures the shared Kargo
// namespace exists.
const KargoProjectNamespaceLabel = "kargo.akuity.io/project"

// EnsureKargoProjectNamespace ensures ns exists AND carries the
// kargo.akuity.io/project label. This is required for the shared Kargo namespace:
// credential provisioning may create the namespace before ArgoCD syncs the Kargo
// Project CR, and Kargo will reject the Project ("namespace already exists and is
// not labeled as a Project namespace") unless the namespace already carries the
// label. The label is applied both on create and, idempotently, by patching a
// pre-existing namespace that lacks it.
func EnsureKargoProjectNamespace(ctx context.Context, client kubernetes.Interface, ns string) error {
	existing, err := client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, cErr := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   ns,
				Labels: map[string]string{KargoProjectNamespaceLabel: "true"},
			},
		}, metav1.CreateOptions{})
		if cErr == nil {
			return nil
		}
		if !apierrors.IsAlreadyExists(cErr) {
			return fmt.Errorf("creating kargo project namespace %q: %w", ns, cErr)
		}
		// Raced with another creator — re-fetch and ensure the label below.
		existing, err = client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	}
	if err != nil {
		return fmt.Errorf("checking kargo project namespace %q: %w", ns, err)
	}
	if existing.Labels[KargoProjectNamespaceLabel] == "true" {
		return nil
	}
	patch := []byte(`{"metadata":{"labels":{"` + KargoProjectNamespaceLabel + `":"true"}}}`)
	if _, err := client.CoreV1().Namespaces().Patch(ctx, ns, apitypes.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("labeling kargo project namespace %q: %w", ns, err)
	}
	return nil
}

// EnsureNamespaceOwned creates the namespace with the given labels when it does
// not exist (returning created=true), or adopts a pre-existing namespace
// without modifying it (returning created=false). The labels — which encode
// suparship ownership — are therefore applied ONLY to namespaces suparship
// creates, so an adopted namespace can never be mistaken for an owned one and
// won't be deleted at project teardown. A namespace racing into existence
// (IsAlreadyExists) is treated as adopted.
func EnsureNamespaceOwned(ctx context.Context, client kubernetes.Interface, ns string, labels map[string]string) (created bool, err error) {
	if _, err := client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err == nil {
		return false, nil // already exists → adopt, don't mutate
	} else if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("checking namespace %q: %w", ns, err)
	}
	_, err = client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns, Labels: labels},
	}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return false, nil // raced with another creator → adopt
	}
	if err != nil {
		return false, fmt.Errorf("creating namespace %q: %w", ns, err)
	}
	return true, nil
}

// DeleteNamespaceIfOwned deletes a single namespace by name, but only when it
// carries all of ownerLabels (i.e. suparship created it). Returns true if it
// was deleted. A missing namespace, or one lacking the ownership labels (an
// adopted/external namespace that happens to be the app's), is left untouched
// and returns false. Used on app rename to reclaim the old app's namespaces
// without ever removing one suparship didn't create.
func DeleteNamespaceIfOwned(ctx context.Context, client kubernetes.Interface, name string, ownerLabels map[string]string) (bool, error) {
	ns, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading namespace %q: %w", name, err)
	}
	for k, v := range ownerLabels {
		if ns.Labels[k] != v {
			return false, nil // not suparship-owned — leave it
		}
	}
	if err := client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("deleting namespace %q: %w", name, err)
	}
	return true, nil
}

// DeleteOwnedNamespaces deletes every namespace matching labelSelector and
// returns the names it deleted. Only suparship-stamped (owned) namespaces carry
// the ownership labels, so a selector targeting them never touches adopted or
// external namespaces. NotFound on an individual delete is ignored (idempotent).
func DeleteOwnedNamespaces(ctx context.Context, client kubernetes.Interface, labelSelector string) ([]string, error) {
	list, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, fmt.Errorf("listing namespaces %q: %w", labelSelector, err)
	}
	var deleted []string
	for i := range list.Items {
		name := list.Items[i].Name
		if err := client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return deleted, fmt.Errorf("deleting namespace %q: %w", name, err)
		}
		deleted = append(deleted, name)
	}
	return deleted, nil
}

// UpsertSecretData creates or updates a K8s Secret in the given namespace,
// merging the provided data into existing keys.
func UpsertSecretData(ctx context.Context, client kubernetes.Interface, ns, name string, data map[string][]byte) error {
	existing, err := client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "suparship",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: data,
		}
		_, err = client.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating secret %s/%s: %w", ns, name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading secret %s/%s: %w", ns, name, err)
	}

	if existing.Data == nil {
		existing.Data = make(map[string][]byte)
	}
	for k, v := range data {
		existing.Data[k] = v
	}
	_, err = client.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating secret %s/%s: %w", ns, name, err)
	}
	return nil
}

// GetSecretData reads a single key from a Kubernetes Secret.
// Returns nil if the secret or key does not exist.
func GetSecretData(ctx context.Context, client kubernetes.Interface, ns, name, key string) ([]byte, error) {
	secret, err := client.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading secret %s/%s: %w", ns, name, err)
	}
	return secret.Data[key], nil
}
