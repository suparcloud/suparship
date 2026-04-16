package tpl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	registryConfigMapName = "suparship-template-registry"
	registryConfigMapKey  = "registry.json"
	systemNamespace       = "suparship-system"
)

// ErrRegistryNotFound is returned when no template registry ConfigMap exists.
var ErrRegistryNotFound = errors.New("template registry not found")

// RegistryStore reads and writes the template registry ConfigMap.
type RegistryStore struct {
	client kubernetes.Interface
}

// NewRegistryStore creates a RegistryStore.
func NewRegistryStore(client kubernetes.Interface) *RegistryStore {
	return &RegistryStore{client: client}
}

// Get returns the current template registry.
func (s *RegistryStore) Get(ctx context.Context) (*TemplateRegistry, error) {
	cm, err := s.client.CoreV1().ConfigMaps(systemNamespace).Get(ctx, registryConfigMapName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrRegistryNotFound
		}
		return nil, fmt.Errorf("get template registry: %w", err)
	}

	data, ok := cm.Data[registryConfigMapKey]
	if !ok || data == "" {
		return nil, ErrRegistryNotFound
	}

	var reg TemplateRegistry
	if err := json.Unmarshal([]byte(data), &reg); err != nil {
		return nil, fmt.Errorf("parse template registry: %w", err)
	}
	return &reg, nil
}

// Save persists the template registry as a ConfigMap.
func (s *RegistryStore) Save(ctx context.Context, reg *TemplateRegistry) error {
	data, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("marshal template registry: %w", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registryConfigMapName,
			Namespace: systemNamespace,
			Labels: map[string]string{
				"suparship.io/managed-by": "suparship",
				"suparship.io/type":       "template-registry",
			},
		},
		Data: map[string]string{
			registryConfigMapKey: string(data),
		},
	}

	existing, err := s.client.CoreV1().ConfigMaps(systemNamespace).Get(ctx, registryConfigMapName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, createErr := s.client.CoreV1().ConfigMaps(systemNamespace).Create(ctx, cm, metav1.CreateOptions{})
			if createErr != nil {
				return fmt.Errorf("create template registry: %w", createErr)
			}
			return nil
		}
		return fmt.Errorf("get existing template registry: %w", err)
	}

	existing.Data = cm.Data
	existing.Labels = cm.Labels
	_, err = s.client.CoreV1().ConfigMaps(systemNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update template registry: %w", err)
	}
	return nil
}
