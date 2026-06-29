package runtime

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNamespace(t *testing.T) {
	if got := Namespace("myapi", "dev"); got != "myapi-dev" {
		t.Fatalf("expected myapi-dev, got %s", got)
	}
}

func TestDeploymentStatus(t *testing.T) {
	tests := []struct {
		name      string
		desired   int32
		ready     int32
		available int32
		want      string
	}{
		{"zero replicas", 0, 0, 0, StatusNotDeployed},
		{"all healthy", 3, 3, 3, StatusHealthy},
		{"partially available", 3, 1, 1, StatusDegraded},
		{"none available", 3, 0, 0, StatusProgressing},
		{"ready but not all available", 3, 3, 2, StatusDegraded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeploymentStatus(tc.desired, tc.ready, tc.available)
			if got != tc.want {
				t.Fatalf("DeploymentStatus(%d,%d,%d) = %s, want %s",
					tc.desired, tc.ready, tc.available, got, tc.want)
			}
		})
	}
}

func int32p(v int32) *int32 { return &v }

func TestK8sProviderNotDeployed(t *testing.T) {
	client := fake.NewSimpleClientset()
	p := NewK8sProvider(client, nil)

	info, err := p.GetServiceRuntime(context.Background(), "myapi-dev", "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusNotDeployed {
		t.Fatalf("expected not_deployed, got %s", info.Status)
	}
	if info.Namespace != "myapi-dev" {
		t.Fatalf("expected namespace myapi-dev, got %s", info.Namespace)
	}
	if len(info.IngressURLs) != 0 {
		t.Fatalf("expected empty ingress URLs, got %v", info.IngressURLs)
	}
}

func TestK8sProviderHealthy(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "api",
			Namespace:         "myapi-dev",
			CreationTimestamp: metav1.Now(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32p(2),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Image: "ghcr.io/org/api:v1.2.3"},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          2,
			ReadyReplicas:     2,
			AvailableReplicas: 2,
		},
	}

	client := fake.NewSimpleClientset(dep)
	p := NewK8sProvider(client, nil)

	info, err := p.GetServiceRuntime(context.Background(), "myapi-dev", "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusHealthy {
		t.Fatalf("expected healthy, got %s", info.Status)
	}
	if info.Image != "ghcr.io/org/api:v1.2.3" {
		t.Fatalf("expected image ghcr.io/org/api:v1.2.3, got %s", info.Image)
	}
	if info.Replicas != 2 {
		t.Fatalf("expected 2 replicas, got %d", info.Replicas)
	}
	if info.Available != 2 {
		t.Fatalf("expected 2 available, got %d", info.Available)
	}
}

func TestK8sProviderDegraded(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "api",
			Namespace:         "myapi-dev",
			CreationTimestamp: metav1.Now(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32p(3),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Image: "ghcr.io/org/api:v1.0.0"},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          3,
			ReadyReplicas:     1,
			AvailableReplicas: 1,
		},
	}

	client := fake.NewSimpleClientset(dep)
	p := NewK8sProvider(client, nil)

	info, err := p.GetServiceRuntime(context.Background(), "myapi-dev", "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusDegraded {
		t.Fatalf("expected degraded, got %s", info.Status)
	}
}

func TestK8sProviderWithIngress(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "web",
			Namespace:         "myapi-dev",
			CreationTimestamp: metav1.Now(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32p(1),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Image: "ghcr.io/org/web:latest"},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          1,
			ReadyReplicas:     1,
			AvailableReplicas: 1,
		},
	}

	pathType := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-ingress",
			Namespace: "myapi-dev",
		},
		Spec: networkingv1.IngressSpec{
			TLS: []networkingv1.IngressTLS{
				{Hosts: []string{"web.example.com"}},
			},
			Rules: []networkingv1.IngressRule{
				{
					Host: "web.example.com",
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{Path: "/", PathType: &pathType, Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: "web",
									},
								}},
							},
						},
					},
				},
			},
		},
	}

	client := fake.NewSimpleClientset(dep, ing)
	p := NewK8sProvider(client, nil)

	info, err := p.GetServiceRuntime(context.Background(), "myapi-dev", "web")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusHealthy {
		t.Fatalf("expected healthy, got %s", info.Status)
	}
	if len(info.IngressURLs) != 1 {
		t.Fatalf("expected 1 ingress URL, got %v", info.IngressURLs)
	}
	if info.IngressURLs[0] != "https://web.example.com" {
		t.Fatalf("expected https://web.example.com, got %s", info.IngressURLs[0])
	}
}

func TestK8sProviderUnrelatedIngress(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "api",
			Namespace:         "myapi-dev",
			CreationTimestamp: metav1.Now(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32p(1),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Image: "ghcr.io/org/api:v1"},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          1,
			ReadyReplicas:     1,
			AvailableReplicas: 1,
		},
	}

	pathType := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-service",
			Namespace: "myapi-dev",
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: "other.example.com",
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{Path: "/", PathType: &pathType, Backend: networkingv1.IngressBackend{
									Service: &networkingv1.IngressServiceBackend{
										Name: "other",
									},
								}},
							},
						},
					},
				},
			},
		},
	}

	client := fake.NewSimpleClientset(dep, ing)
	p := NewK8sProvider(client, nil)

	info, err := p.GetServiceRuntime(context.Background(), "myapi-dev", "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(info.IngressURLs) != 0 {
		t.Fatalf("expected no ingress URLs for unrelated ingress, got %v", info.IngressURLs)
	}
}

