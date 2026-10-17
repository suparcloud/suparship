package kube

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var ErrPromotionBusy = errors.New("another promotion is active; retry after it finishes")

var ErrReleaseNotVerified = errors.New("release is not eligible for promotion")

type VerifiedReleaseCandidate struct {
	Name      string              `json:"name"`
	CreatedAt time.Time           `json:"createdAt"`
	Images    []KargoFreightImage `json:"images"`
}

// PromoteVerifiedRelease promotes historical freight without modifying its approval
// status or the source Stage. Kargo remains responsible for execution-time checks,
// including soak time, and for the destination Git write and Argo CD rollout.
func (s *KargoStore) PromoteVerifiedRelease(ctx context.Context, project, app, source, target, freight string) (*KargoPromotionInfo, error) {
	if freight == "" {
		var err error
		freight, err = s.getCurrentFreight(ctx, kargoNamespaceForProject(project), kargoStageName(app, source))
		if err != nil {
			return nil, err
		}
		if freight == "" {
			return nil, ErrKargoNoFreight
		}
	}
	if err := s.validateVerifiedRelease(ctx, project, app, source, target, freight); err != nil {
		return nil, err
	}
	ns := kargoNamespaceForProject(project)
	targetName := kargoStageName(app, target)

	name, active, err := s.verifiedPromotionAttempt(ctx, ns, targetName, freight)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return active, nil
	}
	return s.submitPromotion(ctx, ns, project, app, target, targetName, freight, name)
}

// ResolveVerifiedRelease validates before any target preparation is written.
func (s *KargoStore) ResolveVerifiedRelease(ctx context.Context, project, app, source, target, freight string) (string, error) {
	if freight == "" {
		var err error
		freight, err = s.getCurrentFreight(ctx, kargoNamespaceForProject(project), kargoStageName(app, source))
		if err != nil {
			return "", err
		}
		if freight == "" {
			return "", ErrKargoNoFreight
		}
	}
	return freight, s.validateVerifiedRelease(ctx, project, app, source, target, freight)
}

func (s *KargoStore) validateVerifiedRelease(ctx context.Context, project, app, source, target, freight string) error {
	ns := kargoNamespaceForProject(project)
	sourceName, targetName := kargoStageName(app, source), kargoStageName(app, target)
	stage, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).Get(ctx, targetName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read target stage: %w", err)
	}
	fr, err := s.dynamic.Resource(kargoFreightGVR).Namespace(ns).Get(ctx, freight, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read selected release: %w", err)
	}
	return verifiedReleasePolicy(stage, fr, app, sourceName)
}

func verifiedReleasePolicy(stage, fr *unstructured.Unstructured, app, sourceName string) error {
	freight := fr.GetName()
	origin, _, _ := unstructured.NestedMap(fr.Object, "origin")
	requests, _, _ := unstructured.NestedSlice(stage.Object, "spec", "requestedFreight")
	// Only admit the application's warehouse, never another app's freight in
	// the shared project namespace.
	kind, _ := origin["kind"].(string)
	name, _ := origin["name"].(string)
	if kind != "Warehouse" || name != app {
		return fmt.Errorf("%w: release does not belong to app %q", ErrReleaseNotVerified, app)
	}
	verified, _, err := unstructured.NestedMap(fr.Object, "status", "verifiedIn")
	if err != nil {
		return err
	}
	if _, ok := verified[sourceName]; !ok {
		return fmt.Errorf("%w: release %q has not been verified in %s", ErrReleaseNotVerified, freight, sourceName)
	}
	eligible := false
	for _, raw := range requests {
		request, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		requestOrigin, _, _ := unstructured.NestedMap(request, "origin")
		if requestOrigin["kind"] != kind || requestOrigin["name"] != name {
			continue
		}
		stages, _, _ := unstructured.NestedStringSlice(request, "sources", "stages")
		if !slices.Contains(stages, sourceName) {
			continue
		}
		strategy, _, _ := unstructured.NestedString(request, "sources", "availabilityStrategy")
		if strategy != "" && strategy != "OneOf" && strategy != "All" {
			continue
		}
		eligible = true
		if strategy == "All" {
			for _, upstream := range stages {
				if _, ok := verified[upstream]; !ok {
					eligible = false
				}
			}
		}
	}
	if !eligible {
		return fmt.Errorf("%w: target upstream verification policy is not satisfied", ErrReleaseNotVerified)
	}
	return nil
}

// VerifiedReleaseCandidates lists immutable release IDs with upstream evidence.
// Eligibility is rechecked on submission; listing does not approve freight.
func (s *KargoStore) VerifiedReleaseCandidates(ctx context.Context, project, app, source, target string) ([]string, error) {
	records, err := s.VerifiedReleaseDetails(ctx, project, app, source, target)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, record := range records {
		out = append(out, record.Name)
	}
	return out, nil
}

