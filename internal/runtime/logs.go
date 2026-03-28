package runtime

import (
	"context"
	"fmt"
	"io"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// LogsRequest describes which pod/container logs to fetch.
type LogsRequest struct {
	Namespace string
	Pod       string // specific pod; if empty, auto-select first matching pod
	Container string // specific container; if empty, use default
	TailLines *int64 // if set, only return this many lines from the end
}

// LogsResult holds the fetched log output and context.
type LogsResult struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Logs      string `json:"logs"`
}

// PodInfo describes a running pod relevant to a service.
type PodInfo struct {
	Name       string   `json:"name"`
	Containers []string `json:"containers"`
}

// LogsProvider reads pod logs from the cluster.
type LogsProvider interface {
	GetLogs(ctx context.Context, req LogsRequest) (*LogsResult, error)
	ListPods(ctx context.Context, namespace, serviceName string) ([]PodInfo, error)
}

// K8sLogsProvider reads pod logs via client-go.
type K8sLogsProvider struct {
	client kubernetes.Interface
}

// NewK8sLogsProvider creates a Kubernetes-backed logs provider.
func NewK8sLogsProvider(client kubernetes.Interface) *K8sLogsProvider {
	return &K8sLogsProvider{client: client}
}

const maxLogBytes = 1 << 20 // 1 MiB safety limit for non-streaming reads

// ListPods returns pods in a namespace matching the service name by label.
func (p *K8sLogsProvider) ListPods(ctx context.Context, namespace, serviceName string) ([]PodInfo, error) {
	pods, err := p.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=" + serviceName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods in %s: %w", namespace, err)
	}

	if len(pods.Items) == 0 {
		pods, err = p.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: "app=" + serviceName,
		})
		if err != nil {
			return nil, fmt.Errorf("listing pods in %s: %w", namespace, err)
		}
	}

	result := make([]PodInfo, 0, len(pods.Items))
	for _, pod := range pods.Items {
		info := PodInfo{Name: pod.Name}
		for _, c := range pod.Spec.Containers {
			info.Containers = append(info.Containers, c.Name)
		}
		result = append(result, info)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// GetLogs fetches logs for a specific pod and container.
func (p *K8sLogsProvider) GetLogs(ctx context.Context, req LogsRequest) (*LogsResult, error) {
	if req.Namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	if req.Pod == "" {
		return nil, fmt.Errorf("pod name is required")
	}

	opts := &corev1.PodLogOptions{}
	if req.Container != "" {
		opts.Container = req.Container
	}
	if req.TailLines != nil {
		opts.TailLines = req.TailLines
	}

	limit := int64(maxLogBytes)
	opts.LimitBytes = &limit

	stream, err := p.client.CoreV1().Pods(req.Namespace).GetLogs(req.Pod, opts).Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading logs for %s/%s: %w", req.Namespace, req.Pod, err)
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		return nil, fmt.Errorf("reading log stream: %w", err)
	}

	container := req.Container
	if container == "" {
		container = "(default)"
	}

	return &LogsResult{
		Pod:       req.Pod,
		Container: container,
		Logs:      string(data),
	}, nil
}

// ValidateLogsRequest checks a LogsRequest for basic correctness.
func ValidateLogsRequest(req *LogsRequest) error {
	if req.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if req.TailLines != nil && *req.TailLines < 0 {
		return fmt.Errorf("tailLines must be non-negative")
	}
	return nil
}
