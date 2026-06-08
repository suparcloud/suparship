package rbac

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/version"
)

// Compile-time check that K8sOrgProvider satisfies OrgStore.
var _ OrgStore = (*K8sOrgProvider)(nil)

// OrgEnvNamesAdapter wraps any OrgProvider and implements the
// compat.OrgEnvsReader interface so that org-level environments can be
// resolved by the compat store without a direct rbac→compat import cycle.
type OrgEnvNamesAdapter struct{ p OrgProvider }

// NewOrgEnvNamesAdapter returns an OrgEnvNamesAdapter wrapping p.
func NewOrgEnvNamesAdapter(p OrgProvider) *OrgEnvNamesAdapter {
	return &OrgEnvNamesAdapter{p: p}
}

// OrgEnvironmentNames returns the canonical environment names from the org
// config. Returns nil when the org cannot be read.
func (a *OrgEnvNamesAdapter) OrgEnvironmentNames(ctx context.Context) []string {
	if a == nil || a.p == nil {
		return nil
	}
	org, err := a.p.GetOrg(ctx)
	if err != nil || org == nil {
		return nil
	}
	names := make([]string, 0, len(org.Environments))
	for _, e := range org.Environments {
		names = append(names, e.Name)
	}
	return names
}

// K8sOrgProvider reads and writes the org configuration from a Kubernetes
// ConfigMap. It implements both OrgProvider (read) and OrgStore (read+write).
// If the ConfigMap does not exist, GetOrg returns the fallback Org.
type K8sOrgProvider struct {
	client      kubernetes.Interface
	fallbackOrg *Org
}

// NewK8sOrgProvider creates an OrgStore backed by the org ConfigMap.
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

// SaveOrg persists the org configuration to the ConfigMap, creating it if it
// does not exist. The org is validated before writing.
func (p *K8sOrgProvider) SaveOrg(ctx context.Context, org *Org) error {
	if err := org.Validate(); err != nil {
		return fmt.Errorf("invalid org: %w", err)
	}

	// Stamp the schema version this binary writes so future upgrades can
	// detect the format (see CheckSchema / docs/upgrading.md).
	org.SchemaVersion = version.Schema

	data, err := org.Marshal()
	if err != nil {
		return err
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName,
			Namespace: ConfigMapNamespace,
			Labels: map[string]string{
				"suparship.io/type": "org",
			},
		},
		Data: map[string]string{
			ConfigMapKey: string(data),
		},
	}

	existing, err := p.client.CoreV1().ConfigMaps(ConfigMapNamespace).Get(ctx, ConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := p.client.CoreV1().ConfigMaps(ConfigMapNamespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating org configmap: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading org configmap: %w", err)
	}

	existing.Data = cm.Data
	if _, err := p.client.CoreV1().ConfigMaps(ConfigMapNamespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating org configmap: %w", err)
	}
	return nil
}
