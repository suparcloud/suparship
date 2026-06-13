package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

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

	labelManagedBy   = "suparship.io/managed-by"
	labelType        = "suparship.io/type"
	labelCluster     = "suparship.io/cluster"
	argoCDSecretType = "argocd.argoproj.io/secret-type"
	argoCDCluster    = "cluster"
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

// UpdateClusterMetadata rewrites only the metadata fields an operator can edit
// after registration — DisplayName, BaseDomain, RoutingProfiles, ESONamespace —
// in the cluster's ConfigMap. APIServer, kubeconfig Secret, and the ArgoCD
// cluster Secret are NOT touched (changing the API server means re-registering).
func (s *K8sClusterStore) UpdateClusterMetadata(ctx context.Context, name string, displayName, baseDomain, esoNamespace string, routing domain.RoutingProfiles) (*domain.Cluster, error) {
	cm, err := s.client.CoreV1().ConfigMaps(suparshipSystemNS).Get(ctx, clusterConfigMapPrefix+name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("cluster %q not found", name)
		}
		return nil, fmt.Errorf("getting cluster %q: %w", name, err)
	}
	cluster, err := clusterFromConfigMap(cm)
	if err != nil {
		return nil, err
	}
	if displayName != "" {
		cluster.DisplayName = displayName
	}
	cluster.BaseDomain = baseDomain
	cluster.RoutingProfiles = routing
	if esoNamespace != "" {
		cluster.ESONamespace = esoNamespace
	}
	cluster.Normalize()

	data, err := json.Marshal(cluster)
	if err != nil {
		return nil, fmt.Errorf("marshal cluster %q: %w", name, err)
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[clusterConfigMapKey] = string(data)
	if _, err := s.client.CoreV1().ConfigMaps(suparshipSystemNS).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return nil, fmt.Errorf("update cluster %q metadata: %w", name, err)
	}
	return cluster, nil
}

// CreateCluster registers a new cluster. It:
//  1. Stores the raw kubeconfig as a Secret in suparship-system.
//  2. Registers (or links) the cluster with ArgoCD by looking for a
//     pre-existing ArgoCD cluster Secret. If one exists but was not
//     created by suparship, suparship links to it without touching it;
//     the ArgoCD Secret will NOT be deleted when this cluster is removed.
//     If no ArgoCD Secret exists, suparship creates one and marks it as owned.
//  3. Stores cluster metadata as a ConfigMap in suparship-system, with
//     ArgoCDOwned reflecting whether suparship owns the ArgoCD Secret.
func (s *K8sClusterStore) CreateCluster(ctx context.Context, cluster domain.Cluster, kubeconfig []byte) error {
	if err := domain.ValidateClusterName(cluster.Name); err != nil {
		return err
	}

	// 1. Store raw kubeconfig Secret first (needed for ArgoCD credential extraction).
	if err := s.writeKubeconfigSecret(ctx, cluster.Name, kubeconfig); err != nil {
		return err
	}

	// 2. Register or link with ArgoCD. Determine whether suparship owns the Secret.
	argoCDOwned, err := s.registerWithArgoCD(ctx, cluster, kubeconfig)
	if err != nil {
		// Non-fatal: ArgoCD may not be installed. The cluster is still usable
		// for log streaming; the operator must register it with ArgoCD separately.
		argoCDOwned = false
	}
	cluster.ArgoCDOwned = argoCDOwned

	// 3. Store cluster metadata ConfigMap with final ArgoCDOwned state.
	return s.writeMetadataConfigMap(ctx, cluster)
}

// ImportCluster registers a cluster discovered in ArgoCD. Unlike CreateCluster
// it does NOT create or link an ArgoCD cluster Secret: the source ArgoCD Secret
// already registers this server (and is named by ArgoCD's own scheme, not
// suparship's), so creating ours would double-register the same server. The
// cluster is recorded with ArgoCDOwned=false, which makes DeleteCluster leave
// the source ArgoCD Secret intact. It still stores the (reconstructed) kubeconfig
// so the hub can build a client for status/logs and sealing-cert fetch.
func (s *K8sClusterStore) ImportCluster(ctx context.Context, cluster domain.Cluster, kubeconfig []byte) error {
	if err := domain.ValidateClusterName(cluster.Name); err != nil {
		return err
	}
	cluster.ArgoCDOwned = false
	cluster.Normalize()
	if err := s.writeKubeconfigSecret(ctx, cluster.Name, kubeconfig); err != nil {
		return err
	}
	return s.writeMetadataConfigMap(ctx, cluster)
}

