package rbac

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// K8sOrgProvider reads the org configuration from a Kubernetes ConfigMap.
// If the ConfigMap does not exist, it returns the fallback Org.
type K8sOrgProvider struct {
	client      kubernetes.Interface
	fallbackOrg *Org
}

// NewK8sOrgProvider creates an OrgProvider backed by the org ConfigMap.
// The fallbackOrg is returned when the ConfigMap has not been created yet.
func NewK8sOrgProvider(client kubernetes.Interface, fallbackOrg *Org) *K8sOrgProvider {
	return &K8sOrgProvider{client: client, fallbackOrg: fallbackOrg}
}

// GetOrg reads the org configuration from the ConfigMap. Returns the fallback
// org if the ConfigMap does not exist.
func (p *K8sOrgProvider) GetOrg(ctx context.Context) (*Org, error) {
	cm, err := p.client.CoreV1().ConfigMaps(ConfigMapNamespace).Get(
		ctx, ConfigMapName, metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return p.fallbackOrg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading configmap %s/%s: %w", ConfigMapNamespace, ConfigMapName, err)
	}

	data, ok := cm.Data[ConfigMapKey]
	if !ok {
		return nil, fmt.Errorf("configmap %s/%s missing key %q", ConfigMapNamespace, ConfigMapName, ConfigMapKey)
	}

	return ParseOrg([]byte(data))
}
