// Package backup captures and restores suparship's control-plane state — the
// ConfigMaps and Secrets in the suparship-system namespace that hold the org
// config, gitops/registry config, project/app/cluster/preview records,
// env-var layers, admin credential, 1Password SA token, and per-cluster
// Connect-token stashes + kubeconfigs.
//
// All authoritative state lives in one namespace and is name-prefixed
// "suparship-", so capture is a prefix+namespace sweep rather than relying on
// the (inconsistent) label conventions. ArgoCD-namespace secrets (cluster
// registrations, repo creds) are intentionally NOT captured: suparship
// re-derives them from the kubeconfig secrets + gitops config on reconcile.
// SealedSecret CRs (template credentials) are out of scope — encrypted and
// regenerable by re-adding the template source.
package backup

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// SystemNamespace is where all backup-worthy state lives.
const SystemNamespace = "suparship-system"

// resourcePrefix selects suparship-owned ConfigMaps/Secrets by name.
const resourcePrefix = "suparship-"

// APIVersion identifies the archive format for forward compatibility.
const APIVersion = "suparship.io/backup/v1"

// Archive is a portable snapshot of control-plane state.
type Archive struct {
	APIVersion string     `yaml:"apiVersion" json:"apiVersion"`
	CreatedAt  string     `yaml:"createdAt" json:"createdAt"`
	Namespace  string     `yaml:"namespace" json:"namespace"`
	Resources  []Resource `yaml:"resources" json:"resources"`
}

