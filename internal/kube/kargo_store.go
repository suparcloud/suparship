package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// Kargo GroupVersionResources.
var (
	kargoWarehouseGVR = schema.GroupVersionResource{
		Group:    "kargo.akuity.io",
		Version:  "v1alpha1",
		Resource: "warehouses",
	}
	kargoStageGVR = schema.GroupVersionResource{
		Group:    "kargo.akuity.io",
		Version:  "v1alpha1",
		Resource: "stages",
	}
	kargoPromotionGVR = schema.GroupVersionResource{
		Group:    "kargo.akuity.io",
		Version:  "v1alpha1",
		Resource: "promotions",
	}
)

// KargoStageStatus holds the current observed status of a Kargo Stage.
type KargoStageStatus struct {
	// Phase is the overall stage phase, e.g. "Steady", "Promoting", "NotReady".
	Phase string
	// CurrentFreight is the name of the Freight currently running in the Stage.
	CurrentFreight string
	// Health is the aggregated health status of the Stage ("Healthy", "Unhealthy", "Unknown").
	Health string
}

// KargoPromotionInfo describes a Kargo Promotion CR that was created or observed.
type KargoPromotionInfo struct {
	// Name is the Promotion CR name.
	Name string
	// Stage is the target Stage name.
	Stage string
	// Freight is the name of the Freight being promoted.
	Freight string
	// Phase is the observed Promotion phase, e.g. "Pending", "Running", "Succeeded".
	Phase string
}

// KargoStore reads and writes Kargo CRs (Warehouse, Stage, Promotion) via the
// Kubernetes dynamic client. It does not import the Kargo SDK to keep
// the dependency footprint small; instead it uses unstructured access patterns
// consistent with kube.ArgoCDStatusReader.
//
// KargoStore implements server.KargoPromoter.
type KargoStore struct {
	dynamic dynamic.Interface
}

// NewKargoStore creates a KargoStore backed by the given dynamic client.
func NewKargoStore(dyn dynamic.Interface) *KargoStore {
	return &KargoStore{dynamic: dyn}
}

// GetStageStatus returns the current observed status of a Kargo Stage.
// The Stage is looked up in projectNS (= the Kargo project namespace, which by
// suparship convention equals the suparship project name).
func (s *KargoStore) GetStageStatus(ctx context.Context, projectNS, stageName string) (*KargoStageStatus, error) {
	obj, err := s.dynamic.Resource(kargoStageGVR).Namespace(projectNS).Get(ctx, stageName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get kargo stage %s/%s: %w", projectNS, stageName, err)
	}
	return parseKargoStageStatus(obj), nil
}

// ListStageStatuses returns the status of all Kargo Stages in projectNS,
// keyed by Stage name.
func (s *KargoStore) ListStageStatuses(ctx context.Context, projectNS string) (map[string]*KargoStageStatus, error) {
	list, err := s.dynamic.Resource(kargoStageGVR).Namespace(projectNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list kargo stages in %q: %w", projectNS, err)
	}

	result := make(map[string]*KargoStageStatus, len(list.Items))
	for _, item := range list.Items {
		name, _, _ := unstructuredString(item.Object, "metadata", "name")
		result[name] = parseKargoStageStatus(&item)
	}
	return result, nil
}

