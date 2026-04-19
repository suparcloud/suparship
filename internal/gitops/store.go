package gitops

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/envconfig"
)

const (
	gitopsConfigMapName = "suparship-gitops-config"
	gitopsConfigMapKey  = "gitops.yaml"

	// ManagedCredentialSecretName is the K8s Secret automatically managed by
	// suparship to hold GitOps repository credentials. Users never need to
	// create or reference this Secret directly.
	ManagedCredentialSecretName = "suparship-gitops-credentials"
)

// ConfigStore reads and writes the GitOps repository configuration
// persisted as a ConfigMap in the suparship-system namespace.
type ConfigStore struct {
	client kubernetes.Interface
}

// NewConfigStore creates a ConfigStore backed by the given client.
func NewConfigStore(client kubernetes.Interface) *ConfigStore {
	return &ConfigStore{client: client}
}

// Get returns the current GitOps configuration.
// Returns ErrConfigNotFound when no configuration exists.
func (s *ConfigStore) Get(ctx context.Context) (*RepoConfig, error) {
	cm, err := s.client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Get(ctx, gitopsConfigMapName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrConfigNotFound
		}
		return nil, fmt.Errorf("get gitops configmap: %w", err)
	}

	data, ok := cm.Data[gitopsConfigMapKey]
	if !ok || data == "" {
		return nil, ErrConfigNotFound
	}

	var cfg RepoConfig
	if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse gitops config: %w", err)
	}
	return &cfg, nil
}

// Save persists the GitOps configuration as a ConfigMap.
// Creates or updates as needed.
func (s *ConfigStore) Save(ctx context.Context, cfg *RepoConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal gitops config: %w", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gitopsConfigMapName,
			Namespace: envconfig.SystemNamespace,
			Labels: map[string]string{
				"suparship.io/managed-by": "suparship",
				"suparship.io/type":       "gitops-config",
			},
		},
		Data: map[string]string{
			gitopsConfigMapKey: string(data),
		},
	}

	existing, err := s.client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Get(ctx, gitopsConfigMapName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, createErr := s.client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Create(ctx, cm, metav1.CreateOptions{})
			if createErr != nil {
				return fmt.Errorf("create gitops configmap: %w", createErr)
			}
			return nil
		}
		return fmt.Errorf("get existing gitops configmap: %w", err)
	}

	existing.Data = cm.Data
	existing.Labels = cm.Labels
	_, err = s.client.CoreV1().ConfigMaps(envconfig.SystemNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update gitops configmap: %w", err)
	}
	return nil
}

// SaveCredentials creates or updates the managed GitOps credential Secret
// (ManagedCredentialSecretName) with provider-appropriate keys. Only non-empty
// values are written; omitted values leave existing keys untouched.
func (s *ConfigStore) SaveCredentials(ctx context.Context, provider, token, username, password string) error {
	data := map[string][]byte{}

	switch provider {
	case "github", "gitlab", "gitea":
		if token != "" {
			data["token"] = []byte(token)
		}
	case "bitbucket":
		if password != "" {
			data["appPassword"] = []byte(password)
		}
		if username != "" {
			data["username"] = []byte(username)
		}
	default: // generic
		if password != "" {
			data["password"] = []byte(password)
		}
		if username != "" {
			data["username"] = []byte(username)
		}
	}

	if len(data) == 0 {
		return nil
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ManagedCredentialSecretName,
			Namespace: envconfig.SystemNamespace,
			Labels: map[string]string{
				"suparship.io/managed-by": "suparship",
				"suparship.io/type":       "gitops-credentials",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}

	existing, err := s.client.CoreV1().Secrets(envconfig.SystemNamespace).Get(ctx, ManagedCredentialSecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, createErr := s.client.CoreV1().Secrets(envconfig.SystemNamespace).Create(ctx, secret, metav1.CreateOptions{})
			if createErr != nil {
				return fmt.Errorf("create gitops credential secret: %w", createErr)
			}
			return nil
		}
		return fmt.Errorf("get existing gitops credential secret: %w", err)
	}

	if existing.Data == nil {
		existing.Data = map[string][]byte{}
	}
	for k, v := range data {
		existing.Data[k] = v
	}
	existing.Labels = secret.Labels
	_, err = s.client.CoreV1().Secrets(envconfig.SystemNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update gitops credential secret: %w", err)
	}
	return nil
}

// HasCredentials reports whether the managed credential Secret exists and
// contains at least one credential key.
func (s *ConfigStore) HasCredentials(ctx context.Context) (bool, error) {
	secret, err := s.client.CoreV1().Secrets(envconfig.SystemNamespace).Get(ctx, ManagedCredentialSecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("check gitops credentials: %w", err)
	}
	return len(secret.Data) > 0, nil
}

// GetCredentials reads the Secret referenced by cfg.AuthSecretRef and returns
// provider-appropriate username and password/token values.
func (s *ConfigStore) GetCredentials(ctx context.Context, cfg *RepoConfig) (username, password string, err error) {
	if cfg.AuthSecretRef == "" {
		return "", "", nil
	}

	secret, err := s.client.CoreV1().Secrets(envconfig.SystemNamespace).Get(ctx, cfg.AuthSecretRef, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("get auth secret %q: %w", cfg.AuthSecretRef, err)
	}

	tokenKey := credentialTokenKey(cfg.Provider)
	if tok, ok := secret.Data[tokenKey]; ok {
		username = string(secret.Data["username"])
		if username == "" {
			username = defaultUsername(cfg.Provider)
		}
		return username, string(tok), nil
	}

	if pw, ok := secret.Data["password"]; ok {
		return string(secret.Data["username"]), string(pw), nil
	}

	return "", "", fmt.Errorf("auth secret %q has no recognized credential key", cfg.AuthSecretRef)
}

// credentialTokenKey returns the Secret data key that holds the primary
// credential for the given provider.
func credentialTokenKey(provider string) string {
	switch provider {
	case "github", "gitlab", "gitea":
		return "token"
	case "bitbucket":
		return "appPassword"
	default:
		return "password"
	}
}

// defaultUsername returns a sensible default username for token-based auth.
func defaultUsername(provider string) string {
	switch provider {
	case "github":
		return "x-access-token"
	case "gitlab":
		return "oauth2"
	case "gitea":
		return "suparship"
	default:
		return ""
	}
}
