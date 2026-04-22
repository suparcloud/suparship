package kube

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/suparcloud/suparship/internal/domain"
)

const (
	suparshipSystemNS = "suparship-system"
	argoCDNS          = "argocd"

	clusterConfigMapPrefix    = "suparship-cluster-"
	clusterKubeconfigPrefix   = "suparship-cluster-kubeconfig-"
	clusterConfigMapKey       = "cluster.json"
	clusterKubeconfigKey      = "kubeconfig"
	argoCDClusterSecretPrefix = "cluster-"

	labelManagedBy  = "suparship.io/managed-by"
	labelType       = "suparship.io/type"
	labelCluster    = "suparship.io/cluster"
	argoCDSecretType = "argocd.argoproj.io/secret-type"
	argoCDCluster   = "cluster"
)

// K8sClusterStore implements domain.ClusterStore backed by Kubernetes
// ConfigMaps and Secrets in the suparship-system namespace.
//
// Per registered cluster:
//   - ConfigMap "suparship-cluster-{name}" in suparship-system — cluster metadata
//   - Secret "suparship-cluster-kubeconfig-{name}" in suparship-system — raw kubeconfig
//   - Secret "cluster-{sanitizedServer}" in argocd — ArgoCD cluster registration
type K8sClusterStore struct {
	client kubernetes.Interface
}

// NewK8sClusterStore returns a K8sClusterStore backed by client.
func NewK8sClusterStore(client kubernetes.Interface) *K8sClusterStore {
	return &K8sClusterStore{client: client}
}

// Compile-time interface check.
var _ domain.ClusterStore = (*K8sClusterStore)(nil)

// ListClusters returns all registered clusters from ConfigMaps in suparship-system.
func (s *K8sClusterStore) ListClusters(ctx context.Context) ([]domain.Cluster, error) {
	cms, err := s.client.CoreV1().ConfigMaps(suparshipSystemNS).List(ctx, metav1.ListOptions{
		LabelSelector: labelType + "=cluster",
	})
	if err != nil {
		return nil, fmt.Errorf("listing cluster configmaps: %w", err)
	}
	clusters := make([]domain.Cluster, 0, len(cms.Items))
	for _, cm := range cms.Items {
		c, err := clusterFromConfigMap(&cm)
		if err != nil {
			return nil, fmt.Errorf("parsing cluster %q: %w", cm.Name, err)
		}
		clusters = append(clusters, *c)
	}
	return clusters, nil
}

// GetCluster returns a single cluster by name.
func (s *K8sClusterStore) GetCluster(ctx context.Context, name string) (*domain.Cluster, error) {
	cm, err := s.client.CoreV1().ConfigMaps(suparshipSystemNS).Get(ctx, clusterConfigMapPrefix+name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("cluster %q not found", name)
		}
		return nil, fmt.Errorf("getting cluster %q: %w", name, err)
	}
	return clusterFromConfigMap(cm)
}

// CreateCluster registers a new cluster. It:
//  1. Stores cluster metadata as a ConfigMap in suparship-system.
//  2. Stores the raw kubeconfig as a Secret in suparship-system.
//  3. Registers the cluster with ArgoCD by creating an ArgoCD cluster Secret
//     in the argocd namespace, with credentials extracted from the kubeconfig.
func (s *K8sClusterStore) CreateCluster(ctx context.Context, cluster domain.Cluster, kubeconfig []byte) error {
	if err := domain.ValidateClusterName(cluster.Name); err != nil {
		return err
	}

	// 1. Store cluster metadata ConfigMap.
	data, err := json.Marshal(cluster)
	if err != nil {
		return fmt.Errorf("marshaling cluster: %w", err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterConfigMapPrefix + cluster.Name,
			Namespace: suparshipSystemNS,
			Labels: map[string]string{
				labelManagedBy: "suparship",
				labelType:      "cluster",
				labelCluster:   cluster.Name,
			},
		},
		Data: map[string]string{
			clusterConfigMapKey: string(data),
		},
	}
	if _, err := s.client.CoreV1().ConfigMaps(suparshipSystemNS).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating cluster configmap: %w", err)
		}
		if _, err := s.client.CoreV1().ConfigMaps(suparshipSystemNS).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating cluster configmap: %w", err)
		}
	}

	// 2. Store raw kubeconfig Secret.
	kubeconfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterKubeconfigPrefix + cluster.Name,
			Namespace: suparshipSystemNS,
			Labels: map[string]string{
				labelManagedBy: "suparship",
				labelType:      "cluster-kubeconfig",
				labelCluster:   cluster.Name,
			},
		},
		Data: map[string][]byte{
			clusterKubeconfigKey: kubeconfig,
		},
	}
	if _, err := s.client.CoreV1().Secrets(suparshipSystemNS).Create(ctx, kubeconfigSecret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating cluster kubeconfig secret: %w", err)
		}
		if _, err := s.client.CoreV1().Secrets(suparshipSystemNS).Update(ctx, kubeconfigSecret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating cluster kubeconfig secret: %w", err)
		}
	}

	// 3. Register with ArgoCD.
	if err := s.registerWithArgoCD(ctx, cluster, kubeconfig); err != nil {
		// Non-fatal: ArgoCD may not be installed. Log but don't fail.
		// The cluster is still usable for log streaming; the operator must
		// register it with ArgoCD separately if ArgoCD is present.
		_ = err // TODO: log warning
	}

	return nil
}

