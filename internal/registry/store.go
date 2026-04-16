package registry

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"gopkg.in/yaml.v3"
)

const (
	configMapName = "suparship-registry-config"
	configMapKey  = "registry.yaml"
	namespace     = "suparship-system"
)

// Store reads and writes registry configuration from a ConfigMap.
type Store struct {
	client kubernetes.Interface
}

// NewStore creates a registry Store.
func NewStore(client kubernetes.Interface) *Store {
	return &Store{client: client}
}

// Get returns the current registry configuration.
func (s *Store) Get(ctx context.Context) (*Config, error) {
	cm, err := s.client.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrConfigNotFound
		}
		return nil, fmt.Errorf("get registry configmap: %w", err)
	}

	data, ok := cm.Data[configMapKey]
	if !ok || data == "" {
		return nil, ErrConfigNotFound
	}

	var cfg Config
	if err := yaml.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("parse registry config: %w", err)
	}
	return &cfg, nil
}

// Save persists the registry configuration.
func (s *Store) Save(ctx context.Context, cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal registry config: %w", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
			Labels: map[string]string{
				"suparship.io/managed-by": "suparship",
				"suparship.io/type":       "registry-config",
			},
		},
		Data: map[string]string{
			configMapKey: string(data),
		},
	}

	existing, err := s.client.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, createErr := s.client.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
			if createErr != nil {
				return fmt.Errorf("create registry configmap: %w", createErr)
			}
			return nil
		}
		return fmt.Errorf("get existing registry configmap: %w", err)
	}

	existing.Data = cm.Data
	existing.Labels = cm.Labels
	_, err = s.client.CoreV1().ConfigMaps(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update registry configmap: %w", err)
	}
	return nil
}
