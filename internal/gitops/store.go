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