// DeleteCluster removes a cluster registration. The ArgoCD cluster Secret
// is only removed if it was created by suparship (has the managed-by label).
func (s *K8sClusterStore) DeleteCluster(ctx context.Context, name string) error {
	cluster, err := s.GetCluster(ctx, name)
	if err != nil {
		return err
	}

	// Remove ArgoCD cluster Secret only if suparship owns it.
	argoCDSecretName := argoCDClusterSecretName(cluster.APIServer)
	existing, getErr := s.client.CoreV1().Secrets(argoCDNS).Get(ctx, argoCDSecretName, metav1.GetOptions{})
	if getErr == nil && isOwnedBySuparship(existing.Labels) {
		if err := s.client.CoreV1().Secrets(argoCDNS).Delete(ctx, argoCDSecretName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting argocd cluster secret: %w", err)
		}
	}

	// Remove kubeconfig Secret.
	if err := s.client.CoreV1().Secrets(suparshipSystemNS).Delete(ctx, clusterKubeconfigPrefix+name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting cluster kubeconfig secret: %w", err)
	}

	// Remove metadata ConfigMap.
	if err := s.client.CoreV1().ConfigMaps(suparshipSystemNS).Delete(ctx, clusterConfigMapPrefix+name, metav1.DeleteOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("cluster %q not found", name)
		}
		return fmt.Errorf("deleting cluster configmap: %w", err)
	}

	return nil
}

// GetKubeconfig retrieves the raw kubeconfig bytes for a registered cluster.
// Used by ClusterClientPool to build per-cluster kubernetes.Interface clients.
func (s *K8sClusterStore) GetKubeconfig(ctx context.Context, clusterName string) ([]byte, error) {
	secret, err := s.client.CoreV1().Secrets(suparshipSystemNS).Get(ctx, clusterKubeconfigPrefix+clusterName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("kubeconfig for cluster %q not found", clusterName)
		}
		return nil, fmt.Errorf("getting kubeconfig secret: %w", err)
	}
	kc, ok := secret.Data[clusterKubeconfigKey]
	if !ok {
		return nil, fmt.Errorf("kubeconfig secret for cluster %q has no %q key", clusterName, clusterKubeconfigKey)
	}
	return kc, nil
}

// ── ArgoCD registration ───────────────────────────────────────────────────────

// argoCDClusterConfig is the JSON structure ArgoCD expects in the "config"
// field of its cluster Secret.
type argoCDClusterConfig struct {
	BearerToken     string              `json:"bearerToken,omitempty"`
	TLSClientConfig argoCDTLSConfig     `json:"tlsClientConfig,omitempty"`
}

type argoCDTLSConfig struct {
	Insecure bool   `json:"insecure,omitempty"`
	CAData   []byte `json:"caData,omitempty"`
	CertData []byte `json:"certData,omitempty"`
	KeyData  []byte `json:"keyData,omitempty"`
}

