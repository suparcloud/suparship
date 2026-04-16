// Package envconfig — k8s.go provides helpers for writing and reading the
// upper-level (Org, Environment, Project) runtime ConfigMaps in the
// suparship-system namespace.
//
// # Why separate from the domain store
//
// The domain-object stores (rbac.K8sOrgProvider, project.K8sStore, etc.) persist
// the full config document (org.yaml, project.yaml, app.json). The helpers here
// write a separate, purpose-built ConfigMap for each upper hierarchy level that
// contains ONLY the resolved env var key/value pairs as ConfigMap data. These
// ConfigMaps are annotated for automatic replication into app namespaces via
// Stakater Replicator so that containers can load them with envFrom.
//
// # Naming
//
//	suparship-envvars-org              — Org level
//	suparship-envvars-env-{envtype}    — Environment level (e.g. "-staging")
//	suparship-envvars-project-{name}   — Project level
//
// All ConfigMaps live in the suparship-system namespace.
//
// # Replicator annotations
//
// Org ConfigMap:         replicate-to: ".*"          (all namespaces)
// Environment ConfigMap: replicate-to: ".*-{envtype}"  (pattern match)
// Project ConfigMap:     replicate-to-matching: "suparship.io/project={name}"
//                        (requires app namespaces to carry this label)
//
// # ExternalSecrets for upper levels
//
// SecretRefs at Org/Environment/Project levels are rendered as ExternalSecret
// manifests and committed to gitops-output/_infra/ by the GitOps publisher.
// ESO creates K8s Secrets in suparship-system; those Secrets are also annotated
// for Replicator replication. See internal/gitops/eso.go.
package envconfig

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// SystemNamespace is where suparship control-plane ConfigMaps live.
	SystemNamespace = "suparship-system"

	// ReplicatorAnnotation is the Stakater Replicator annotation for namespace
	// pattern-based replication (regex match on namespace name).
	ReplicatorAnnotation = "replicator.v1.mittwald.de/replicate-to"

	// ReplicatorMatchingAnnotation is the Stakater Replicator annotation for
	// label-selector-based replication.
	ReplicatorMatchingAnnotation = "replicator.v1.mittwald.de/replicate-to-matching"

	envVarsOrgPrefix     = "suparship-envvars-org"
	envVarsEnvPrefix     = "suparship-envvars-env-"
	envVarsProjectPrefix = "suparship-envvars-project-"
)

// OrgEnvConfigMapName returns the ConfigMap name for the Org-level env vars.
func OrgEnvConfigMapName() string { return envVarsOrgPrefix }

// EnvTypeEnvConfigMapName returns the ConfigMap name for an Environment-level
// env var set (keyed by env type: staging, prod, preview).
func EnvTypeEnvConfigMapName(envType string) string {
	return envVarsEnvPrefix + envType
}

// ProjectEnvConfigMapName returns the ConfigMap name for a Project-level env
// var set.
func ProjectEnvConfigMapName(projectName string) string {
	return envVarsProjectPrefix + projectName
}

// UpperLevelEnvWriter writes the Org/Environment/Project runtime ConfigMaps to
// suparship-system with the appropriate Stakater Replicator annotations.
// Only Vars are written to ConfigMaps; SecretRef rendering is handled by the
// GitOps publisher (internal/gitops/eso.go).
type UpperLevelEnvWriter struct {
	client kubernetes.Interface
}

// NewUpperLevelEnvWriter creates an UpperLevelEnvWriter backed by client.
func NewUpperLevelEnvWriter(client kubernetes.Interface) *UpperLevelEnvWriter {
	return &UpperLevelEnvWriter{client: client}
}

// WriteOrgEnvConfig creates or updates the Org-level env var ConfigMap in
// suparship-system. The ConfigMap is annotated to be replicated into all
// namespaces by Stakater Replicator.
// When cfg.Vars is empty the ConfigMap is written with empty data (not deleted)
// so that existing replicas are updated to empty rather than orphaned.
func (w *UpperLevelEnvWriter) WriteOrgEnvConfig(ctx context.Context, cfg EnvConfig) error {
	annotations := map[string]string{
		ReplicatorAnnotation: ".*",
	}
	return w.upsertEnvConfigMap(ctx, OrgEnvConfigMapName(), annotations, cfg.Vars)
}

