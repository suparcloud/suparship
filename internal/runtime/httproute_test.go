package runtime

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func httpRouteObj(namespace, name string, labels map[string]string, hostnames []string, path, backendSvc string) *unstructured.Unstructured {
	rule := map[string]any{
		"backendRefs": []any{map[string]any{"name": backendSvc}},
	}
	if path != "" {
		rule["matches"] = []any{map[string]any{"path": map[string]any{"value": path}}}
	}
	hosts := make([]any, len(hostnames))
	for i, h := range hostnames {
		hosts[i] = h
	}
	obj := map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec":       map[string]any{"hostnames": hosts, "rules": []any{rule}},
	}
	u := &unstructured.Unstructured{Object: obj}
	if len(labels) > 0 {
		u.SetLabels(labels)
	}
	return u
}

func newDynFake(objs ...k8sruntime.Object) *dynfake.FakeDynamicClient {
	scheme := k8sruntime.NewScheme()
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{httpRouteGVR: "HTTPRouteList"},
		objs...,
	)
}

func hasURL(urls []string, want string) bool {
	for _, u := range urls {
		if u == want {
			return true
		}
	}
	return false
}

// GetServiceRuntime surfaces an HTTPRoute whose backendRef targets the service.
func TestGetServiceRuntime_HTTPRouteBackendRef(t *testing.T) {
	rt := httpRouteObj("ns", "web-route", nil, []string{"web.example.com"}, "/api", "web")
	p := NewK8sProvider(fake.NewSimpleClientset(), newDynFake(rt))

	info, err := p.GetServiceRuntime(context.Background(), "ns", "web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasURL(info.IngressURLs, "https://web.example.com/api") {
		t.Fatalf("expected https://web.example.com/api, got %v", info.IngressURLs)
	}
}

// GetAppRuntime surfaces an HTTPRoute carrying the app's instance label (no path
// match → "/", so a bare host URL).
func TestGetAppRuntime_HTTPRouteByLabel(t *testing.T) {
	reps := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "foo-web", Namespace: "ns",
			Labels:            map[string]string{instanceLabel: "foo"},
			CreationTimestamp: metav1.Now(),
		},
		Spec:   appsv1.DeploymentSpec{Replicas: &reps},
		Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1},
	}
	rt := httpRouteObj("ns", "foo-route", map[string]string{instanceLabel: "foo"}, []string{"foo.example.com"}, "", "foo")
	p := NewK8sProvider(fake.NewSimpleClientset(dep), newDynFake(rt))

	info, err := p.GetAppRuntime(context.Background(), "ns", "foo", "foo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusHealthy {
		t.Fatalf("status = %s, want healthy", info.Status)
	}
	if !hasURL(info.IngressURLs, "https://foo.example.com") {
		t.Fatalf("expected https://foo.example.com, got %v", info.IngressURLs)
	}
}

// A resource-only app (an HTTPRoute/ingress app with no workloads) must report
// Healthy — it's deployed, there are just no pods to count — not "not deployed".
func TestGetAppRuntime_ResourceOnlyAppHealthy(t *testing.T) {
	rt := httpRouteObj("ns", "routes", map[string]string{instanceLabel: "routes"},
		[]string{"voiceai-livekit.example.com"}, "", "lk-sh-web")
	p := NewK8sProvider(fake.NewSimpleClientset(), newDynFake(rt))

	info, err := p.GetAppRuntime(context.Background(), "ns", "routes", "routes")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusHealthy {
		t.Fatalf("status = %s, want healthy (resource-only app is deployed)", info.Status)
	}
	if info.Replicas != 0 {
		t.Errorf("replicas = %d, want 0 (no workloads)", info.Replicas)
	}
	if !hasURL(info.IngressURLs, "https://voiceai-livekit.example.com") {
		t.Errorf("expected the route endpoint, got %v", info.IngressURLs)
	}
}

// With neither workloads nor routing resources, the app is genuinely not deployed.
func TestGetAppRuntime_NoWorkloadsNoRoutesNotDeployed(t *testing.T) {
	p := NewK8sProvider(fake.NewSimpleClientset(), newDynFake())
	info, err := p.GetAppRuntime(context.Background(), "ns", "ghost", "ghost")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusNotDeployed {
		t.Fatalf("status = %s, want not_deployed", info.Status)
	}
}

