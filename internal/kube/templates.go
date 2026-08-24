package kube

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/tpl"
)

const (
	// templateLabelSelector matches ConfigMaps that store template definitions.
	templateLabelSelector = "suparship.io/type=template"

	// templateConfigMapKey is the data key inside the ConfigMap that holds
	// the raw template.yaml content.
	templateConfigMapKey = tpl.TemplateFileName

	// templateChartBundleKey is the binaryData key inside the template
	// ConfigMap that holds a packaged Helm chart (chart.tgz). Optional —
	// external-mode templates (chart in a Helm registry) don't ship one.
	templateChartBundleKey = "chart.tgz"

	// templateConfigMapPrefix is the "suparship-template-{name}" naming
	// convention shared by all cluster-stored templates.
	templateConfigMapPrefix = "suparship-template-"

	// templateRoleLabel distinguishes the unversioned "current" alias
	// from immutable per-version archives. Lets LoadTemplates skip
	// archives so the gallery doesn't double-count entries.
	templateRoleLabel = "suparship.io/template-role"

	// templateRoleCurrent marks the unversioned ConfigMap (the always-
	// latest alias). Templates persisted before versioned naming was
	// introduced lack this label entirely; LoadTemplates treats absence
	// of the label as "current" for back-compat.
	templateRoleCurrent = "current"

	// templateRoleArchive marks a per-version ConfigMap. Filtered out
	// of LoadTemplates so the gallery doesn't surface old versions.
	templateRoleArchive = "archive"

	// systemNamespace mirrors the namespace constant used by project.K8sStore
	// and preview.K8sStore so all suparship ConfigMaps live together.
	systemNamespace = "suparship-system"
)

// LoadTemplates reads template definitions stored as ConfigMaps in the
// suparship-system namespace and returns them as a validated slice.
//
// Discovery convention:
//
//	apiVersion: v1
//	kind: ConfigMap
//	metadata:
//	  name: suparship-template-web-service
//	  namespace: suparship-system
//	  labels:
//	    suparship.io/type: template
//	data:
//	  template.yaml: |
//	    apiVersion: suparship.io/v1alpha1
//	    kind: Template
//	    ...
//
// ConfigMaps that carry the label but are missing the "template.yaml" key
// are silently skipped; this is defensive against partially created objects.
// A missing label or wrong namespace means the ConfigMap is ignored entirely.
//
// Results are sorted by template name for determinism. An empty cluster (no
// matching ConfigMaps) returns a nil slice without error; the caller should
// treat nil and empty the same way.
//
// TODO: In a future version this will read from a TemplateRegistry CRD
// instead of generic ConfigMaps, enabling versioned template lifecycle
// management (install, upgrade, pin). The function signature will not change.
func LoadTemplates(ctx context.Context, client kubernetes.Interface) ([]*tpl.Template, error) {
	cms, err := client.CoreV1().ConfigMaps(systemNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: templateLabelSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("listing template configmaps in %s: %w", systemNamespace, err)
	}

	templates := make([]*tpl.Template, 0, len(cms.Items))
	for _, cm := range cms.Items {
		// Skip per-version archives — the gallery surfaces one entry per
		// template name (the current alias). ConfigMaps without the role
		// label predate versioned naming and are treated as current.
		if cm.Labels[templateRoleLabel] == templateRoleArchive {
			continue
		}

		data, ok := cm.Data[templateConfigMapKey]
		if !ok {
			// Missing payload key — skip rather than fail. The ConfigMap
			// may have been created by another tool or be mid-reconciliation.
			continue
		}

		t, err := tpl.Parse([]byte(data))
		if err != nil {
			return nil, fmt.Errorf("invalid template in configmap %s: %w", cm.Name, err)
		}
		templates = append(templates, t)
	}

	sort.Slice(templates, func(i, j int) bool {
		return templates[i].Metadata.Name < templates[j].Metadata.Name
	})

	return templates, nil
}

// TemplateConfigMapName returns the well-known ConfigMap name for the
// "current" alias of a template — the always-latest pointer. Used for
// listing and version-blind lookups (today's publisher path).
func TemplateConfigMapName(templateName string) string {
	return templateConfigMapPrefix + templateName
}

