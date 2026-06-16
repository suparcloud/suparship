package kube

import (
	"context"
	"fmt"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/domain"
)

const (
	// templateOverrideType labels the ConfigMaps that store org-level platform
	// value overrides for a template. Deliberately distinct from the template
	// ConfigMap (suparship.io/type=template) so an external template sync, which
	// overwrites the template body, never touches the override.
	templateOverrideType = "template-override"

	// templateOverrideConfigMapPrefix + template name names the override CM.
	templateOverrideConfigMapPrefix = "suparship-template-override-"

	// templateOverrideKey is the data key holding the override YAML.
	templateOverrideKey = "override.yaml"
)

// TemplateOverrideConfigMapName returns the ConfigMap name for a template's
// org-level platform override.
func TemplateOverrideConfigMapName(templateName string) string {
	return templateOverrideConfigMapPrefix + templateName
}

// LoadTemplateOverride reads the org-level platform override for a template.
// Returns (nil, nil) when none exists — callers treat that as "no override".
func LoadTemplateOverride(ctx context.Context, client kubernetes.Interface, templateName string) (*domain.TemplateOverride, error) {
	cm, err := client.CoreV1().ConfigMaps(systemNamespace).Get(
		ctx, TemplateOverrideConfigMapName(templateName), metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get template override %s: %w", templateName, err)
	}
	raw := cm.Data[templateOverrideKey]
	if raw == "" {
		return &domain.TemplateOverride{}, nil
	}
	var ov domain.TemplateOverride
	if err := yaml.Unmarshal([]byte(raw), &ov); err != nil {
		return nil, fmt.Errorf("parse template override %s: %w", templateName, err)
	}
	return &ov, nil
}

// ListTemplateOverrides loads every template override in one List call, keyed by
// template name. Used by display paths (the gallery) that need overrides for many
// templates without an API round-trip per template. Parse failures for a single
// override are skipped (logged by the caller if desired) so one bad CM doesn't
// blank the whole gallery.
func ListTemplateOverrides(ctx context.Context, client kubernetes.Interface) (map[string]*domain.TemplateOverride, error) {
	list, err := client.CoreV1().ConfigMaps(systemNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "suparship.io/type=" + templateOverrideType,
	})
	if err != nil {
		return nil, fmt.Errorf("list template overrides: %w", err)
	}
	out := make(map[string]*domain.TemplateOverride, len(list.Items))
	for i := range list.Items {
		cm := &list.Items[i]
		name := cm.Labels["suparship.io/template-name"]
		if name == "" {
			continue
		}
		var ov domain.TemplateOverride
		if raw := cm.Data[templateOverrideKey]; raw != "" {
			if err := yaml.Unmarshal([]byte(raw), &ov); err != nil {
				continue
			}
		}
		out[name] = &ov
	}
	return out, nil
}

// SaveTemplateOverride upserts a template's org-level platform override. An empty
// override is deleted rather than stored, so "clear all" leaves no stale CM.
func SaveTemplateOverride(ctx context.Context, client kubernetes.Interface, templateName string, ov *domain.TemplateOverride) error {
	if ov.IsEmpty() {
		return DeleteTemplateOverride(ctx, client, templateName)
	}
	data, err := yaml.Marshal(ov)
	if err != nil {
		return fmt.Errorf("marshal template override %s: %w", templateName, err)
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      TemplateOverrideConfigMapName(templateName),
			Namespace: systemNamespace,
			Labels: map[string]string{
				"suparship.io/type":            templateOverrideType,
				"app.kubernetes.io/managed-by": "suparship",
				"suparship.io/template-name":   templateName,
			},
		},
		Data: map[string]string{templateOverrideKey: string(data)},
	}
	cms := client.CoreV1().ConfigMaps(systemNamespace)
	if err := upsertConfigMap(ctx, cms, cm); err != nil {
		return fmt.Errorf("upsert template override %s: %w", templateName, err)
	}
	return nil
}

// DeleteTemplateOverride removes a template's org-level override ConfigMap.
// A missing ConfigMap is not an error (idempotent clear).
func DeleteTemplateOverride(ctx context.Context, client kubernetes.Interface, templateName string) error {
	err := client.CoreV1().ConfigMaps(systemNamespace).Delete(
		ctx, TemplateOverrideConfigMapName(templateName), metav1.DeleteOptions{},
	)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete template override %s: %w", templateName, err)
	}
	return nil
}
