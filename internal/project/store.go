package project

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Store defines CRUD operations for projects.
type Store interface {
	List(ctx context.Context) ([]*Project, error)
	Get(ctx context.Context, name string) (*Project, error)
	Save(ctx context.Context, p *Project) error
	Delete(ctx context.Context, name string) error
}

// K8sStore implements Store using Kubernetes ConfigMaps.
// Each project is stored as a separate ConfigMap named
// suparship-project-{name} in the suparship-system namespace.
type K8sStore struct {
	client kubernetes.Interface
}

// NewK8sStore creates a ConfigMap-backed project store.
func NewK8sStore(client kubernetes.Interface) *K8sStore {
	return &K8sStore{client: client}
}

func (s *K8sStore) List(ctx context.Context) ([]*Project, error) {
	cms, err := s.client.CoreV1().ConfigMaps(Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "suparship.io/type=project",
	})
	if err != nil {
		return nil, fmt.Errorf("listing project configmaps: %w", err)
	}

	projects := make([]*Project, 0, len(cms.Items))
	for _, cm := range cms.Items {
		data, ok := cm.Data[ConfigMapKey]
		if !ok {
			continue
		}
		p, err := Parse([]byte(data))
		if err != nil {
			return nil, fmt.Errorf("parsing project from configmap %s: %w", cm.Name, err)
		}
		projects = append(projects, p)
	}

	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Metadata.Name < projects[j].Metadata.Name
	})

	return projects, nil
}

func (s *K8sStore) Get(ctx context.Context, name string) (*Project, error) {
	cmName := ConfigMapPrefix + name
	cm, err := s.client.CoreV1().ConfigMaps(Namespace).Get(ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("project %q not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("reading configmap %s/%s: %w", Namespace, cmName, err)
	}

	data, ok := cm.Data[ConfigMapKey]
	if !ok {
		return nil, fmt.Errorf("configmap %s missing key %q", cmName, ConfigMapKey)
	}

	return Parse([]byte(data))
}

func (s *K8sStore) Save(ctx context.Context, p *Project) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("invalid project: %w", err)
	}

	data, err := p.Marshal()
	if err != nil {
		return err
	}

	cmName := p.ConfigMapName()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: Namespace,
			Labels: map[string]string{
				"suparship.io/type":    "project",
				"suparship.io/project": p.Metadata.Name,
			},
		},
		Data: map[string]string{
			ConfigMapKey: string(data),
		},
	}

	existing, err := s.client.CoreV1().ConfigMaps(Namespace).Get(ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.client.CoreV1().ConfigMaps(Namespace).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating configmap %s/%s: %w", Namespace, cmName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking configmap %s/%s: %w", Namespace, cmName, err)
	}

	existing.Labels = cm.Labels
	existing.Data = cm.Data
	_, err = s.client.CoreV1().ConfigMaps(Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating configmap %s/%s: %w", Namespace, cmName, err)
	}
	return nil
}

func (s *K8sStore) Delete(ctx context.Context, name string) error {
	cmName := ConfigMapPrefix + name
	err := s.client.CoreV1().ConfigMaps(Namespace).Delete(ctx, cmName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("project %q not found", name)
	}
	if err != nil {
		return fmt.Errorf("deleting configmap %s/%s: %w", Namespace, cmName, err)
	}
	return nil
}
