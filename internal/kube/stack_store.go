// Package kube — stack_store.go provides a Kubernetes-backed implementation of
// domain.StackStore. A stack is a logical grouping of apps within a project; the
// record holds the stack's metadata + shared override layer. Membership lives on
// the app (AppSpec.Stack), not here.
//
// ConfigMap naming:
//
//	suparship-stack-{project}-{stack}    — stack record (stack.json)
//
// Labels:
//
//	suparship.io/type    = "stack"
//	suparship.io/project = {projectName}
package kube

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/project"
)

const (
	stackConfigMapPrefix = "suparship-stack-"
	stackConfigMapKey    = "stack.json"
)

// K8sStackStore implements domain.StackStore using Kubernetes ConfigMaps.
type K8sStackStore struct {
	client kubernetes.Interface
}

// NewK8sStackStore creates a K8sStackStore backed by the given client.
func NewK8sStackStore(client kubernetes.Interface) *K8sStackStore {
	return &K8sStackStore{client: client}
}

var _ domain.StackStore = (*K8sStackStore)(nil)

func stackConfigMapName(projectName, stackName string) string {
	return stackConfigMapPrefix + projectName + "-" + stackName
}

// SaveStack upserts a stack record ConfigMap.
func (s *K8sStackStore) SaveStack(ctx context.Context, stack *domain.Stack) error {
	data, err := json.Marshal(stack)
	if err != nil {
		return fmt.Errorf("marshaling stack %q: %w", stack.Name, err)
	}
	cmName := stackConfigMapName(stack.ProjectName, stack.Name)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"suparship.io/type":    "stack",
				"suparship.io/project": stack.ProjectName,
				"suparship.io/stack":   stack.Name,
			},
		},
		Data: map[string]string{stackConfigMapKey: string(data)},
	}

	existing, err := s.client.CoreV1().ConfigMaps(project.Namespace).Get(ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := s.client.CoreV1().ConfigMaps(project.Namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating stack configmap %s: %w", cmName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking stack configmap %s: %w", cmName, err)
	}
	existing.Labels = cm.Labels
	existing.Data = cm.Data
	if _, err := s.client.CoreV1().ConfigMaps(project.Namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating stack configmap %s: %w", cmName, err)
	}
	return nil
}

// GetStack retrieves a single stack by project and name.
func (s *K8sStackStore) GetStack(ctx context.Context, projectName, stackName string) (*domain.Stack, error) {
	cmName := stackConfigMapName(projectName, stackName)
	cm, err := s.client.CoreV1().ConfigMaps(project.Namespace).Get(ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("stack %q not found in project %q", stackName, projectName)
	}
	if err != nil {
		return nil, fmt.Errorf("reading stack configmap %s: %w", cmName, err)
	}
	return parseStackConfigMap(cm)
}

// ListStacks returns all stacks for a project.
func (s *K8sStackStore) ListStacks(ctx context.Context, projectName string) ([]*domain.Stack, error) {
	cms, err := s.client.CoreV1().ConfigMaps(project.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "suparship.io/type=stack,suparship.io/project=" + projectName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing stack configmaps for project %q: %w", projectName, err)
	}
	stacks := make([]*domain.Stack, 0, len(cms.Items))
	for i := range cms.Items {
		st, err := parseStackConfigMap(&cms.Items[i])
		if err != nil {
			return nil, fmt.Errorf("parsing stack from configmap %s: %w", cms.Items[i].Name, err)
		}
		stacks = append(stacks, st)
	}
	return stacks, nil
}

// DeleteStack removes a stack record. Member apps are untouched (the caller
// clears their AppSpec.Stack or deletes them separately).
func (s *K8sStackStore) DeleteStack(ctx context.Context, projectName, stackName string) error {
	cmName := stackConfigMapName(projectName, stackName)
	err := s.client.CoreV1().ConfigMaps(project.Namespace).Delete(ctx, cmName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("stack %q not found in project %q", stackName, projectName)
	}
	if err != nil {
		return fmt.Errorf("deleting stack configmap %s: %w", cmName, err)
	}
	return nil
}

func parseStackConfigMap(cm *corev1.ConfigMap) (*domain.Stack, error) {
	raw, ok := cm.Data[stackConfigMapKey]
	if !ok {
		return nil, fmt.Errorf("configmap %s missing %s", cm.Name, stackConfigMapKey)
	}
	var st domain.Stack
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, fmt.Errorf("unmarshaling stack from %s: %w", cm.Name, err)
	}
	return &st, nil
}