func TestIngressReferencesService(t *testing.T) {
	tests := []struct {
		ingress string
		service string
		want    bool
	}{
		{"api", "api", true},
		{"api-ingress", "api", true},
		{"web", "api", false},
		{"web-ingress", "api", false},
		{"api-v2", "api", true},
	}
	for _, tc := range tests {
		if got := ingressReferencesService(tc.ingress, tc.service); got != tc.want {
			t.Errorf("ingressReferencesService(%q, %q) = %v, want %v",
				tc.ingress, tc.service, got, tc.want)
		}
	}
}

// deployWithInstance builds a Deployment labelled for app-native discovery.
func deployWithInstance(name, ns, instance, image string, replicas, available int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			CreationTimestamp: metav1.Now(),
			Labels:            map[string]string{"app.kubernetes.io/instance": instance},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32p(replicas),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: image}}},
			},
		},
		Status: appsv1.DeploymentStatus{Replicas: replicas, ReadyReplicas: available, AvailableReplicas: available},
	}
}

// GetAppRuntime aggregates across every labelled Deployment regardless of name,
// so a BYO chart (Deployments named <release>-server / <release>-cm, not the app
// name) reports real replicas instead of a name-miss 0/0.
func TestGetAppRuntime_AggregatesLabelledWorkloads(t *testing.T) {
	const ns, instance = "proj-voice-staging", "proj-voice-staging"
	agent := deployWithInstance("voice-server", ns, instance, "img/agent:1", 3, 3)
	cm := deployWithInstance("voice-cm", ns, instance, "img/cm:1", 1, 0) // progressing
	client := fake.NewSimpleClientset(agent, cm)
	p := NewK8sProvider(client, nil)

	info, err := p.GetAppRuntime(context.Background(), ns, instance, "voice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Replicas != 4 || info.Available != 3 {
		t.Errorf("aggregate replicas = %d/%d, want 4/3", info.Available, info.Replicas)
	}
	// worst-of: one workload progressing → app is not healthy.
	if info.Status != StatusProgressing {
		t.Errorf("status = %s, want progressing (worst-of)", info.Status)
	}
}

// With no labelled workloads (label tracking off / canonical app), GetAppRuntime
// falls back to the name-based single-Deployment lookup.
func TestGetAppRuntime_FallsBackToNameWhenNoLabels(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "myapi-dev", CreationTimestamp: metav1.Now()},
		Spec:       appsv1.DeploymentSpec{Replicas: int32p(2)},
		Status:     appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2, AvailableReplicas: 2},
	}
	client := fake.NewSimpleClientset(dep) // no instance label
	p := NewK8sProvider(client, nil)

	info, err := p.GetAppRuntime(context.Background(), "myapi-dev", "myapi-api-dev", "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusHealthy || info.Replicas != 2 {
		t.Errorf("fallback lookup failed: status=%s replicas=%d", info.Status, info.Replicas)
	}
}

// A multi-workload app where one Deployment is scaled to zero (KEDA idle) must
// not be dragged to not_deployed: the running workload's health wins.
func TestGetAppRuntime_ScaleToZeroIsIdleNotNotDeployed(t *testing.T) {
	const ns, instance = "proj-voice-staging", "proj-voice-staging"
	agent := deployWithInstance("voice-server", ns, instance, "img/agent:1", 0, 0) // KEDA idle
	cm := deployWithInstance("voice-cm", ns, instance, "img/cm:1", 1, 1)            // healthy
	client := fake.NewSimpleClientset(agent, cm)
	p := NewK8sProvider(client, nil)

	info, err := p.GetAppRuntime(context.Background(), ns, instance, "voice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusHealthy {
		t.Errorf("status = %s, want healthy (idle agent must not drag it down)", info.Status)
	}
	if info.Replicas != 1 || info.Available != 1 {
		t.Errorf("replicas = %d/%d, want 1/1", info.Available, info.Replicas)
	}
}

// When every workload is scaled to zero the app reports idle, not not_deployed.
func TestGetAppRuntime_AllScaledToZeroIsIdle(t *testing.T) {
	const ns, instance = "proj-voice-staging", "proj-voice-staging"
	agent := deployWithInstance("voice-server", ns, instance, "img/agent:1", 0, 0)
	cm := deployWithInstance("voice-cm", ns, instance, "img/cm:1", 0, 0)
	client := fake.NewSimpleClientset(agent, cm)
	p := NewK8sProvider(client, nil)

	info, err := p.GetAppRuntime(context.Background(), ns, instance, "voice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusIdle {
		t.Errorf("status = %s, want idle", info.Status)
	}
}

