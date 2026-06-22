package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// Well-known suparship identity labels stamped on generated Kargo CRs. Kept in
// sync with internal/gitops (which owns CR generation); duplicated here as
// string literals to avoid a kube→gitops import. Used to scope list queries to a
// single app within a project's Kargo namespace.
const (
	labelKargoStageApp = "suparship.io/app"

	// kargoNamespacePrefix mirrors gitops.KargoNamespaceForProject (duplicated to
	// avoid a kube→gitops import): each suparship project's Kargo CRs live in the
	// namespace "kargo-{project}".
	kargoNamespacePrefix = "kargo-"
)

// kargoNamespaceForProject returns the Kargo Project namespace for a suparship
// project. Mirrors gitops.KargoNamespaceForProject.
func kargoNamespaceForProject(projectName string) string {
	return kargoNamespacePrefix + projectName
}

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
	// AvailableFreightCount is the number of Freight items that are available
	// for promotion into this Stage but have not yet been promoted. A value > 0
	// means Kargo has detected new images/commits that are ready to promote.
	AvailableFreightCount int
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
//
// Each suparship project's Kargo CRs live in its own kargo-{project} namespace on
// the tooling cluster (Kargo binds a Project 1:1 to a namespace). Methods take a
// projectName and resolve the namespace via kargoNamespaceForProject; resource
// names within a project namespace are unqualified ("{app}", "{app}-{env}").
type KargoStore struct {
	dynamic dynamic.Interface
}

// NewKargoStore creates a KargoStore backed by the given dynamic client. The
// per-project Kargo namespace is resolved from the projectName passed to each
// method.
func NewKargoStore(dyn dynamic.Interface) *KargoStore {
	return &KargoStore{dynamic: dyn}
}

// GetStageStatus returns the current observed status of a Kargo Stage in the
// the project's kargo-{project} namespace. stageName is the Stage name
// ("{app}-{env}").
func (s *KargoStore) GetStageStatus(ctx context.Context, projectName, stageName string) (*KargoStageStatus, error) {
	ns := kargoNamespaceForProject(projectName)
	obj, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).Get(ctx, stageName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get kargo stage %s/%s: %w", ns, stageName, err)
	}
	status := parseKargoStageStatus(obj)
	slog.Debug("kargo stage status",
		"namespace", ns,
		"stage", stageName,
		"phase", status.Phase,
		"health", status.Health,
		"currentFreight", status.CurrentFreight,
		"availableFreightCount", status.AvailableFreightCount,
	)
	return status, nil
}

// ListAppStageStatuses returns the status of the Kargo Stages belonging to one
// app, keyed by Stage name. The project's kargo-{project} namespace isolates the
// project; the query is further scoped to the app by the stamped suparship.io/app
// label (so a sibling app whose name extends this one isn't matched by prefix).
func (s *KargoStore) ListAppStageStatuses(ctx context.Context, projectName, appName string) (map[string]*KargoStageStatus, error) {
	ns := kargoNamespaceForProject(projectName)
	selector := fmt.Sprintf("%s=%s", labelKargoStageApp, appName)
	list, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, fmt.Errorf("list kargo stages in %q (%s): %w", ns, selector, err)
	}

	result := make(map[string]*KargoStageStatus, len(list.Items))
	for _, item := range list.Items {
		name, _, _ := unstructuredString(item.Object, "metadata", "name")
		st := parseKargoStageStatus(&item)
		result[name] = st
		slog.Debug("kargo stage listed",
			"namespace", ns,
			"stage", name,
			"phase", st.Phase,
			"health", st.Health,
			"currentFreight", st.CurrentFreight,
			"availableFreightCount", st.AvailableFreightCount,
		)
	}
	slog.Debug("kargo stages listed", "namespace", ns, "project", projectName, "app", appName, "count", len(result))
	return result, nil
}

