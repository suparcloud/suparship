package kube

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/suparcloud/suparship/internal/domain"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/util/retry"
)

const validationRunAnnotation = "suparship.io/validation-run"
const validationLeaseAnnotation = "suparship.io/validation-lease"

var ErrValidationLeaseBusy = errors.New("validation lifecycle is busy")
var ErrValidationStageMissing = errors.New("validation stage has not been published")

type validationLease struct {
	Token string    `json:"token"`
	Until time.Time `json:"until"`
}

// Every lifecycle action (including settings transitions) uses a Stage CAS lease.
// Work has a shorter deadline than the lease; expired owners cannot save progress.
func (s *KargoStore) WithValidationLease(ctx context.Context, project, app, env string, fn func(context.Context, *domain.ValidationRun) error) error {
	resource := s.dynamic.Resource(kargoStageGVR).Namespace(kargoNamespaceForProject(project))
	name := kargoStageName(app, env)
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	lease := validationLease{Token: hex.EncodeToString(tokenBytes), Until: time.Now().Add(15 * time.Minute)}
	var run domain.ValidationRun
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		obj, err := resource.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: %v", ErrValidationStageMissing, err)
		}
		if err != nil {
			return err
		}
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = map[string]string{}
		}
		if raw := annotations[validationLeaseAnnotation]; raw != "" {
			var old validationLease
			if err = json.Unmarshal([]byte(raw), &old); err != nil {
				return err
			}
			if time.Now().Before(old.Until) {
				return ErrValidationLeaseBusy
			}
		}
		run = domain.ValidationRun{}
		if raw := annotations[validationRunAnnotation]; raw != "" {
			if err = json.Unmarshal([]byte(raw), &run); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(lease)
		annotations[validationLeaseAnnotation] = string(raw)
		obj.SetAnnotations(annotations)
		_, err = resource.Update(ctx, obj, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return err
	}
	workCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	workErr := fn(workCtx, &run)
	// A cancelled caller still releases its lease and records retryable progress.
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer saveCancel()
	saveErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		obj, err := resource.Get(saveCtx, name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		annotations := obj.GetAnnotations()
		var current validationLease
		if err = json.Unmarshal([]byte(annotations[validationLeaseAnnotation]), &current); err != nil {
			return err
		}
		if current.Token != lease.Token || time.Now().After(current.Until) {
			return ErrValidationLeaseBusy
		}
		raw, err := json.Marshal(run)
		if err != nil {
			return err
		}
		annotations[validationRunAnnotation] = string(raw)
		delete(annotations, validationLeaseAnnotation)
		obj.SetAnnotations(annotations)
		_, err = resource.Update(saveCtx, obj, metav1.UpdateOptions{})
		return err
	})
	return errors.Join(workErr, saveErr)
}

func (s *KargoStore) NextValidationFreight(ctx context.Context, project, app string, cursor time.Time) (string, time.Time, error) {
	list, err := s.dynamic.Resource(kargoFreightGVR).Namespace(kargoNamespaceForProject(project)).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", time.Time{}, err
	}
	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].GetCreationTimestamp().After(list.Items[j].GetCreationTimestamp().Time)
	})
	for _, fr := range list.Items {
		origin, _, _ := unstructured.NestedString(fr.Object, "origin", "name")
		kind, _, _ := unstructured.NestedString(fr.Object, "origin", "kind")
		at := fr.GetCreationTimestamp().Time
		if origin == app && kind == "Warehouse" && at.After(cursor) {
			return fr.GetName(), at, nil
		}
	}
	return "", time.Time{}, nil
}