// A nil dynamic client disables HTTPRoute discovery without error.
func TestGetServiceRuntime_NilDynamicNoHTTPRoute(t *testing.T) {
	p := NewK8sProvider(fake.NewSimpleClientset(), nil)
	info, err := p.GetServiceRuntime(context.Background(), "ns", "web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(info.IngressURLs) != 0 {
		t.Fatalf("expected no URLs, got %v", info.IngressURLs)
	}
}

// The HTTPRoute URL scheme follows the org's secure-endpoints setting: nil
// getter (never wired) keeps the https default; a false getter yields http.
func TestHTTPRouteURLScheme_FollowsSecureEndpoints(t *testing.T) {
	rt := httpRouteObj("ns", "web-route", nil, []string{"web.example.com"}, "", "web")

	p := NewK8sProvider(fake.NewSimpleClientset(), newDynFake(rt))
	info, err := p.GetServiceRuntime(context.Background(), "ns", "web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasURL(info.IngressURLs, "https://web.example.com") {
		t.Fatalf("nil secure func should default to https, got %v", info.IngressURLs)
	}

	p.SetSecureEndpointsFunc(func() bool { return false })
	info, err = p.GetServiceRuntime(context.Background(), "ns", "web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasURL(info.IngressURLs, "http://web.example.com") {
		t.Fatalf("secure=false should yield http, got %v", info.IngressURLs)
	}
}

// Platform-rendered HTTPRoutes are labelled suparship.io/app=<owner> (ArgoCD's
// label tracking overwrites the instance label with its Application name), so
// GetAppRuntimeFor attributes them to the app by owner — also for a composed
// component whose instance is "{app}-{component}".
func TestGetAppRuntimeFor_PlatformRouteByOwnerLabel(t *testing.T) {
	reps := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-web", Namespace: "ns", Labels: map[string]string{instanceLabel: "hello-web"}},
		Spec:       appsv1.DeploymentSpec{Replicas: &reps},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1},
	}
	rt := httpRouteObj("ns", "hello-web-route", map[string]string{
		instanceLabel:   "demo-hello-staging-platform", // what ArgoCD stamps
		routeOwnerLabel: "hello",
	}, []string{"hello.example.com"}, "/", "hello-web")
	p := NewK8sProvider(fake.NewSimpleClientset(dep), newDynFake(rt))

	info, err := p.GetAppRuntimeFor(context.Background(), "ns", "hello-web", "hello-web", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasURL(info.IngressURLs, "https://hello.example.com") {
		t.Fatalf("platform route should be attributed by owner label, got %v", info.IngressURLs)
	}
	// Without the owner hint the relabelled route is invisible to the instance.
	plain, err := p.GetAppRuntime(context.Background(), "ns", "hello-web", "hello-web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hasURL(plain.IngressURLs, "https://hello.example.com") {
		t.Fatalf("instance-only lookup should not see the relabelled route, got %v", plain.IngressURLs)
	}
}

// Platform-rendered Ingresses carry the same owner label and are attributed
// the same way.
func TestGetAppRuntimeFor_PlatformIngressByOwnerLabel(t *testing.T) {
	reps := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-web", Namespace: "ns", Labels: map[string]string{instanceLabel: "hello-web"}},
		Spec:       appsv1.DeploymentSpec{Replicas: &reps},
		Status:     appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1},
	}
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "hello-web", Namespace: "ns", Labels: map[string]string{
			instanceLabel:   "demo-hello-staging-platform",
			routeOwnerLabel: "hello",
		}},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{Host: "hello.example.com"}}},
	}
	p := NewK8sProvider(fake.NewSimpleClientset(dep, ing), newDynFake())
	info, err := p.GetAppRuntimeFor(context.Background(), "ns", "hello-web", "hello-web", "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !hasURL(info.IngressURLs, "https://hello.example.com") {
		t.Fatalf("platform ingress should be attributed by owner label, got %v", info.IngressURLs)
	}
}
