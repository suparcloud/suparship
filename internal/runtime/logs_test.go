package runtime

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestValidateLogsRequest_OK(t *testing.T) {
	tl := int64(100)
	req := &LogsRequest{
		Namespace: "api-dev",
		Pod:       "backend-abc",
		TailLines: &tl,
	}
	if err := ValidateLogsRequest(req); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestValidateLogsRequest_MissingNamespace(t *testing.T) {
	req := &LogsRequest{Pod: "backend-abc"}
	if err := ValidateLogsRequest(req); err == nil {
		t.Fatal("expected error for missing namespace")
	}
}

func TestValidateLogsRequest_NegativeTailLines(t *testing.T) {
	tl := int64(-1)
	req := &LogsRequest{
		Namespace: "api-dev",
		TailLines: &tl,
	}
	if err := ValidateLogsRequest(req); err == nil {
		t.Fatal("expected error for negative tailLines")
	}
}

func TestValidateLogsRequest_NilTailLines(t *testing.T) {
	req := &LogsRequest{Namespace: "api-dev"}
	if err := ValidateLogsRequest(req); err != nil {
		t.Fatalf("nil tailLines should be valid, got: %v", err)
	}
}

func podWithLabels(name, ns string, labels map[string]string, containers ...string) *corev1.Pod {
	cs := make([]corev1.Container, 0, len(containers))
	for _, c := range containers {
		cs = append(cs, corev1.Container{Name: c})
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels},
		Spec:       corev1.PodSpec{Containers: cs},
	}
}

// A BYO chart labels its pods app.kubernetes.io/name=<chart> and
// app.kubernetes.io/instance=<app> (the Helm release = app name). ListPods must
// find them via the instance label even though the name label is the chart name.
func TestListPods_FindsByInstanceLabel(t *testing.T) {
	const ns = "test-voiceai"
	pod := podWithLabels("test-voiceai-express-caller-voiceai-livekit-agent-cm-abc", ns,
		map[string]string{
			"app.kubernetes.io/name":     "voiceai-livekit-agent", // chart name, NOT app name
			"app.kubernetes.io/instance": "test-voiceai-express-caller",
		}, "cm")
	client := fake.NewSimpleClientset(pod)
	p := NewK8sLogsProvider(client)

	pods, err := p.ListPods(context.Background(), ns, "test-voiceai-express-caller")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 1 || pods[0].Name != pod.Name {
		t.Fatalf("expected the BYO pod found by instance label, got %+v", pods)
	}
	if len(pods[0].Containers) != 1 || pods[0].Containers[0] != "cm" {
		t.Errorf("expected container 'cm', got %+v", pods[0].Containers)
	}
}

// Canonical charts that label app.kubernetes.io/name=<app> still resolve.
func TestListPods_FallsBackToNameLabel(t *testing.T) {
	const ns = "myapi-dev"
	pod := podWithLabels("api-xyz", ns,
		map[string]string{"app.kubernetes.io/name": "api"}, "web")
	client := fake.NewSimpleClientset(pod)
	p := NewK8sLogsProvider(client)

	pods, err := p.ListPods(context.Background(), ns, "api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 1 || pods[0].Name != "api-xyz" {
		t.Fatalf("expected the pod found by name label, got %+v", pods)
	}
}