// CreatePromotion creates a Kargo Promotion CR to advance freight from
// fromStage to toStage for projectName/appName. It reads the current Freight
// from fromStage and submits a Promotion CR targeting toStage in the shared
// Kargo namespace.
//
// fromStage and toStage are suparship environment names (e.g. "staging", "prod").
// CreatePromotion converts them to Kargo Stage names using the "{app}-{env}"
// convention (e.g. "color-app-staging") within the project's kargo-{project}
// namespace.
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
func (s *KargoStore) CreatePromotion(ctx context.Context, projectName, appName, fromStage, toStage string) (*KargoPromotionInfo, error) {
	ns := kargoNamespaceForProject(projectName)
	// Kargo Stage names follow the "{app}-{env}" convention within the project ns.
	qualifiedFromStage := kargoStageName(appName, fromStage)
	qualifiedToStage := kargoStageName(appName, toStage)

	slog.Debug("kargo create promotion: resolving freight",
		"namespace", ns,
		"project", projectName,
		"app", appName,
		"fromStage", qualifiedFromStage,
		"toStage", qualifiedToStage,
	)

	freight, err := s.getCurrentFreight(ctx, ns, qualifiedFromStage)
	if err != nil {
		return nil, fmt.Errorf("resolve freight from stage %q: %w", qualifiedFromStage, err)
	}
	if freight == "" {
		slog.Debug("kargo create promotion: no current freight in source stage",
			"namespace", ns,
			"fromStage", qualifiedFromStage,
		)
		return nil, ErrKargoNoFreight
	}

	slog.Debug("kargo create promotion: freight resolved",
		"namespace", ns,
		"fromStage", qualifiedFromStage,
		"freight", freight,
	)

	// Approve the Freight for the target Stage so Kargo allows the Promotion even
	// when staging verification is absent (the case with pure argoCDAppUpdates).
	if approveErr := s.approveFreightForStage(ctx, ns, freight, qualifiedToStage); approveErr != nil {
		// Non-fatal: log and continue. If the Freight is already approved or
		// the Stage accepts it from an upstream, this succeeds anyway.
		slog.Debug("kargo freight approval failed (non-fatal, continuing)",
			"namespace", ns,
			"freight", freight,
			"stage", qualifiedToStage,
			"error", approveErr,
		)
	} else {
		slog.Debug("kargo freight approved for target stage",
			"namespace", ns,
			"freight", freight,
			"stage", qualifiedToStage,
		)
	}

	// Kargo v1.x requires every Promotion to carry its own spec.steps. The Kargo
	// API server copies them from the target Stage's promotionTemplate when
	// promoting via its UI/CLI, but a Promotion created directly through the
	// Kubernetes API (as we do here) receives no such defaulting — the admission
	// webhook then rejects it with "Stage ... defines no promotion steps". So we
	// read the live Stage's promotionTemplate steps and embed them ourselves.
	steps, err := s.stagePromotionSteps(ctx, ns, qualifiedToStage)
	if err != nil {
		return nil, fmt.Errorf("resolve promotion steps for stage %q: %w", qualifiedToStage, err)
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("target stage %q defines no promotion steps (spec.promotionTemplate.spec.steps is empty) — re-publish the app's Kargo CRs", qualifiedToStage)
	}

	promotionName := fmt.Sprintf("%s-%s-%d", appName, toStage, time.Now().Unix())

	slog.Debug("kargo create promotion: submitting Promotion CR",
		"namespace", ns,
		"promotion", promotionName,
		"stage", qualifiedToStage,
		"freight", freight,
		"steps", len(steps),
	)

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kargo.akuity.io/v1alpha1",
			"kind":       "Promotion",
			"metadata": map[string]any{
				"name":      promotionName,
				"namespace": ns,
				"labels": map[string]any{
					"suparship.io/app":               appName,
					"suparship.io/project":           projectName,
					"kargo.akuity.io/stage":          qualifiedToStage,
					"suparship.io/generator-version": "v0.1.0",
				},
			},
			"spec": map[string]any{
				"stage":   qualifiedToStage,
				"freight": freight,
				"steps":   steps,
			},
		},
	}

	created, err := s.dynamic.Resource(kargoPromotionGVR).Namespace(ns).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("create kargo promotion %q: %w", promotionName, err)
	}

	info := &KargoPromotionInfo{
		Name:    promotionName,
		Stage:   qualifiedToStage,
		Freight: freight,
	}
	if statusRaw, ok := created.Object["status"].(map[string]any); ok {
		info.Phase, _, _ = unstructuredString(statusRaw, "phase")
	}
	slog.Debug("kargo promotion CR created",
		"namespace", ns,
		"promotion", promotionName,
		"stage", qualifiedToStage,
		"freight", freight,
		"initialPhase", info.Phase,
	)
	return info, nil
}

