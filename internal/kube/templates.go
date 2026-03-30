package kube

import (
	"context"
	"fmt"
	"sort"

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
