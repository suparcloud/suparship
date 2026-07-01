package kube

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// labeledAppCR builds a suparShip Application with explicit project+app labels
// (appCR defaults the app label to the CR name; here the CR is "{app}-{env}").
func labeledAppCR(crName, project, app string) *unstructured.Unstructured {
	a := appCR(crName, project, false, nil)
	a.SetLabels(map[string]string{"suparship.io/project": project, "suparship.io/app": app})
	return a
}

func TestRefreshApps_AnnotatesOnlyMatchingApps(t *testing.T) {
	r := newStuckReader(t,
		labeledAppCR("web-staging", "voiceai", "web"),
		labeledAppCR("agent-staging", "voiceai", "agent"),
		labeledAppCR("loner-staging", "voiceai", "loner"), // not in the refresh set
	)
	ctx := context.Background()

	if err := r.RefreshApps(ctx, "voiceai", []string{"web", "agent"}); err != nil {
		t.Fatalf("RefreshApps: %v", err)
	}

	for _, tc := range []struct {
		cr   string
		want bool
	}{
		{"web-staging", true},
		{"agent-staging", true},
		{"loner-staging", false},
	} {
		got, err := r.dynamic.Resource(argoCDAppGVR).Namespace("argocd").Get(ctx, tc.cr, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get %s: %v", tc.cr, err)
		}
		val, has := got.GetAnnotations()[argoRefreshAnnotation]
		if has != tc.want {
			t.Errorf("%s: refresh annotation present=%v, want %v", tc.cr, has, tc.want)
		}
		if tc.want && val != argoRefreshNormal {
			t.Errorf("%s: refresh annotation = %q, want %q", tc.cr, val, argoRefreshNormal)
		}
	}
}

func TestRefreshApps_NoopOnEmpty(t *testing.T) {
	r := newStuckReader(t, labeledAppCR("web-staging", "voiceai", "web"))
	if err := r.RefreshApps(context.Background(), "voiceai", nil); err != nil {
		t.Errorf("empty app list should be a no-op, got %v", err)
	}
	var nilReader *ArgoCDStatusReader
	if err := nilReader.RefreshApps(context.Background(), "voiceai", []string{"web"}); err != nil {
		t.Errorf("nil reader should be a no-op, got %v", err)
	}
}