func (s *KargoStore) VerifiedReleaseDetails(ctx context.Context, project, app, source, target string) ([]VerifiedReleaseCandidate, error) {
	ns := kargoNamespaceForProject(project)
	stage, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).Get(ctx, kargoStageName(app, target), metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read target policy: %w", err)
	}
	items, err := s.dynamic.Resource(kargoFreightGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("read releases: %w", err)
	}
	sort.Slice(items.Items, func(i, j int) bool {
		a, b := items.Items[i].GetCreationTimestamp(), items.Items[j].GetCreationTimestamp()
		if a.Equal(&b) {
			return items.Items[i].GetName() < items.Items[j].GetName()
		}
		return a.After(b.Time)
	})
	out := []VerifiedReleaseCandidate{}
	for i := range items.Items {
		fr := &items.Items[i]
		err := verifiedReleasePolicy(stage, fr, app, kargoStageName(app, source))
		if errors.Is(err, ErrReleaseNotVerified) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read release evidence: %w", err)
		}
		record := VerifiedReleaseCandidate{Name: fr.GetName(), CreatedAt: fr.GetCreationTimestamp().Time, Images: []KargoFreightImage{}}
		images, _, err := unstructured.NestedSlice(fr.Object, "images")
		if err != nil {
			return nil, err
		}
		for _, raw := range images {
			im, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			repo, _, _ := unstructured.NestedString(im, "repoURL")
			tag, _, _ := unstructured.NestedString(im, "tag")
			record.Images = append(record.Images, KargoFreightImage{RepoURL: repo, Tag: tag})
		}
		out = append(out, record)
	}
	return out, nil
}

// NextVerifiedRelease never chooses an older discovered release automatically.
// Manual promotion and rollback remain explicit operations.
func (s *KargoStore) NextVerifiedRelease(ctx context.Context, project, app, source, target string) (string, error) {
	candidates, err := s.VerifiedReleaseDetails(ctx, project, app, source, target)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", nil
	}
	current, err := s.getCurrentFreight(ctx, kargoNamespaceForProject(project), kargoStageName(app, target))
	if err != nil {
		return "", err
	}
	if current == "" {
		return candidates[0].Name, nil
	}
	if current == candidates[0].Name {
		stage, err := s.dynamic.Resource(kargoStageGVR).Namespace(kargoNamespaceForProject(project)).Get(ctx, kargoStageName(app, target), metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		phase, _, _ := unstructured.NestedString(stage.Object, "status", "lastPromotion", "status", "phase")
		if phase == "Failed" || phase == "Errored" || phase == "Aborted" {
			return current, nil
		}
		return "", nil
	}
	fr, err := s.dynamic.Resource(kargoFreightGVR).Namespace(kargoNamespaceForProject(project)).Get(ctx, current, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read current release before advancing: %w", err)
	}
	if !candidates[0].CreatedAt.After(fr.GetCreationTimestamp().Time) {
		return "", nil
	}
	return candidates[0].Name, nil
}

// Stage-wide attempt names make concurrent submissions contend on one Kubernetes
// create. Repeating an active request returns that operation instead of queuing it twice.
func (s *KargoStore) verifiedPromotionAttempt(ctx context.Context, ns, stage, freight string) (string, *KargoPromotionInfo, error) {
	list, err := s.dynamic.Resource(kargoPromotionGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("read active promotions: %w", err)
	}
	prefix := fmt.Sprintf("verified-%x-", sha256.Sum256([]byte(stage)))
	prefix = prefix[:25] + "-"
	attempt := 0
	latestAttemptName := ""
	var active *KargoPromotionInfo
	successes := map[string]*KargoPromotionInfo{}
	for _, item := range list.Items {
		target, _, _ := unstructured.NestedString(item.Object, "spec", "stage")
		if target != stage {
			continue
		}
		if strings.HasPrefix(item.GetName(), prefix) {
			n, _ := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(item.GetName(), prefix), "r"))
			if n > attempt {
				attempt = n
				latestAttemptName = item.GetName()
			}
		}
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		switch phase {
		case "Succeeded":
			selected, _, _ := unstructured.NestedString(item.Object, "spec", "freight")
			if selected == freight {
				successes[item.GetName()] = &KargoPromotionInfo{Name: item.GetName(), Stage: stage, Freight: freight, Phase: phase}
			}
			continue
		case "Failed", "Errored", "Aborted":
			continue
		}
		selected, _, _ := unstructured.NestedString(item.Object, "spec", "freight")
		if selected != freight {
			return "", nil, ErrPromotionBusy
		}
		active = &KargoPromotionInfo{Name: item.GetName(), Stage: stage, Freight: freight, Phase: phase}
	}
	if active != nil {
		return "", active, nil
	}
	succeeded := successes[latestAttemptName]
	if succeeded != nil {
		// The latest collection is Kargo's actual destination release, not merely
		// a past successful operation that may have since been rolled back.
		current, err := s.getCurrentFreight(ctx, ns, stage)
		if err != nil {
			return "", nil, err
		}
		if current == freight {
			target, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).Get(ctx, stage, metav1.GetOptions{})
			if err != nil {
				return "", nil, err
			}
			last, _, _ := unstructured.NestedString(target.Object, "status", "lastPromotion", "name")
			if last == "" || last == succeeded.Name {
				return "", succeeded, nil
			}
		}
	}
	return verifiedAttemptName(prefix, attempt+1), nil, nil
}

// Kargo orders Promotion names lexically. Fixed-width attempts preserve that
// order across 9 -> 10; r also sorts after the earlier experimental numeric names.
func verifiedAttemptName(prefix string, attempt int) string {
	return fmt.Sprintf("%sr%020d", prefix, attempt)
}