// stsWithInstance builds a StatefulSet labelled for app-native discovery.
func stsWithInstance(name, ns, instance, image string, replicas, available int32) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			CreationTimestamp: metav1.Now(),
			Labels:            map[string]string{"app.kubernetes.io/instance": instance},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32p(replicas),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: image}}},
			},
		},
		Status: appsv1.StatefulSetStatus{Replicas: replicas, ReadyReplicas: available, AvailableReplicas: available},
	}
}

// dsWithInstance builds a DaemonSet labelled for app-native discovery. A
// DaemonSet's desired count is per-node (DesiredNumberScheduled), not spec.replicas.
func dsWithInstance(name, ns, instance, image string, desired, ready int32) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			CreationTimestamp: metav1.Now(),
			Labels:            map[string]string{"app.kubernetes.io/instance": instance},
		},
		Spec: appsv1.DaemonSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: image}}},
			},
		},
		Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: desired, NumberReady: ready, NumberAvailable: ready},
	}
}

// The real-world valkey case: an app that ships as a StatefulSet (no Deployment
// at all) must report its true health, not a perpetual 0/0 "not deployed".
func TestGetAppRuntime_DiscoversStatefulSet(t *testing.T) {
	const ns, instance = "voiceai", "valkey-internal"
	sts := stsWithInstance("valkey-internal", ns, instance, "valkey:9.0.2", 1, 1)
	client := fake.NewSimpleClientset(sts)
	p := NewK8sProvider(client, nil)

	info, err := p.GetAppRuntime(context.Background(), ns, instance, "valkey-internal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusHealthy {
		t.Errorf("status = %s, want healthy", info.Status)
	}
	if info.Replicas != 1 || info.Available != 1 {
		t.Errorf("replicas = %d/%d, want 1/1", info.Available, info.Replicas)
	}
	if info.Image != "valkey:9.0.2" {
		t.Errorf("image = %s, want valkey:9.0.2", info.Image)
	}
}

// A degraded StatefulSet (some pods not ready) must surface as degraded.
func TestGetAppRuntime_StatefulSetDegraded(t *testing.T) {
	const ns, instance = "voiceai", "valkey-internal"
	sts := stsWithInstance("valkey-internal", ns, instance, "valkey:9.0.2", 3, 1)
	client := fake.NewSimpleClientset(sts)
	p := NewK8sProvider(client, nil)

	info, err := p.GetAppRuntime(context.Background(), ns, instance, "valkey-internal")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusDegraded {
		t.Errorf("status = %s, want degraded", info.Status)
	}
}

// An app that ships as a DaemonSet reports its per-node health.
func TestGetAppRuntime_DiscoversDaemonSet(t *testing.T) {
	const ns, instance = "obs", "node-agent"
	ds := dsWithInstance("node-agent", ns, instance, "agent:1", 3, 3)
	client := fake.NewSimpleClientset(ds)
	p := NewK8sProvider(client, nil)

	info, err := p.GetAppRuntime(context.Background(), ns, instance, "node-agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusHealthy {
		t.Errorf("status = %s, want healthy", info.Status)
	}
	if info.Replicas != 3 || info.Available != 3 {
		t.Errorf("replicas = %d/%d, want 3/3", info.Available, info.Replicas)
	}
}

// An app composed of mixed workload kinds aggregates worst-of across all of them:
// a healthy Deployment plus a degraded StatefulSet is degraded, with summed replicas.
func TestGetAppRuntime_AggregatesMixedKinds(t *testing.T) {
	const ns, instance = "proj-staging", "proj-staging"
	api := deployWithInstance("api", ns, instance, "img/api:1", 2, 2)   // healthy
	db := stsWithInstance("db", ns, instance, "postgres:16", 3, 1)      // degraded
	client := fake.NewSimpleClientset(api, db)
	p := NewK8sProvider(client, nil)

	info, err := p.GetAppRuntime(context.Background(), ns, instance, "proj")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusDegraded {
		t.Errorf("status = %s, want degraded (worst-of)", info.Status)
	}
	if info.Replicas != 5 || info.Available != 3 {
		t.Errorf("replicas = %d/%d, want 5/3", info.Available, info.Replicas)
	}
}

// The name-based fallback (no instance labels) must also find a StatefulSet, so
// label-less / canonical apps that ship as StatefulSets aren't reported 0/0.
func TestGetServiceRuntime_FindsStatefulSetByName(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "valkey", Namespace: "myapi-dev", CreationTimestamp: metav1.Now()},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32p(1),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "valkey:9.0.2"}}},
			},
		},
		Status: appsv1.StatefulSetStatus{Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1},
	}
	client := fake.NewSimpleClientset(sts) // no instance label
	p := NewK8sProvider(client, nil)

	info, err := p.GetAppRuntime(context.Background(), "myapi-dev", "myapi-valkey-dev", "valkey")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Status != StatusHealthy || info.Replicas != 1 {
		t.Errorf("fallback STS lookup failed: status=%s replicas=%d", info.Status, info.Replicas)
	}
	if info.Image != "valkey:9.0.2" {
		t.Errorf("image = %s, want valkey:9.0.2", info.Image)
	}
}