// TemplateConfigMapNameVersioned returns the per-version archive
// ConfigMap name for (templateName, version). The archive is immutable
// in spirit: SaveTemplate writes it on each persist, but a future
// version-aware persistence path can refuse to overwrite an existing
// archive whose content differs.
//
// Sanitises the version into a DNS-1123-safe suffix: lowercases the
// version, replaces any disallowed character (e.g. SemVer build-metadata
// `+`) with `-`, trims trailing dashes. The result is appended after a
// dash separator: suparship-template-<name>-<sanitized-version>.
//
// When version is empty, returns the unversioned alias name — callers
// that don't yet pin to a version still get a valid ConfigMap name.
func TemplateConfigMapNameVersioned(templateName, version string) string {
	if version == "" {
		return TemplateConfigMapName(templateName)
	}
	return TemplateConfigMapName(templateName) + "-" + sanitizeVersionForName(version)
}

// sanitizeVersionForName produces a DNS-1123-safe suffix from a SemVer
// string. Common cases: "1.2.0" → "1.2.0", "1.2.0-rc.1" → "1.2.0-rc.1",
// "1.2.0+build.5" → "1.2.0-build.5".
//
// NOTE: gitops.chartVersionDir mirrors this for the version-scoped chart
// directory name; keep the two in sync (they can't share a package — kube
// imports gitops, so gitops can't import kube).
func sanitizeVersionForName(version string) string {
	var b strings.Builder
	b.Grow(len(version))
	for _, r := range strings.ToLower(version) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// SaveTemplate persists a Template (and an optional packaged chart) into
// the suparship-system namespace.
//
// Writes two ConfigMaps so version-aware lookups and version-blind
// lookups both resolve correctly:
//
//  1. The "current" alias at TemplateConfigMapName(name). Always
//     overwritten with the latest content. This is what LoadTemplates
//     surfaces in the gallery and what version-blind LoadChartBundle
//     calls resolve to.
//  2. The per-version archive at TemplateConfigMapNameVersioned(name,
//     version). Lets the publisher resolve a specific pinned version
//     when an app captured `Template.Version` at create time. Today
//     this is overwritten on every save; a future version-aware
//     persistence path can enforce immutability (refuse to overwrite
//     an existing archive whose content differs).
//
// chartTGZ may be nil for templates whose charts ship out-of-band
// (external-mode templates whose chart lives in a Helm registry).
//
// When the template has no Metadata.Version set, only the alias is
// written — there's no meaningful version axis to archive against.
func SaveTemplate(ctx context.Context, client kubernetes.Interface, t *tpl.Template, chartTGZ []byte) error {
	if t == nil {
		return fmt.Errorf("nil template")
	}
	if err := t.Validate(); err != nil {
		return fmt.Errorf("validate template: %w", err)
	}
	yamlBytes, err := tpl.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshal template: %w", err)
	}

	cms := client.CoreV1().ConfigMaps(systemNamespace)

	// 1) Current alias — always latest. Carries the role label so future
	//    operators can tell at a glance which is which.
	aliasCM := buildTemplateCM(
		TemplateConfigMapName(t.Metadata.Name),
		t.Metadata.Name,
		t.Metadata.Version,
		templateRoleCurrent,
		yamlBytes,
		chartTGZ,
	)
	if err := upsertConfigMap(ctx, cms, aliasCM); err != nil {
		return fmt.Errorf("upsert alias configmap: %w", err)
	}

	// 2) Per-version archive — only meaningful when the template
	//    declares a version. Skipping when empty avoids creating a
	//    duplicate of the alias at the same name.
	if t.Metadata.Version == "" {
		return nil
	}
	archiveCM := buildTemplateCM(
		TemplateConfigMapNameVersioned(t.Metadata.Name, t.Metadata.Version),
		t.Metadata.Name,
		t.Metadata.Version,
		templateRoleArchive,
		yamlBytes,
		chartTGZ,
	)
	if err := upsertConfigMap(ctx, cms, archiveCM); err != nil {
		return fmt.Errorf("upsert archive configmap: %w", err)
	}
	return nil
}