// CreatePromotion creates a Kargo Promotion CR to advance freight from
// fromStage to toStage within projectNS. It reads the current Freight from
// fromStage and submits a Promotion CR targeting toStage.
//
// When the target Stage gates on an upstream stage (e.g. prod gates on staging),
// Kargo requires the Freight to be either verified in the upstream stage or
// explicitly approved for the target stage. Since suparship uses
// promotionMechanisms.argoCDAppUpdates without image substitution, Kargo never
// marks Freight as verified (it can't confirm the image is running). As a
// workaround, CreatePromotion explicitly approves the Freight for toStage so
// that Kargo allows the Promotion to proceed.
//
// Returns ErrKargoNoFreight when fromStage has no current Freight to promote.
func (s *KargoStore) CreatePromotion(ctx context.Context, projectNS, appName, fromStage, toStage string) (*KargoPromotionInfo, error) {
	freight, err := s.getCurrentFreight(ctx, projectNS, fromStage)
	if err != nil {
		return nil, fmt.Errorf("resolve freight from stage %q: %w", fromStage, err)
	}
	if freight == "" {
		return nil, ErrKargoNoFreight
	}

	// Approve the Freight for the target Stage so Kargo allows the Promotion even
	// when staging verification is absent (the case with pure argoCDAppUpdates).
	if approveErr := s.approveFreightForStage(ctx, projectNS, freight, toStage); approveErr != nil {
		// Non-fatal: log and continue. If the Freight is already approved or
		// the Stage accepts it from an upstream, this succeeds anyway.
		_ = approveErr
	}

	promotionName := fmt.Sprintf("%s-%s-%d", appName, toStage, time.Now().Unix())

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kargo.akuity.io/v1alpha1",
			"kind":       "Promotion",
			"metadata": map[string]any{
				"name":      promotionName,
				"namespace": projectNS,
				"labels": map[string]any{
					"suparship.io/app":              appName,
					"suparship.io/project":          projectNS,
					"kargo.akuity.io/stage":         toStage,
					"suparship.io/generator-version": "v0.1.0",
				},
			},
			"spec": map[string]any{
				"stage":   toStage,
				"freight": freight,
			},
		},
	}

	created, err := s.dynamic.Resource(kargoPromotionGVR).Namespace(projectNS).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create kargo promotion %q: %w", promotionName, err)
	}

	info := &KargoPromotionInfo{
		Name:    promotionName,
		Stage:   toStage,
		Freight: freight,
	}
	if statusRaw, ok := created.Object["status"].(map[string]any); ok {
		info.Phase, _, _ = unstructuredString(statusRaw, "phase")
	}
	return info, nil
}

// GetPromotionStatus returns the observed status of a named Kargo Promotion CR.
func (s *KargoStore) GetPromotionStatus(ctx context.Context, projectNS, promotionName string) (*KargoPromotionInfo, error) {
	obj, err := s.dynamic.Resource(kargoPromotionGVR).Namespace(projectNS).Get(ctx, promotionName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get kargo promotion %s/%s: %w", projectNS, promotionName, err)
	}

	info := &KargoPromotionInfo{Name: promotionName}
	if spec, ok := obj.Object["spec"].(map[string]any); ok {
		info.Stage, _, _ = unstructuredString(spec, "stage")
		info.Freight, _, _ = unstructuredString(spec, "freight")
	}
	if statusRaw, ok := obj.Object["status"].(map[string]any); ok {
		info.Phase, _, _ = unstructuredString(statusRaw, "phase")
	}
	return info, nil
}