// writeKubeconfigSecret creates (or updates) the kubeconfig Secret for a cluster
// in suparship-system. Shared by CreateCluster and ImportCluster.
func (s *K8sClusterStore) writeKubeconfigSecret(ctx context.Context, clusterName string, kubeconfig []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterKubeconfigPrefix + clusterName,
			Namespace: suparshipSystemNS,
			Labels: map[string]string{
				labelManagedBy: "suparship",
				labelType:      "cluster-kubeconfig",
				labelCluster:   clusterName,
			},
		},
		Data: map[string][]byte{
			clusterKubeconfigKey: kubeconfig,
		},
	}
	if _, err := s.client.CoreV1().Secrets(suparshipSystemNS).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating cluster kubeconfig secret: %w", err)
		}
		if _, err := s.client.CoreV1().Secrets(suparshipSystemNS).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("updating cluster kubeconfig secret: %w", err)
		}
	}
	return nil
}

// writeMetadataConfigMap creates (or updates) the cluster metadata ConfigMap in
// suparship-system. Shared by CreateCluster and ImportCluster.
func (s *K8sClusterStore) writeMetadataConfigMap(ctx context.Context, cluster domain.Cluster) error {
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
	return nil
}

// DeleteCluster removes a cluster registration. The ArgoCD cluster Secret
// is only removed if suparship owns it — i.e. ArgoCDOwned is true in the
// stored metadata, or (for clusters registered before this field existed)
// the Secret carries the suparship.io/managed-by label. Pre-existing ArgoCD
// cluster registrations are never touched.
func (s *K8sClusterStore) DeleteCluster(ctx context.Context, name string) error {
	cluster, err := s.GetCluster(ctx, name)
	if err != nil {
		return err
	}

	// Remove ArgoCD cluster Secret only when suparship owns it.
	// Prefer the stored ArgoCDOwned flag; fall back to label check for clusters
	// registered before ArgoCDOwned was introduced.
	argoCDSecretName := argoCDClusterSecretName(cluster.APIServer)
	existing, getErr := s.client.CoreV1().Secrets(argoCDNS).Get(ctx, argoCDSecretName, metav1.GetOptions{})
	if getErr == nil {
		ownedByFlag := cluster.ArgoCDOwned
		ownedByLabel := isOwnedBySuparship(existing.Labels)
		if ownedByFlag || ownedByLabel {
			if err := s.client.CoreV1().Secrets(argoCDNS).Delete(ctx, argoCDSecretName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("deleting argocd cluster secret: %w", err)
			}
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
// field of its cluster Secret. suparship writes only bearerToken + TLS; the
// extra fields exist so import can detect (and reject) exec/cloud-IAM and
// basic-auth registrations it cannot reconstruct a kubeconfig from. They are
// omitempty, so registration round-trips unchanged.
type argoCDClusterConfig struct {
	BearerToken        string           `json:"bearerToken,omitempty"`
	Username           string           `json:"username,omitempty"`
	Password           string           `json:"password,omitempty"`
	TLSClientConfig    argoCDTLSConfig  `json:"tlsClientConfig,omitempty"`
	AWSAuthConfig      *json.RawMessage `json:"awsAuthConfig,omitempty"`
	ExecProviderConfig *json.RawMessage `json:"execProviderConfig,omitempty"`
}

type argoCDTLSConfig struct {
	Insecure bool   `json:"insecure,omitempty"`
	CAData   []byte `json:"caData,omitempty"`
	CertData []byte `json:"certData,omitempty"`
	KeyData  []byte `json:"keyData,omitempty"`
}

// registerWithArgoCD creates or links an ArgoCD cluster Secret for the given
// cluster. It returns (owned, err) where:
//   - owned=true means suparship created the Secret and will manage it.
//   - owned=false means a pre-existing Secret was found; suparship links to it
//     without modifying it, and will not delete it on cluster removal.
//
// A non-nil error is only returned for unexpected failures (e.g. network
// errors), not for the pre-existing-secret case.
func (s *K8sClusterStore) registerWithArgoCD(ctx context.Context, cluster domain.Cluster, kubeconfigBytes []byte) (owned bool, err error) {
	config, err := clientcmd.NewClientConfigFromBytes(kubeconfigBytes)
	if err != nil {
		return false, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	restCfg, err := config.ClientConfig()
	if err != nil {
		return false, fmt.Errorf("building rest config from kubeconfig: %w", err)
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
		return false, fmt.Errorf("marshaling argocd cluster config: %w", err)
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
			return false, fmt.Errorf("creating argocd cluster secret: %w", err)
		}

		// Secret already exists — check ownership.
		existing, getErr := s.client.CoreV1().Secrets(argoCDNS).Get(ctx, secretName, metav1.GetOptions{})
		if getErr != nil {
			return false, fmt.Errorf("reading existing argocd cluster secret: %w", getErr)
		}

		if !isOwnedBySuparship(existing.Labels) {
			// Pre-existing ArgoCD cluster registration — link without touching it.
			// suparship will not manage or delete this Secret.
			return false, nil
		}

		// Owned by suparship — update credentials.
		if _, err := s.client.CoreV1().Secrets(argoCDNS).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return false, fmt.Errorf("updating argocd cluster secret: %w", err)
		}
		return true, nil
	}

	return true, nil
}

// ── ArgoCD import ──────────────────────────────────────────────────────────────

// ListArgoCDClusters returns every cluster ArgoCD has registered (one ArgoCD
// cluster Secret each), classified for import. Clusters that already exist in
// suparship are flagged AlreadyRegistered (matched by API server); clusters
// ArgoCD authenticates to with exec/cloud-IAM auth are flagged not Importable
// with a reason — suparship's hub client + ArgoCD-secret format only handle
// bearer-token, client-cert, and basic auth.
func (s *K8sClusterStore) ListArgoCDClusters(ctx context.Context) ([]domain.ArgoCDClusterCandidate, error) {
	secrets, err := s.client.CoreV1().Secrets(argoCDNS).List(ctx, metav1.ListOptions{
		LabelSelector: argoCDSecretType + "=" + argoCDCluster,
	})
	if err != nil {
		return nil, fmt.Errorf("listing argocd cluster secrets: %w", err)
	}

	registeredServers := map[string]bool{}
	if clusters, err := s.ListClusters(ctx); err == nil {
		for _, c := range clusters {
			registeredServers[strings.TrimSpace(c.APIServer)] = true
		}
	}

	out := make([]domain.ArgoCDClusterCandidate, 0, len(secrets.Items))
	for i := range secrets.Items {
		sec := &secrets.Items[i]
		name := secretField(sec, "name")
		server := secretField(sec, "server")
		cand := classifyArgoCDCluster(name, server, secretField(sec, "config"))
		cand.AlreadyRegistered = registeredServers[strings.TrimSpace(server)]
		out = append(out, cand)
	}
	return out, nil
}

// ImportArgoCDCluster looks up the ArgoCD cluster registration named `name`,
// reconstructs a kubeconfig from its stored credentials, and registers it with
// suparship via ImportCluster (ArgoCDOwned=false — the source ArgoCD Secret is
// left as the canonical registration). Returns an error (suitable as a skip
// reason) when the cluster is not found, already registered, or uses auth
// suparship cannot reconstruct.
func (s *K8sClusterStore) ImportArgoCDCluster(ctx context.Context, name string) (*domain.Cluster, error) {
	server, configRaw, found, err := s.findArgoCDClusterSecret(ctx, name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("argocd cluster %q not found", name)
	}

	cand := classifyArgoCDCluster(name, server, configRaw)
	if !cand.Importable {
		return nil, fmt.Errorf("%s", cand.Reason)
	}

	if existing, _ := s.GetCluster(ctx, name); existing != nil {
		return nil, fmt.Errorf("cluster %q is already registered", name)
	}

	var cfg argoCDClusterConfig
	if configRaw != "" {
		if err := json.Unmarshal([]byte(configRaw), &cfg); err != nil {
			return nil, fmt.Errorf("parsing argocd cluster config: %w", err)
		}
	}
	kubeconfig, err := buildKubeconfigFromArgoCD(server, cfg)
	if err != nil {
		return nil, err
	}

	cluster := domain.Cluster{
		Name:        name,
		DisplayName: name,
		APIServer:   server,
		Status:      "ready",
		ArgoCDOwned: false,
	}
	cluster.Normalize()
	if err := s.ImportCluster(ctx, cluster, kubeconfig); err != nil {
		return nil, err
	}
	return &cluster, nil
}

// findArgoCDClusterSecret returns the (server, config) of the ArgoCD cluster
// Secret whose data.name equals name. ArgoCD names its cluster Secrets by its
// own scheme, so we match on the stored name field rather than the object name.
func (s *K8sClusterStore) findArgoCDClusterSecret(ctx context.Context, name string) (server, config string, found bool, err error) {
	secrets, err := s.client.CoreV1().Secrets(argoCDNS).List(ctx, metav1.ListOptions{
		LabelSelector: argoCDSecretType + "=" + argoCDCluster,
	})
	if err != nil {
		return "", "", false, fmt.Errorf("listing argocd cluster secrets: %w", err)
	}
	for i := range secrets.Items {
		sec := &secrets.Items[i]
		if secretField(sec, "name") == name {
			return secretField(sec, "server"), secretField(sec, "config"), true, nil
		}
	}
	return "", "", false, nil
}

// classifyArgoCDCluster determines whether an ArgoCD cluster registration can be
// imported and how it authenticates.
func classifyArgoCDCluster(name, server, configRaw string) domain.ArgoCDClusterCandidate {
	cand := domain.ArgoCDClusterCandidate{Name: name, Server: server}
	var cfg argoCDClusterConfig
	if configRaw != "" {
		if err := json.Unmarshal([]byte(configRaw), &cfg); err != nil {
			cand.AuthType = "unknown"
			cand.Reason = "could not parse the ArgoCD cluster config"
			return cand
		}
	}
	switch {
	case cfg.ExecProviderConfig != nil || cfg.AWSAuthConfig != nil:
		cand.AuthType = "exec"
		cand.Reason = "exec / cloud-IAM auth not supported — register with a token-based kubeconfig instead"
	case cfg.BearerToken != "":
		cand.AuthType = "token"
		cand.Importable = true
	case len(cfg.TLSClientConfig.CertData) > 0:
		cand.AuthType = "clientCert"
		cand.Importable = true
	case cfg.Username != "":
		cand.AuthType = "basic"
		cand.Importable = true
	default:
		cand.AuthType = "unknown"
		cand.Reason = "no supported credentials found in the ArgoCD cluster config"
	}
	return cand
}

// buildKubeconfigFromArgoCD reconstructs a kubeconfig from an ArgoCD cluster
// {server, config}. It is the reverse of registerWithArgoCD and supports the
// auth modes classifyArgoCDCluster marks importable (bearer token, client cert,
// basic auth).
func buildKubeconfigFromArgoCD(server string, cfg argoCDClusterConfig) ([]byte, error) {
	if cfg.BearerToken == "" && len(cfg.TLSClientConfig.CertData) == 0 && cfg.Username == "" {
		return nil, fmt.Errorf("argocd cluster config has no supported credentials (bearer token / client cert / basic auth)")
	}

	const key = "default"
	apiCfg := clientcmdapi.NewConfig()

	cluster := &clientcmdapi.Cluster{Server: server}
	if cfg.TLSClientConfig.Insecure {
		cluster.InsecureSkipTLSVerify = true
	} else {
		cluster.CertificateAuthorityData = cfg.TLSClientConfig.CAData
	}
	apiCfg.Clusters[key] = cluster

	apiCfg.AuthInfos[key] = &clientcmdapi.AuthInfo{
		Token:                 cfg.BearerToken,
		ClientCertificateData: cfg.TLSClientConfig.CertData,
		ClientKeyData:         cfg.TLSClientConfig.KeyData,
		Username:              cfg.Username,
		Password:              cfg.Password,
	}
	apiCfg.Contexts[key] = &clientcmdapi.Context{Cluster: key, AuthInfo: key}
	apiCfg.CurrentContext = key

	return clientcmd.Write(*apiCfg)
}

// secretField reads a Secret value from Data (real clusters store the decoded
// value there) falling back to StringData (used by tests that build Secrets
// without the fake clientset's StringData→Data merge).
func secretField(sec *corev1.Secret, key string) string {
	if v, ok := sec.Data[key]; ok {
		return string(v)
	}
	return sec.StringData[key]
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
	// Heal records that were registered with stray whitespace (e.g. a leading
	// space in APIServer breaks ArgoCD destination matching).
	c.Normalize()
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