// GetPromotionStatus returns the observed status of a named Kargo Promotion CR
// in the project's kargo-{project} namespace.
func (s *KargoStore) GetPromotionStatus(ctx context.Context, projectName, promotionName string) (*KargoPromotionInfo, error) {
	ns := kargoNamespaceForProject(projectName)
	obj, err := s.dynamic.Resource(kargoPromotionGVR).Namespace(ns).Get(ctx, promotionName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get kargo promotion %s/%s: %w", ns, promotionName, err)
	}

	info := &KargoPromotionInfo{Name: promotionName}
	if spec, ok := obj.Object["spec"].(map[string]any); ok {
		info.Stage, _, _ = unstructuredString(spec, "stage")
		info.Freight, _, _ = unstructuredString(spec, "freight")
	}
	if statusRaw, ok := obj.Object["status"].(map[string]any); ok {
		info.Phase, _, _ = unstructuredString(statusRaw, "phase")
	}
	slog.Debug("kargo promotion status",
		"namespace", ns,
		"promotion", promotionName,
		"stage", info.Stage,
		"freight", info.Freight,
		"phase", info.Phase,
	)
	return info, nil
}

// CheckWarehouseReady returns true when the Warehouse CR exists in the project's
// kargo-{project} namespace. Used by readiness probes to verify Kargo is
// functional. warehouseName is the Warehouse name ("{app}").
func (s *KargoStore) CheckWarehouseReady(ctx context.Context, projectName, warehouseName string) (bool, error) {
	ns := kargoNamespaceForProject(projectName)
	_, err := s.dynamic.Resource(kargoWarehouseGVR).Namespace(ns).Get(ctx, warehouseName, metav1.GetOptions{})
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
func (s *KargoStore) getCurrentFreight(ctx context.Context, ns, stageName string) (string, error) {
	obj, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).Get(ctx, stageName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get kargo stage %s/%s: %w", ns, stageName, err)
	}

	statusRaw, ok := obj.Object["status"].(map[string]any)
	if ok {
		// Kargo v1.x: status.freightHistory is newest-first; the current Freight
		// is the most recent collection's item.
		if name := freightNameFromHistory(statusRaw); name != "" {
			slog.Debug("kargo current freight resolved (v1.x freightHistory)",
				"namespace", ns, "stage", stageName, "freight", name)
			return name, nil
		}
		// Kargo ≥ v0.8: status.currentFreight is a map with a "name" key.
		if cf, ok := statusRaw["currentFreight"].(map[string]any); ok {
			name, found, _ := unstructuredString(cf, "name")
			if found && name != "" {
				slog.Debug("kargo current freight resolved (v0.8+ field)",
					"namespace", ns, "stage", stageName, "freight", name)
				return name, nil
			}
		}
		// Kargo < v0.8: status.currentFreight was a plain string.
		if cf, ok := statusRaw["currentFreight"].(string); ok && cf != "" {
			slog.Debug("kargo current freight resolved (legacy string field)",
				"namespace", ns, "stage", stageName, "freight", cf)
			return cf, nil
		}
	}

	slog.Debug("kargo current freight not in stage status — falling back to latest successful promotion",
		"namespace", ns, "stage", stageName)
	// Fallback: find the Freight from the most recent successful Promotion for
	// this Stage. This handles the case where promotionMechanisms.argoCDAppUpdates
	// is used without image substitution — Kargo marks the Promotion Succeeded
	// but never sets status.currentFreight because it cannot verify delivery.
	return s.latestSuccessfulPromotionFreight(ctx, ns, stageName)
}

// stagePromotionSteps returns the promotion steps defined on the target Stage's
// spec.promotionTemplate.spec.steps (Kargo v1.x). These must be embedded in the
// Promotion CR we create directly via the Kubernetes API, because — unlike the
// Kargo API server — a raw dynamic-client create gets no step defaulting from
// the webhook. Returns a nil slice (no error) when the Stage defines no steps,
// which CreatePromotion treats as a fail-fast condition.
//
// The returned slice is a deep copy (via unstructured.NestedSlice) of
// dynamic-client-safe values, so it can be assigned straight into a new
// Promotion's spec.
func (s *KargoStore) stagePromotionSteps(ctx context.Context, ns, stageName string) ([]any, error) {
	obj, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).Get(ctx, stageName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get kargo stage %s/%s: %w", ns, stageName, err)
	}
	steps, found, err := unstructured.NestedSlice(obj.Object, "spec", "promotionTemplate", "spec", "steps")
	if err != nil {
		return nil, fmt.Errorf("read promotion steps from stage %s/%s: %w", ns, stageName, err)
	}
	if !found {
		return nil, nil
	}
	return steps, nil
}