// Resource is one captured ConfigMap or Secret, stripped of server-managed
// metadata (resourceVersion, uid, creationTimestamp, managedFields, owner
// refs) so it re-applies cleanly to any cluster.
type Resource struct {
	Kind        string            `yaml:"kind" json:"kind"` // "ConfigMap" | "Secret"
	Name        string            `yaml:"name" json:"name"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
	// SecretType carries corev1.Secret.Type (empty for ConfigMaps).
	SecretType string `yaml:"secretType,omitempty" json:"secretType,omitempty"`
	// Data holds ConfigMap string data.
	Data map[string]string `yaml:"data,omitempty" json:"data,omitempty"`
	// BinaryData holds ConfigMap binary data and Secret data (both []byte;
	// marshal as base64 in YAML/JSON).
	BinaryData map[string][]byte `yaml:"binaryData,omitempty" json:"binaryData,omitempty"`
}

const (
	kindConfigMap = "ConfigMap"
	kindSecret    = "Secret"
)

// dropAnnotations are server/tool-managed annotations we never restore.
var dropAnnotations = map[string]bool{
	"kubectl.kubernetes.io/last-applied-configuration": true,
}

// Create snapshots all suparship ConfigMaps and Secrets in namespace (default
// SystemNamespace). extraNames lets the caller include resources that don't
// match the "suparship-" prefix — e.g. an admin Secret renamed via
// adminSecretRef. now is injected for deterministic timestamps in tests.
func Create(ctx context.Context, client kubernetes.Interface, namespace string, extraNames []string, now time.Time) (*Archive, error) {
	if namespace == "" {
		namespace = SystemNamespace
	}
	include := func(name string) bool {
		if strings.HasPrefix(name, resourcePrefix) {
			return true
		}
		for _, n := range extraNames {
			if n == name {
				return true
			}
		}
		return false
	}

	arch := &Archive{
		APIVersion: APIVersion,
		CreatedAt:  now.UTC().Format(time.RFC3339),
		Namespace:  namespace,
	}

	cms, err := client.CoreV1().ConfigMaps(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing configmaps in %s: %w", namespace, err)
	}
	for i := range cms.Items {
		cm := &cms.Items[i]
		if !include(cm.Name) {
			continue
		}
		arch.Resources = append(arch.Resources, Resource{
			Kind:        kindConfigMap,
			Name:        cm.Name,
			Labels:      cm.Labels,
			Annotations: sanitizeAnnotations(cm.Annotations),
			Data:        cm.Data,
			BinaryData:  cm.BinaryData,
		})
	}

	secs, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing secrets in %s: %w", namespace, err)
	}
	for i := range secs.Items {
		s := &secs.Items[i]
		if !include(s.Name) {
			continue
		}
		// Skip ServiceAccount token Secrets — they're minted by the cluster,
		// not suparship state, and restoring them is meaningless.
		if s.Type == corev1.SecretTypeServiceAccountToken {
			continue
		}
		arch.Resources = append(arch.Resources, Resource{
			Kind:        kindSecret,
			Name:        s.Name,
			Labels:      s.Labels,
			Annotations: sanitizeAnnotations(s.Annotations),
			SecretType:  string(s.Type),
			BinaryData:  s.Data,
		})
	}

	// Stable order so successive backups diff cleanly.
	sort.Slice(arch.Resources, func(i, j int) bool {
		if arch.Resources[i].Kind != arch.Resources[j].Kind {
			return arch.Resources[i].Kind < arch.Resources[j].Kind
		}
		return arch.Resources[i].Name < arch.Resources[j].Name
	})
	return arch, nil
}

func sanitizeAnnotations(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if dropAnnotations[k] {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RestoreResult reports what Restore did.
type RestoreResult struct {
	Created []string
	Updated []string
}

// Restore applies the archive's resources into namespace (default
// SystemNamespace; the archive's own Namespace is used when namespace is
// empty), creating missing ones and updating existing ones in place
// (preserving their resourceVersion). It does not delete resources absent from
// the archive — restore is additive, never destructive.
func Restore(ctx context.Context, client kubernetes.Interface, arch *Archive, namespace string) (*RestoreResult, error) {
	if arch == nil {
		return nil, fmt.Errorf("nil archive")
	}
	if arch.APIVersion != APIVersion {
		return nil, fmt.Errorf("unsupported archive apiVersion %q (want %q)", arch.APIVersion, APIVersion)
	}
	if namespace == "" {
		namespace = arch.Namespace
	}
	if namespace == "" {
		namespace = SystemNamespace
	}

	res := &RestoreResult{}
	for _, r := range arch.Resources {
		switch r.Kind {
		case kindConfigMap:
			created, err := restoreConfigMap(ctx, client, namespace, r)
			if err != nil {
				return res, err
			}
			recordResult(res, r.Name, created)
		case kindSecret:
			created, err := restoreSecret(ctx, client, namespace, r)
			if err != nil {
				return res, err
			}
			recordResult(res, r.Name, created)
		default:
			return res, fmt.Errorf("unknown resource kind %q for %q", r.Kind, r.Name)
		}
	}
	return res, nil
}

func recordResult(res *RestoreResult, name string, created bool) {
	if created {
		res.Created = append(res.Created, name)
	} else {
		res.Updated = append(res.Updated, name)
	}
}

func restoreConfigMap(ctx context.Context, client kubernetes.Interface, ns string, r Resource) (created bool, err error) {
	api := client.CoreV1().ConfigMaps(ns)
	existing, getErr := api.Get(ctx, r.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(getErr) {
		_, err = api.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: r.Name, Namespace: ns, Labels: r.Labels, Annotations: r.Annotations},
			Data:       r.Data,
			BinaryData: r.BinaryData,
		}, metav1.CreateOptions{})
		return true, err
	}
	if getErr != nil {
		return false, fmt.Errorf("get configmap %q: %w", r.Name, getErr)
	}
	existing.Labels = r.Labels
	existing.Annotations = r.Annotations
	existing.Data = r.Data
	existing.BinaryData = r.BinaryData
	_, err = api.Update(ctx, existing, metav1.UpdateOptions{})
	return false, err
}

func restoreSecret(ctx context.Context, client kubernetes.Interface, ns string, r Resource) (created bool, err error) {
	api := client.CoreV1().Secrets(ns)
	secretType := corev1.SecretType(r.SecretType)
	if secretType == "" {
		secretType = corev1.SecretTypeOpaque
	}
	existing, getErr := api.Get(ctx, r.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(getErr) {
		_, err = api.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: r.Name, Namespace: ns, Labels: r.Labels, Annotations: r.Annotations},
			Type:       secretType,
			Data:       r.BinaryData,
		}, metav1.CreateOptions{})
		return true, err
	}
	if getErr != nil {
		return false, fmt.Errorf("get secret %q: %w", r.Name, getErr)
	}
	// Secret.Type is immutable — keep the existing type, only refresh data/meta.
	existing.Labels = r.Labels
	existing.Annotations = r.Annotations
	existing.Data = r.BinaryData
	_, err = api.Update(ctx, existing, metav1.UpdateOptions{})
	return false, err
}
