package kube

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var kargoProjectConfigGVR = schema.GroupVersionResource{Group: "kargo.akuity.io", Version: "v1alpha1", Resource: "projectconfigs"}

// DownstreamSchedulerReady fails closed until the generated explicit disabled
// policy is live and Kargo has reconciled the target after that configuration.
// Unknown earlier selectors are conservative blockers, never assumed not to match.
func (s *KargoStore) DownstreamSchedulerReady(ctx context.Context, project, app, target string) (bool, error) {
	ns, stageName := kargoNamespaceForProject(project), kargoStageName(app, target)
	config, err := s.dynamic.Resource(kargoProjectConfigGVR).Namespace(ns).Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("read scheduler ownership: %w", err)
	}
	policies, _, err := unstructured.NestedSlice(config.Object, "spec", "promotionPolicies")
	if err != nil {
		return false, err
	}
	disabled := false
	for _, raw := range policies {
		policy, ok := raw.(map[string]any)
		if !ok {
			return false, fmt.Errorf("invalid promotion policy")
		}
		name, _, err := unstructured.NestedString(policy, "stage")
		if err != nil {
			return false, err
		}
		if selector, exists := policy["stageSelector"]; exists {
			fields, ok := selector.(map[string]any)
			if !ok {
				return false, fmt.Errorf("invalid stage selector")
			}
			// Suparship emits exact names. Custom label/pattern selectors require an
			// explicit per-stage policy before them to establish unambiguous ownership.
			if len(fields) != 1 {
				return false, nil
			}
			name, _, err = unstructured.NestedString(fields, "name")
			if err != nil {
				return false, err
			}
		}
		if name == "" || containsPatternPrefix(name) {
			return false, nil
		}
		if name != stageName {
			continue
		}
		enabled, _, err := unstructured.NestedBool(policy, "autoPromotionEnabled")
		if err != nil {
			return false, err
		}
		disabled = !enabled
		break // Kargo uses the first matching policy.
	}
	if !disabled {
		return false, nil
	}
	stage, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).Get(ctx, stageName, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	// ResourceVersion includes changes to policy and survives process restarts.
	token := fmt.Sprintf("suparship-handoff-%x", sha256.Sum256([]byte(string(config.GetUID())+":"+config.GetResourceVersion())))
	handled, _, err := unstructured.NestedString(stage.Object, "status", "lastHandledRefresh")
	if err != nil {
		return false, err
	}
	enabled, _, err := unstructured.NestedBool(stage.Object, "status", "autoPromotionEnabled")
	if err != nil {
		return false, err
	}
	if handled == token && !enabled {
		return true, nil
	}
	if stage.GetAnnotations()["kargo.akuity.io/refresh"] != token {
		patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"resourceVersion": stage.GetResourceVersion(), "annotations": map[string]string{"kargo.akuity.io/refresh": token}}})
		_, err = s.dynamic.Resource(kargoStageGVR).Namespace(ns).Patch(ctx, stageName, types.MergePatchType, patch, metav1.PatchOptions{})
	}
	return false, err
}

func containsPatternPrefix(name string) bool {
	for _, prefix := range []string{"glob:", "regex:", "regexp:"} {
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