func (s *KargoStore) ObserveValidation(ctx context.Context, project, app, env, freight string) (domain.ValidationObservation, error) {
	var out domain.ValidationObservation
	ns, stageName := kargoNamespaceForProject(project), kargoStageName(app, env)
	stage, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).Get(ctx, stageName, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	status, _ := stage.Object["status"].(map[string]any)
	out.CurrentFreight = freightNameFromHistory(status)
	promotions, err := s.dynamic.Resource(kargoPromotionGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return out, err
	}
	for _, p := range promotions.Items {
		target, _, _ := unstructured.NestedString(p.Object, "spec", "stage")
		if target != stageName {
			continue
		}
		phase, _, _ := unstructured.NestedString(p.Object, "status", "phase")
		if phase != "Succeeded" && phase != "Failed" && phase != "Errored" && phase != "Aborted" {
			out.Active = true
		}
	}
	if out.CurrentFreight != "" {
		current, err := s.dynamic.Resource(kargoFreightGVR).Namespace(ns).Get(ctx, out.CurrentFreight, metav1.GetOptions{})
		if err != nil {
			return out, err
		}
		out.CurrentCreatedAt = current.GetCreationTimestamp().Time
	}
	if freight == "" {
		return out, nil
	}
	fr, err := s.dynamic.Resource(kargoFreightGVR).Namespace(ns).Get(ctx, freight, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	verified, _, err := unstructured.NestedMap(fr.Object, "status", "verifiedIn")
	if err != nil {
		return out, err
	}
	_, out.Verified = verified[stageName]
	selected, _, _ := unstructured.NestedString(stage.Object, "status", "lastPromotion", "freight", "name")
	phase, _, _ := unstructured.NestedString(stage.Object, "status", "lastPromotion", "status", "phase")
	out.Failed = selected == freight && (phase == "Failed" || phase == "Errored" || phase == "Aborted")
	history, _, _ := unstructured.NestedSlice(stage.Object, "status", "freightHistory")
	for _, raw := range history {
		coll, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		items, _, _ := unstructured.NestedMap(coll, "items")
		matched := false
		for _, rawItem := range items {
			item, ok := rawItem.(map[string]any)
			if ok && item["name"] == freight {
				matched = true
			}
		}
		if !matched {
			continue
		}
		checks, _, _ := unstructured.NestedSlice(coll, "verificationHistory")
		if len(checks) > 0 {
			check, _ := checks[0].(map[string]any)
			p, _ := check["phase"].(string)
			out.Failed = out.Failed || p == "Failed" || p == "Error" || p == "Errored" || p == "Aborted" || p == "Inconclusive"
			out.Active = out.Active || p == "Running" || p == "Pending"
		}
		break
	}
	return out, nil
}

func (s *KargoStore) PromoteValidationFreight(ctx context.Context, project, app, env, freight string) error {
	ready, err := s.DownstreamSchedulerReady(ctx, project, app, env)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("waiting for validation scheduler handoff")
	}
	ns, stageName := kargoNamespaceForProject(project), kargoStageName(app, env)
	stage, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).Get(ctx, stageName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	requests, _, _ := unstructured.NestedSlice(stage.Object, "spec", "requestedFreight")
	direct := false
	for _, r := range requests {
		m, _ := r.(map[string]any)
		origin, _, _ := unstructured.NestedString(m, "origin", "name")
		yes, _, _ := unstructured.NestedBool(m, "sources", "direct")
		if origin == app && yes {
			direct = true
		}
	}
	if !direct {
		return fmt.Errorf("validation stage must receive its own Warehouse directly")
	}
	fr, err := s.dynamic.Resource(kargoFreightGVR).Namespace(ns).Get(ctx, freight, metav1.GetOptions{})
	if err != nil {
		return err
	}
	origin, _, _ := unstructured.NestedString(fr.Object, "origin", "name")
	kind, _, _ := unstructured.NestedString(fr.Object, "origin", "kind")
	if origin != app || kind != "Warehouse" {
		return fmt.Errorf("release belongs to a different Warehouse")
	}
	name, active, err := s.verifiedPromotionAttempt(ctx, ns, stageName, freight)
	if err != nil {
		return err
	}
	if active != nil {
		return nil
	}
	_, err = s.submitPromotion(ctx, ns, project, app, env, stageName, freight, name)
	return err
}

// Applications must exist before Kargo syncs; deletion must finish before reuse.
// Application finalizers perform cascading workload cleanup. Never delete a namespace.
func (s *KargoStore) ValidationApplicationsReady(ctx context.Context, names []string, present bool) (bool, error) {
	resource := s.dynamic.Resource(schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}).Namespace("argocd")
	for _, name := range names {
		obj, err := resource.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if present {
				return false, nil
			}
			continue
		}
		if err != nil {
			return false, err
		}
		if obj.GetDeletionTimestamp() != nil {
			return false, nil
		}
		idle := obj.GetAnnotations()["suparship.io/validation-idle"] == "true"
		if present {
			// Do not promote against the healthy empty shell from the preceding run.
			if idle {
				return false, nil
			}
			automated, _, _ := unstructured.NestedMap(obj.Object, "spec", "syncPolicy", "automated")
			if automated != nil {
				return false, nil
			}
			continue
		}
		if !idle {
			return false, nil
		}
		sync, _, _ := unstructured.NestedString(obj.Object, "status", "sync", "status")
		health, _, _ := unstructured.NestedString(obj.Object, "status", "health", "status")
		resources, _, _ := unstructured.NestedSlice(obj.Object, "status", "resources")
		operation, _, _ := unstructured.NestedMap(obj.Object, "operation")
		if sync != "Synced" || health != "Healthy" || len(resources) != 0 || operation != nil {
			return false, nil
		}
		// Status can still describe the old workload source immediately after spec
		// changes. Require Argo to have reconciled the empty source itself.
		field := "source"
		if sources, _, _ := unstructured.NestedSlice(obj.Object, "spec", "sources"); len(sources) > 0 {
			field = "sources"
		}
		desired, _, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", field)
		observed, _, _ := unstructured.NestedFieldNoCopy(obj.Object, "status", "sync", "comparedTo", field)
		if desired == nil || !reflect.DeepEqual(desired, observed) {
			return false, nil
		}
	}
	return true, nil
}
