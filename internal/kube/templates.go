package kube

import (
	"context"
	"fmt"
	"sort"

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
	// templates whose charts come from SUPARSHIP_TEMPLATES_DIR don't ship one.
	templateChartBundleKey = "chart.tgz"

	// templateConfigMapPrefix is the "suparship-template-{name}" naming
	// convention shared by all cluster-stored templates.
	templateConfigMapPrefix = "suparship-template-"

	// systemNamespace mirrors the namespace constant used by project.K8sStore
	// and preview.K8sStore so all suparShip ConfigMaps live together.
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

// TemplateConfigMapName returns the well-known ConfigMap name for a template
// stored in the cluster. Exposed so handlers and the publisher fallback
// agree on the lookup key.
func TemplateConfigMapName(templateName string) string {
	return templateConfigMapPrefix + templateName
}

// SaveTemplate persists a Template (and an optional packaged chart) into the
// suparship-system namespace as a ConfigMap, replacing any existing entry
// with the same name. The chart bytes are stored as binaryData["chart.tgz"]
// — Kubernetes binaryData has a 1 MiB combined limit per ConfigMap.
//
// chartTGZ may be nil for templates whose charts ship out-of-band (e.g.
// pre-existing built-ins loaded from SUPARSHIP_TEMPLATES_DIR).
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

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TemplateConfigMapName(t.Metadata.Name),
			Namespace: systemNamespace,
			Labels: map[string]string{
				"suparship.io/type":             "template",
				"app.kubernetes.io/managed-by": "suparship",
			},
		},
		Data: map[string]string{templateConfigMapKey: string(yamlBytes)},
	}
	if len(chartTGZ) > 0 {
		cm.BinaryData = map[string][]byte{templateChartBundleKey: chartTGZ}
	}

	cms := client.CoreV1().ConfigMaps(systemNamespace)
	existing, err := cms.Get(ctx, cm.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, createErr := cms.Create(ctx, cm, metav1.CreateOptions{}); createErr != nil {
			return fmt.Errorf("create template configmap: %w", createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get existing template configmap: %w", err)
	}
	cm.ResourceVersion = existing.ResourceVersion
	if _, updateErr := cms.Update(ctx, cm, metav1.UpdateOptions{}); updateErr != nil {
		return fmt.Errorf("update template configmap: %w", updateErr)
	}
	return nil
}

// DeleteTemplate removes the cluster ConfigMap for a template. Returns
// (false, nil) when the template ConfigMap doesn't exist — built-in
// templates loaded from --templates-dir live on disk, not in the cluster,
// and have nothing to remove. Other errors propagate so handlers can
// distinguish "not deletable" from "delete failed".
func DeleteTemplate(ctx context.Context, client kubernetes.Interface, templateName string) (bool, error) {
	if templateName == "" {
		return false, fmt.Errorf("template name is required")
	}
	name := TemplateConfigMapName(templateName)
	err := client.CoreV1().ConfigMaps(systemNamespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("delete template configmap %s: %w", name, err)
}

// LoadChartBundle returns the packaged Helm chart bytes stored alongside the
// template ConfigMap, or nil when the template has no bundle (i.e. chart is
// shipped via SUPARSHIP_TEMPLATES_DIR or hasn't been imported through the
// BYO-chart flow). Returns an error only on unexpected API failures —
// "ConfigMap not found" is treated as "no bundle" to keep callers simple.
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