// latestSuccessfulPromotionFreight scans Promotion CRs in the project's
// kargo-{project} namespace for the most recently-completed successful Promotion
// targeting stageName and returns its Freight name. Returns "" (no error) when
// none are found.
//
// Note: we list all Promotions (no label selector) because manually-created
// Promotions may not carry the suparship labels. We filter by spec.stage (unique
// within the project namespace).
func (s *KargoStore) latestSuccessfulPromotionFreight(ctx context.Context, ns, stageName string) (string, error) {
	list, err := s.dynamic.Resource(kargoPromotionGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
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
	if latestFreight != "" {
		slog.Debug("kargo current freight resolved (fallback: latest successful promotion)",
			"namespace", ns,
			"stage", stageName,
			"freight", latestFreight,
			"finishedAt", latestTime,
		)
	} else {
		slog.Debug("kargo current freight: no successful promotions found for stage",
			"namespace", ns,
			"stage", stageName,
		)
	}
	return latestFreight, nil
}

// freightNameFromHistory extracts the most recent Freight name from a Kargo v1.x
// Stage status. status.freightHistory is newest-first; each collection's "items"
// map is keyed by warehouse, with a "name" per Freight. For a single-warehouse
// stage there's one item; keys are sorted for deterministic selection.
func freightNameFromHistory(statusRaw map[string]any) string {
	history, ok := statusRaw["freightHistory"].([]any)
	if !ok || len(history) == 0 {
		return ""
	}
	coll, ok := history[0].(map[string]any)
	if !ok {
		return ""
	}
	items, ok := coll["items"].(map[string]any)
	if !ok {
		return ""
	}
	keys := make([]string, 0, len(items))
	for k := range items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if item, ok := items[k].(map[string]any); ok {
			if name, _, _ := unstructuredString(item, "name"); name != "" {
				return name
			}
		}
	}
	return ""
}

func parseKargoStageStatus(obj *unstructured.Unstructured) *KargoStageStatus {
	s := &KargoStageStatus{}
	statusRaw, ok := obj.Object["status"].(map[string]any)
	if !ok {
		return s
	}
	s.Phase, _, _ = unstructuredString(statusRaw, "phase")
	s.Health, _, _ = unstructuredString(statusRaw, "health", "status")
	// Kargo v1.x: current Freight comes from freightHistory; older versions
	// exposed status.currentFreight (map or string).
	if name := freightNameFromHistory(statusRaw); name != "" {
		s.CurrentFreight = name
	} else if cf, ok := statusRaw["currentFreight"].(map[string]any); ok {
		s.CurrentFreight, _, _ = unstructuredString(cf, "name")
	} else if cf, ok := statusRaw["currentFreight"].(string); ok {
		s.CurrentFreight = cf
	}

	// Count available freights (freights detected by Kargo but not yet promoted).
	// Kargo < v1 stored these in status.availableFreights; v1.x does not surface a
	// count here, so this degrades to 0 (display-only).
	if available, ok := statusRaw["availableFreights"].([]any); ok {
		s.AvailableFreightCount = len(available)
	}

	return s
}

// approveFreightForStage patches the Freight's status.approvedFor field to
// mark it as approved for stageName. This is required when promoting to a Stage
// that gates on an upstream stage and Kargo cannot verify delivery (e.g.
// argoCDAppUpdates without image substitution).
func (s *KargoStore) approveFreightForStage(ctx context.Context, ns, freightName, stageName string) error {
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
	_, err = s.dynamic.Resource(kargoFreightGVR).Namespace(ns).Patch(
		ctx, freightName, apitypes.MergePatchType, patchBytes,
		metav1.PatchOptions{}, "status",
	)
	return err
}

// ErrKargoNoFreight is returned by CreatePromotion when the source Stage has no
// current Freight to promote. The caller should surface this as a 400 Bad Request
// (the user must trigger a delivery to the source stage before promoting).
var ErrKargoNoFreight = fmt.Errorf("source stage has no current freight to promote")

// kargoStageName returns the Kargo Stage name for an app environment. Stages live
// in the per-project kargo-{project} namespace, so the name is "{app}-{env}".
// Mirrors gitops.KargoStageName (kept local to avoid a kube→gitops import).
func kargoStageName(appName, envName string) string {
	return appName + "-" + envName
}
