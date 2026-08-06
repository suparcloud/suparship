package server

import (
	"context"
	"fmt"

	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/secrets"
	"k8s.io/client-go/kubernetes"
)

const saTokenNamespace = "suparship-system"

// KubeSATokenStore persists the 1Password SA token as a K8s Secret.
type KubeSATokenStore struct {
	client kubernetes.Interface
}

func NewKubeSATokenStore(client kubernetes.Interface) *KubeSATokenStore {
	return &KubeSATokenStore{client: client}
}

func (s *KubeSATokenStore) SaveToken(ctx context.Context, token string) error {
	if err := k8s.EnsureNamespace(ctx, s.client, saTokenNamespace); err != nil {
		return fmt.Errorf("ensuring namespace %s: %w", saTokenNamespace, err)
	}
	return k8s.UpsertSecretData(ctx, s.client, saTokenNamespace, secrets.SATokenSecretName, map[string][]byte{
		secrets.SATokenSecretKey: []byte(token),
	})
}

func (s *KubeSATokenStore) LoadToken(ctx context.Context) (string, error) {
	data, err := k8s.GetSecretData(ctx, s.client, saTokenNamespace, secrets.SATokenSecretName, secrets.SATokenSecretKey)
	if err != nil {
		return "", err
	}
	if data == nil {
		return "", fmt.Errorf("SA token not found in %s/%s", saTokenNamespace, secrets.SATokenSecretName)
	}
	return string(data), nil
}

// KubeVaultTokenStore persists suparship's HashiCorp Vault write token as a
// K8s Secret (secrets.VaultTokenSecretName) — the Vault backend's counterpart
// to KubeSATokenStore, satisfying the same SATokenStore interface.
type KubeVaultTokenStore struct {
	client kubernetes.Interface
}

func NewKubeVaultTokenStore(client kubernetes.Interface) *KubeVaultTokenStore {
	return &KubeVaultTokenStore{client: client}
}

func (s *KubeVaultTokenStore) SaveToken(ctx context.Context, token string) error {
	if err := k8s.EnsureNamespace(ctx, s.client, saTokenNamespace); err != nil {
		return fmt.Errorf("ensuring namespace %s: %w", saTokenNamespace, err)
	}
	return k8s.UpsertSecretData(ctx, s.client, saTokenNamespace, secrets.VaultTokenSecretName, map[string][]byte{
		secrets.VaultTokenSecretKey: []byte(token),
	})
}

func (s *KubeVaultTokenStore) LoadToken(ctx context.Context) (string, error) {
	data, err := k8s.GetSecretData(ctx, s.client, saTokenNamespace, secrets.VaultTokenSecretName, secrets.VaultTokenSecretKey)
	if err != nil {
		return "", err
	}
	if data == nil {
		return "", fmt.Errorf("vault token not found in %s/%s", saTokenNamespace, secrets.VaultTokenSecretName)
	}
	return string(data), nil
}