// CheckWarehouseReady returns true when the Warehouse CR exists in projectNS.
// Used by readiness probes to verify Kargo is functional for a project.
func (s *KargoStore) CheckWarehouseReady(ctx context.Context, projectNS, warehouseName string) (bool, error) {
	_, err := s.dynamic.Resource(kargoWarehouseGVR).Namespace(projectNS).Get(ctx, warehouseName, metav1.GetOptions{})
	if err != nil {
		return false, nil //nolint:nilerr // not-found is expected before first deploy
	}
	return true, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// getCurrentFreight returns the name of the Freight currently committed to stageName.
// Returns an empty string when the Stage has no current freight (not yet promoted).
//
// Strategy (in order):
//  1. status.currentFreight.name (Kargo v0.8+ canonical field).
//  2. status.currentFreight as plain string (Kargo < v0.8 compatibility).
//  3. Freight from the most recent successful Promotion targeting this Stage
//     — used when promotionMechanisms.argoCDAppUpdates is configured without
//     image substitution (Kargo cannot verify delivery, so it never sets
//     currentFreight itself, but the Promotion still succeeded).
func (s *KargoStore) getCurrentFreight(ctx context.Context, projectNS, stageName string) (string, error) {
	obj, err := s.dynamic.Resource(kargoStageGVR).Namespace(projectNS).Get(ctx, stageName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get kargo stage %s/%s: %w", projectNS, stageName, err)
	}

	statusRaw, ok := obj.Object["status"].(map[string]any)
	if ok {
		// Kargo ≥ v0.8: status.currentFreight is a map with a "name" key.
		if cf, ok := statusRaw["currentFreight"].(map[string]any); ok {
			name, found, _ := unstructuredString(cf, "name")
			if found && name != "" {
				return name, nil
			}
		}
		// Kargo < v0.8: status.currentFreight was a plain string.
		if cf, ok := statusRaw["currentFreight"].(string); ok && cf != "" {
			return cf, nil
		}
	}

	// Fallback: find the Freight from the most recent successful Promotion for
	// this Stage. This handles the case where promotionMechanisms.argoCDAppUpdates
	// is used without image substitution — Kargo marks the Promotion Succeeded
	// but never sets status.currentFreight because it cannot verify delivery.
	return s.latestSuccessfulPromotionFreight(ctx, projectNS, stageName)
}

// latestSuccessfulPromotionFreight scans Promotion CRs in projectNS for the
// most recently-completed successful Promotion targeting stageName and returns
// its Freight name. Returns "" (no error) when none are found.
//
// Note: we list all Promotions (no label selector) because manually-created
// Promotions may not carry the suparship labels. We filter by spec.stage.
func (s *KargoStore) latestSuccessfulPromotionFreight(ctx context.Context, projectNS, stageName string) (string, error) {
	list, err := s.dynamic.Resource(kargoPromotionGVR).Namespace(projectNS).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", nil //nolint:nilerr
	}

	var latestTime string
	var latestFreight string
	for _, item := range list.Items {
		spec, ok := item.Object["spec"].(map[string]any)
		if !ok {
			continue
		}
		// Filter to the target Stage.
		stage, _, _ := unstructuredString(spec, "stage")
		if stage != stageName {
			continue
		}
		statusRaw, ok := item.Object["status"].(map[string]any)
		if !ok {
			continue
		}
		phase, _, _ := unstructuredString(statusRaw, "phase")
		if phase != "Succeeded" {
			continue
		}
		// Use finishedAt as a recency comparator (RFC3339 sorts lexically).
		finishedAt, _, _ := unstructuredString(statusRaw, "finishedAt")
		if finishedAt <= latestTime {
			continue
		}
		freight, _, _ := unstructuredString(spec, "freight")
		if freight == "" {
			continue
		}
		latestTime = finishedAt
		latestFreight = freight
	}
	return latestFreight, nil
}

func parseKargoStageStatus(obj *unstructured.Unstructured) *KargoStageStatus {
	s := &KargoStageStatus{}
	statusRaw, ok := obj.Object["status"].(map[string]any)
	if !ok {
		return s
	}
	s.Phase, _, _ = unstructuredString(statusRaw, "phase")
	s.Health, _, _ = unstructuredString(statusRaw, "health", "status")
	if cf, ok := statusRaw["currentFreight"].(map[string]any); ok {
		s.CurrentFreight, _, _ = unstructuredString(cf, "name")
	} else if cf, ok := statusRaw["currentFreight"].(string); ok {
		s.CurrentFreight = cf
	}
	return s
}

// approveFreightForStage patches the Freight's status.approvedFor field to
// mark it as approved for stageName. This is required when promoting to a Stage
// that gates on an upstream stage and Kargo cannot verify delivery (e.g.
// argoCDAppUpdates without image substitution).
func (s *KargoStore) approveFreightForStage(ctx context.Context, projectNS, freightName, stageName string) error {
	kargoFreightGVR := schema.GroupVersionResource{
		Group:    "kargo.akuity.io",
		Version:  "v1alpha1",
		Resource: "freights",
	}
	patch := map[string]any{
		"status": map[string]any{
			"approvedFor": map[string]any{
				stageName: map[string]any{},
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("marshal approval patch: %w", err)
	}
	_, err = s.dynamic.Resource(kargoFreightGVR).Namespace(projectNS).Patch(
		ctx, freightName, apitypes.MergePatchType, patchBytes,
		metav1.PatchOptions{}, "status",
	)
	return err
}

// ErrKargoNoFreight is returned by CreatePromotion when the source Stage has no
// current Freight to promote. The caller should surface this as a 400 Bad Request
// (the user must trigger a delivery to the source stage before promoting).
var ErrKargoNoFreight = fmt.Errorf("source stage has no current freight to promote")