// registerWithArgoCD creates an ArgoCD cluster Secret from the provided
// kubeconfig. ArgoCD uses this Secret to deploy Applications to the cluster.
//
// If an ArgoCD Secret for the same API server URL already exists but was
// not created by suparship (missing suparship.io/managed-by label), the
// Secret is left untouched to avoid overwriting externally managed clusters.
func (s *K8sClusterStore) registerWithArgoCD(ctx context.Context, cluster domain.Cluster, kubeconfigBytes []byte) error {
	config, err := clientcmd.NewClientConfigFromBytes(kubeconfigBytes)
	if err != nil {
		return fmt.Errorf("parsing kubeconfig: %w", err)
	}
	restCfg, err := config.ClientConfig()
	if err != nil {
		return fmt.Errorf("building rest config from kubeconfig: %w", err)
	}

	argoCDCfg := argoCDClusterConfig{
		BearerToken: restCfg.BearerToken,
		TLSClientConfig: argoCDTLSConfig{
			Insecure: restCfg.TLSClientConfig.Insecure,
			CAData:   restCfg.TLSClientConfig.CAData,
			CertData: restCfg.TLSClientConfig.CertData,
			KeyData:  restCfg.TLSClientConfig.KeyData,
		},
	}

	argoCDCfgJSON, err := json.Marshal(argoCDCfg)
	if err != nil {
		return fmt.Errorf("marshaling argocd cluster config: %w", err)
	}

	secretName := argoCDClusterSecretName(cluster.APIServer)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: argoCDNS,
			Labels: map[string]string{
				argoCDSecretType: argoCDCluster,
				labelManagedBy:   "suparship",
				labelCluster:     cluster.Name,
			},
			Annotations: map[string]string{
				"suparship.io/cluster-name": cluster.Name,
			},
		},
		StringData: map[string]string{
			"name":   cluster.Name,
			"server": cluster.APIServer,
			"config": string(argoCDCfgJSON),
		},
	}

	if _, err := s.client.CoreV1().Secrets(argoCDNS).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating argocd cluster secret: %w", err)
		}
		// Secret already exists — only update if suparship owns it.
		existing, getErr := s.client.CoreV1().Secrets(argoCDNS).Get(ctx, secretName, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("reading existing argocd cluster secret: %w", getErr)
		}
		if !isOwnedBySuparship(existing.Labels) {
			return fmt.Errorf("argocd cluster secret %q exists but is not managed by suparship — refusing to overwrite", secretName)
		}
		if _, err := s.client.CoreV1().Secrets(argoCDNS).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating argocd cluster secret: %w", err)
		}
	}

	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func clusterFromConfigMap(cm *corev1.ConfigMap) (*domain.Cluster, error) {
	raw, ok := cm.Data[clusterConfigMapKey]
	if !ok {
		return nil, fmt.Errorf("configmap %q missing %q key", cm.Name, clusterConfigMapKey)
	}
	var c domain.Cluster
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("unmarshaling cluster from configmap: %w", err)
	}
	return &c, nil
}

// argoCDClusterSecretName derives a stable Secret name from the API server URL.
// ArgoCD itself uses a similar hash-based scheme; we use a simpler sanitized prefix
// to keep names predictable.
func argoCDClusterSecretName(apiServer string) string {
	// Sanitize by removing scheme and replacing non-alphanumeric chars with hyphens.
	name := apiServer
	for _, prefix := range []string{"https://", "http://"} {
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			name = name[len(prefix):]
			break
		}
	}
	sanitized := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			sanitized = append(sanitized, ch)
		} else if ch >= 'A' && ch <= 'Z' {
			sanitized = append(sanitized, ch+32) // lowercase
		} else {
			sanitized = append(sanitized, '-')
		}
	}
	s := string(sanitized)
	// Trim trailing hyphens and cap at 63 chars total (with prefix).
	for len(s) > 0 && s[len(s)-1] == '-' {
		s = s[:len(s)-1]
	}
	const maxSuffix = 63 - len(argoCDClusterSecretPrefix)
	if len(s) > maxSuffix {
		s = s[:maxSuffix]
	}
	return argoCDClusterSecretPrefix + s
}

// isOwnedBySuparship returns true if the labels indicate the resource was
// created and is managed by suparship. Used to avoid overwriting or deleting
// ArgoCD cluster Secrets that were registered externally.
func isOwnedBySuparship(labels map[string]string) bool {
	return labels != nil && labels[labelManagedBy] == "suparship"
}