// buildTemplateCM constructs the ConfigMap object for either the alias
// or the archive write. Labels carry the template name + version so
// future selectors can address them without parsing the CM name.
func buildTemplateCM(cmName, templateName, version, role string, yamlBytes, chartTGZ []byte) *corev1.ConfigMap {
	labels := map[string]string{
		"suparship.io/type":            "template",
		"app.kubernetes.io/managed-by": "suparship",
		"suparship.io/template-name":   templateName,
		templateRoleLabel:              role,
	}
	if version != "" {
		labels["suparship.io/template-version"] = version
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: systemNamespace,
			Labels:    labels,
		},
		Data: map[string]string{templateConfigMapKey: string(yamlBytes)},
	}
	if len(chartTGZ) > 0 {
		cm.BinaryData = map[string][]byte{templateChartBundleKey: chartTGZ}
	}
	return cm
}

// upsertConfigMap creates or updates a ConfigMap. Mirrors the previous
// inlined Get/Create-or-Update pattern; extracted so SaveTemplate's
// two-write loop reads cleanly.
func upsertConfigMap(ctx context.Context, cms typedConfigMaps, cm *corev1.ConfigMap) error {
	existing, err := cms.Get(ctx, cm.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, createErr := cms.Create(ctx, cm, metav1.CreateOptions{}); createErr != nil {
			return fmt.Errorf("create %s: %w", cm.Name, createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get existing %s: %w", cm.Name, err)
	}
	cm.ResourceVersion = existing.ResourceVersion
	if _, updateErr := cms.Update(ctx, cm, metav1.UpdateOptions{}); updateErr != nil {
		return fmt.Errorf("update %s: %w", cm.Name, updateErr)
	}
	return nil
}

// typedConfigMaps narrows the corev1 ConfigMap interface to the methods
// upsertConfigMap uses. Lets the helper accept the pre-namespaced client
// returned by client.CoreV1().ConfigMaps(ns) without the full corev1
// interface surface.
type typedConfigMaps interface {
	Get(ctx context.Context, name string, opts metav1.GetOptions) (*corev1.ConfigMap, error)
	Create(ctx context.Context, cm *corev1.ConfigMap, opts metav1.CreateOptions) (*corev1.ConfigMap, error)
	Update(ctx context.Context, cm *corev1.ConfigMap, opts metav1.UpdateOptions) (*corev1.ConfigMap, error)
}

// DeleteTemplate removes the cluster ConfigMaps for a template — both
// the alias and any per-version archives. Returns (false, nil) when no
// matching ConfigMaps exist. Other errors propagate so handlers can
// distinguish "not deletable" from "delete failed".
//
// Implemented as List + per-item Delete rather than DeleteCollection
// because the testing/fake clientset's DeleteCollection ignores label
// selectors (silent no-op). Per-template archive counts are small in
// practice (single-digit), so the extra round trips don't matter.
func DeleteTemplate(ctx context.Context, client kubernetes.Interface, templateName string) (bool, error) {
	if templateName == "" {
		return false, fmt.Errorf("template name is required")
	}
	cms := client.CoreV1().ConfigMaps(systemNamespace)

	selector := "suparship.io/template-name=" + templateName
	list, err := cms.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return false, fmt.Errorf("list template configmaps for %s: %w", templateName, err)
	}
	if len(list.Items) == 0 {
		// Back-compat: templates persisted before versioned naming
		// landed lack the template-name label. Try the alias by name
		// directly so DeleteTemplate still works on legacy ConfigMaps.
		legacyName := TemplateConfigMapName(templateName)
		err := cms.Delete(ctx, legacyName, metav1.DeleteOptions{})
		if err == nil {
			return true, nil
		}
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("delete template configmap %s: %w", legacyName, err)
	}

	for _, cm := range list.Items {
		if err := cms.Delete(ctx, cm.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("delete template configmap %s: %w", cm.Name, err)
		}
	}
	return true, nil
}

// LoadChartBundle returns the packaged Helm chart bytes stored alongside the
// template ConfigMap, or nil when the template has no bundle (an
// external-mode template, or one saved without chart bytes). Returns an
// error only on unexpected API failures —
// "ConfigMap not found" is treated as "no bundle" to keep callers simple.
//
// Reads the unversioned alias (always-latest pointer). Callers that need
// a specific pinned version use LoadChartBundleVersion.
func LoadChartBundle(ctx context.Context, client kubernetes.Interface, templateName string) ([]byte, error) {
	cm, err := client.CoreV1().ConfigMaps(systemNamespace).Get(
		ctx, TemplateConfigMapName(templateName), metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get template configmap %s: %w", templateName, err)
	}
	return cm.BinaryData[templateChartBundleKey], nil
}

// TemplateClusterStored reports whether a template is persisted as a cluster
// ConfigMap (imported/synced) rather than shipped on disk (built-in). Used to
// gate in-place metadata edits — disk built-ins have no ConfigMap to update.
func TemplateClusterStored(ctx context.Context, client kubernetes.Interface, templateName string) (bool, error) {
	_, err := client.CoreV1().ConfigMaps(systemNamespace).Get(
		ctx, TemplateConfigMapName(templateName), metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get template configmap %s: %w", templateName, err)
	}
	return true, nil
}

// TemplateVersion describes one persisted version of a template — the
// suparship-template-{name}-{version} archive ConfigMap.
type TemplateVersion struct {
	// Version is the metadata.version declared on the persisted template.yaml.
	Version string
	// CreatedAt is when the archive ConfigMap was first persisted (the
	// ConfigMap's CreationTimestamp). Operators use this to tell archives
	// apart when SemVer alone isn't enough (e.g. pre-release retries).
	CreatedAt string
}

// ListTemplateVersions returns every persisted version of a template by
// listing per-version archive ConfigMaps. The current alias is included
// when its template-version label is set (writes after PR1.4) so the
// caller doesn't have to merge two queries.
//
// Results are returned in the order the API returns them; callers that
// need SemVer ordering sort at their layer.
func ListTemplateVersions(ctx context.Context, client kubernetes.Interface, templateName string) ([]TemplateVersion, error) {
	if templateName == "" {
		return nil, fmt.Errorf("template name is required")
	}
	selector := "suparship.io/template-name=" + templateName
	cms, err := client.CoreV1().ConfigMaps(systemNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		return nil, fmt.Errorf("list template versions for %s: %w", templateName, err)
	}
	out := make([]TemplateVersion, 0, len(cms.Items))
	seen := make(map[string]struct{}, len(cms.Items))
	for _, cm := range cms.Items {
		// The current alias and its archive both carry the same
		// template-version label after PR1.4 — dedupe by version so
		// the caller sees one entry per persisted version.
		v := cm.Labels["suparship.io/template-version"]
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, TemplateVersion{
			Version:   v,
			CreatedAt: cm.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return out, nil
}

// LoadChartBundleVersion returns the chart bytes for a specific pinned
// (templateName, version). Falls back to the unversioned alias when no
// archive exists for that version — this keeps the caller working
// against templates persisted before versioned naming was introduced
// (their ConfigMap is the alias only, with the version label still
// matching). Returns nil bytes (no error) when neither exists.
//
// When version is empty the call collapses to LoadChartBundle.
func LoadChartBundleVersion(ctx context.Context, client kubernetes.Interface, templateName, version string) ([]byte, error) {
	if version == "" {
		return LoadChartBundle(ctx, client, templateName)
	}
	cms := client.CoreV1().ConfigMaps(systemNamespace)
	cm, err := cms.Get(ctx, TemplateConfigMapNameVersioned(templateName, version), metav1.GetOptions{})
	if err == nil {
		return cm.BinaryData[templateChartBundleKey], nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get template configmap %s@%s: %w", templateName, version, err)
	}
	// Archive not present — fall back to the alias. If the alias's
	// declared version matches what was asked, this is the right answer
	// (operator just hasn't re-saved since versioned naming landed). If
	// it doesn't match, the publisher's caller can detect the mismatch
	// from the parsed Template.Metadata.Version.
	return LoadChartBundle(ctx, client, templateName)
}
