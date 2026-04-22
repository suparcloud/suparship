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