// WriteEnvTypeEnvConfig creates or updates the Environment-level env var
// ConfigMap for the given env type (e.g. "staging") in suparship-system.
// The ConfigMap is annotated to be replicated to namespaces whose name matches
// ".*-{envType}" (e.g. all staging namespaces follow the {app}-staging pattern).
func (w *UpperLevelEnvWriter) WriteEnvTypeEnvConfig(ctx context.Context, envType string, cfg EnvConfig) error {
	annotations := map[string]string{
		ReplicatorAnnotation: fmt.Sprintf(".*-%s", envType),
	}
	return w.upsertEnvConfigMap(ctx, EnvTypeEnvConfigMapName(envType), annotations, cfg.Vars)
}

// WriteProjectEnvConfig creates or updates the Project-level env var ConfigMap
// in suparship-system. The ConfigMap is annotated to replicate to namespaces
// that carry the label "suparship.io/project={projectName}". App namespaces must
// have this label applied when they are created (see internal/gitops/publisher.go
// namespace generation).
func (w *UpperLevelEnvWriter) WriteProjectEnvConfig(ctx context.Context, projectName string, cfg EnvConfig) error {
	annotations := map[string]string{
		ReplicatorMatchingAnnotation: fmt.Sprintf("suparship.io/project=%s", projectName),
	}
	return w.upsertEnvConfigMap(ctx, ProjectEnvConfigMapName(projectName), annotations, cfg.Vars)
}

// ReadOrgEnvConfig reads the Org-level env var ConfigMap from suparship-system.
// Returns an empty EnvConfig (not an error) when the ConfigMap does not exist.
// Note: only Vars are stored in the ConfigMap; SecretRefs must be loaded from
// the org domain object (rbac.Org.EnvConfig).
func (w *UpperLevelEnvWriter) ReadOrgEnvConfig(ctx context.Context) (EnvConfig, error) {
	return w.readEnvConfigMap(ctx, OrgEnvConfigMapName())
}

// ReadEnvTypeEnvConfig reads the Environment-level env var ConfigMap for the
// given env type. Returns an empty EnvConfig when not found.
func (w *UpperLevelEnvWriter) ReadEnvTypeEnvConfig(ctx context.Context, envType string) (EnvConfig, error) {
	return w.readEnvConfigMap(ctx, EnvTypeEnvConfigMapName(envType))
}

// ReadProjectEnvConfig reads the Project-level env var ConfigMap for the given
// project. Returns an empty EnvConfig when not found.
func (w *UpperLevelEnvWriter) ReadProjectEnvConfig(ctx context.Context, projectName string) (EnvConfig, error) {
	return w.readEnvConfigMap(ctx, ProjectEnvConfigMapName(projectName))
}

// upsertEnvConfigMap creates or updates a ConfigMap in suparship-system.
func (w *UpperLevelEnvWriter) upsertEnvConfigMap(
	ctx context.Context,
	name string,
	annotations map[string]string,
	vars map[string]string,
) error {
	data := make(map[string]string, len(vars))
	for k, v := range vars {
		data[k] = v
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: SystemNamespace,
			Labels: map[string]string{
				"suparship.io/managed-by": "suparship",
				"suparship.io/type":       "envvars",
			},
			Annotations: annotations,
		},
		Data: data,
	}

	existing, err := w.client.CoreV1().ConfigMaps(SystemNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = w.client.CoreV1().ConfigMaps(SystemNamespace).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating env configmap %s: %w", name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading env configmap %s: %w", name, err)
	}

	// Preserve any annotations Replicator may have added (replication status),
	// but always ensure our own annotations are present.
	if existing.Annotations == nil {
		existing.Annotations = make(map[string]string)
	}
	for k, v := range annotations {
		existing.Annotations[k] = v
	}
	existing.Labels = cm.Labels
	existing.Data = cm.Data

	_, err = w.client.CoreV1().ConfigMaps(SystemNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating env configmap %s: %w", name, err)
	}
	return nil
}

// readEnvConfigMap reads a ConfigMap from suparship-system and returns its data
// as an EnvConfig.Vars map. Returns empty EnvConfig when not found.
func (w *UpperLevelEnvWriter) readEnvConfigMap(ctx context.Context, name string) (EnvConfig, error) {
	cm, err := w.client.CoreV1().ConfigMaps(SystemNamespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return EnvConfig{}, nil
	}
	if err != nil {
		return EnvConfig{}, fmt.Errorf("reading env configmap %s: %w", name, err)
	}

	vars := make(map[string]string, len(cm.Data))
	for k, v := range cm.Data {
		vars[k] = v
	}
	if len(vars) == 0 {
		vars = nil
	}
	return EnvConfig{Vars: vars}, nil
}
