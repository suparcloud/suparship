package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
)

const promotionReservationAnnotation = "suparship.io/promotion-reservation"

type promotionReservation struct {
	Predecessor string `json:"predecessor"`
	Name        string `json:"name"`
}

// Match Kargo v1.9's promotion builder: stage prefix (at most 218 chars),
// lowercase ULID, and the first seven characters of the Freight ID.
func nativePromotionName(stage, freight string) string {
	if len(stage) > 218 {
		stage = stage[:218]
	}
	if len(freight) > 7 {
		freight = freight[:7]
	}
	return strings.ToLower(stage + "." + ulid.Make().String() + "." + freight)
}

// Reserve a native name with the Stage's resourceVersion as the CAS. Callers
// observing the same predecessor share one create name, including across server
// replicas and restarts. The reservation survives a crash before CR creation.
// Native Kargo clients do not participate in this reservation; Kargo queues them.
func (s *KargoStore) reservePromotionName(ctx context.Context, ns, stage, freight, predecessor string) (string, error) {
	var name string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		obj, err := s.dynamic.Resource(kargoStageGVR).Namespace(ns).Get(ctx, stage, metav1.GetOptions{})
		if err != nil {
			return err
		}
		last, _, err := unstructured.NestedString(obj.Object, "status", "lastPromotion", "name")
		if err != nil {
			return err
		}
		if last > predecessor {
			predecessor = last
		}
		annotations := obj.GetAnnotations()
		var reservation promotionReservation
		if raw := annotations[promotionReservationAnnotation]; raw != "" {
			if err := json.Unmarshal([]byte(raw), &reservation); err != nil {
				return fmt.Errorf("invalid promotion reservation: %w", err)
			}
			if reservation.Predecessor == predecessor && reservation.Name > predecessor {
				name = reservation.Name
				return nil
			}
			// Another caller advanced after our list. Never overwrite its reservation.
			if reservation.Predecessor > predecessor {
				return ErrPromotionBusy
			}
		}
		name = nativePromotionName(stage, freight)
		if name <= predecessor {
			return fmt.Errorf("promotion history %q sorts after a new Kargo-compatible name; reconcile legacy promotion history or clock skew before retrying", predecessor)
		}
		reservation = promotionReservation{Predecessor: predecessor, Name: name}
		raw, err := json.Marshal(reservation)
		if err != nil {
			return err
		}
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[promotionReservationAnnotation] = string(raw)
		obj.SetAnnotations(annotations)
		_, err = s.dynamic.Resource(kargoStageGVR).Namespace(ns).Update(ctx, obj, metav1.UpdateOptions{})
		return err
	})
	return name, err
}
