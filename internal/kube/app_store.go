// Package kube — app_store.go provides a Kubernetes-backed implementation of
// domain.AppStore. Apps and their environment instances are persisted as
// ConfigMaps in the suparship-system namespace following the same conventions
// used by project.K8sStore.
//
// ConfigMap naming:
//
//	suparship-app-{project}-{app}             — app definition (app.json)
//	suparship-appenv-{project}-{app}-{env}    — environment instance (env.json)
//
// Labels on all ConfigMaps:
//
//	suparship.io/type    = "app" | "app-environment"
//	suparship.io/project = {projectName}
//	suparship.io/app     = {appName}          (on app-environment only)
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
	appConfigMapPrefix    = "suparship-app-"
	appEnvConfigMapPrefix = "suparship-appenv-"
	appConfigMapKey       = "app.json"
	appEnvConfigMapKey    = "env.json"
)

// K8sAppStore implements domain.AppStore using Kubernetes ConfigMaps.
// Each app is stored as suparship-app-{project}-{app} and each app
// environment as suparship-appenv-{project}-{app}-{env} in the
// suparship-system namespace.
type K8sAppStore struct {
	client kubernetes.Interface
}

// NewK8sAppStore creates a K8sAppStore backed by the given client.
func NewK8sAppStore(client kubernetes.Interface) *K8sAppStore {
	return &K8sAppStore{client: client}
}

// compile-time interface check.
var _ domain.AppStore = (*K8sAppStore)(nil)

func appConfigMapName(projectName, appName string) string {
	return appConfigMapPrefix + projectName + "-" + appName
}

func appEnvConfigMapName(projectName, appName, envName string) string {
	return appEnvConfigMapPrefix + projectName + "-" + appName + "-" + envName
}

// SaveApp upserts an app definition ConfigMap.
func (s *K8sAppStore) SaveApp(ctx context.Context, projectName string, app *domain.App) error {
	app.ProjectName = projectName

	data, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("marshaling app %q: %w", app.Name, err)
	}

	cmName := appConfigMapName(projectName, app.Name)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"suparship.io/type":    "app",
				"suparship.io/project": projectName,
				"suparship.io/app":     app.Name,
			},
		},
		Data: map[string]string{
			appConfigMapKey: string(data),
		},
	}

	existing, err := s.client.CoreV1().ConfigMaps(project.Namespace).Get(ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.client.CoreV1().ConfigMaps(project.Namespace).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating app configmap %s: %w", cmName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking app configmap %s: %w", cmName, err)
	}

	existing.Labels = cm.Labels
	existing.Data = cm.Data
	_, err = s.client.CoreV1().ConfigMaps(project.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating app configmap %s: %w", cmName, err)
	}
	return nil
}

// GetApp retrieves a single app by project and name.
func (s *K8sAppStore) GetApp(ctx context.Context, projectName, appName string) (*domain.App, error) {
	cmName := appConfigMapName(projectName, appName)
	cm, err := s.client.CoreV1().ConfigMaps(project.Namespace).Get(ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("app %q not found in project %q", appName, projectName)
	}
	if err != nil {
		return nil, fmt.Errorf("reading app configmap %s: %w", cmName, err)
	}

	return parseAppConfigMap(cm)
}

// ListApps returns all apps for a project.
func (s *K8sAppStore) ListApps(ctx context.Context, projectName string) ([]*domain.App, error) {
	cms, err := s.client.CoreV1().ConfigMaps(project.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "suparship.io/type=app,suparship.io/project=" + projectName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing app configmaps for project %q: %w", projectName, err)
	}

	apps := make([]*domain.App, 0, len(cms.Items))
	for i := range cms.Items {
		app, err := parseAppConfigMap(&cms.Items[i])
		if err != nil {
			return nil, fmt.Errorf("parsing app from configmap %s: %w", cms.Items[i].Name, err)
		}
		apps = append(apps, app)
	}
	return apps, nil
}

// SaveAppEnvironment upserts an app environment ConfigMap.
// Runtime-derived fields (Status) are stored as-is; callers should not write
// live cluster observations back through this method in steady state.
func (s *K8sAppStore) SaveAppEnvironment(ctx context.Context, projectName string, env *domain.AppEnvironment) error {
	env.ProjectName = projectName

	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshaling environment %q for app %q: %w", env.EnvName, env.AppName, err)
	}

	cmName := appEnvConfigMapName(projectName, env.AppName, env.EnvName)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"suparship.io/type":    "app-environment",
				"suparship.io/project": projectName,
				"suparship.io/app":     env.AppName,
				"suparship.io/env":     env.EnvName,
			},
		},
		Data: map[string]string{
			appEnvConfigMapKey: string(data),
		},
	}

	existing, err := s.client.CoreV1().ConfigMaps(project.Namespace).Get(ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = s.client.CoreV1().ConfigMaps(project.Namespace).Create(ctx, cm, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating app-environment configmap %s: %w", cmName, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking app-environment configmap %s: %w", cmName, err)
	}

	existing.Labels = cm.Labels
	existing.Data = cm.Data
	_, err = s.client.CoreV1().ConfigMaps(project.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating app-environment configmap %s: %w", cmName, err)
	}
	return nil
}

// GetAppEnvironment retrieves a single environment instance by name.
func (s *K8sAppStore) GetAppEnvironment(ctx context.Context, projectName, appName, envName string) (*domain.AppEnvironment, error) {
	cmName := appEnvConfigMapName(projectName, appName, envName)
	cm, err := s.client.CoreV1().ConfigMaps(project.Namespace).Get(ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	if err != nil {
		return nil, fmt.Errorf("reading app-environment configmap %s: %w", cmName, err)
	}

	return parseAppEnvConfigMap(cm)
}

// ListAppEnvironments returns all environment instances for an app.
func (s *K8sAppStore) ListAppEnvironments(ctx context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	cms, err := s.client.CoreV1().ConfigMaps(project.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "suparship.io/type=app-environment,suparship.io/project=" + projectName + ",suparship.io/app=" + appName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing app-environment configmaps for app %q/%q: %w", projectName, appName, err)
	}

	envs := make([]*domain.AppEnvironment, 0, len(cms.Items))
	for i := range cms.Items {
		env, err := parseAppEnvConfigMap(&cms.Items[i])
		if err != nil {
			return nil, fmt.Errorf("parsing env from configmap %s: %w", cms.Items[i].Name, err)
		}
		envs = append(envs, env)
	}
	return envs, nil
}

// ListAppPreviews returns all preview environment instances for an app.
func (s *K8sAppStore) ListAppPreviews(ctx context.Context, projectName, appName string) ([]*domain.AppEnvironment, error) {
	envs, err := s.ListAppEnvironments(ctx, projectName, appName)
	if err != nil {
		return nil, err
	}

	var previews []*domain.AppEnvironment
	for _, env := range envs {
		if env.EnvType == domain.AppEnvPreview {
			previews = append(previews, env)
		}
	}
	return previews, nil
}

// DeleteAppEnvironment removes an environment instance ConfigMap.
func (s *K8sAppStore) DeleteAppEnvironment(ctx context.Context, projectName, appName, envName string) error {
	cmName := appEnvConfigMapName(projectName, appName, envName)
	err := s.client.CoreV1().ConfigMaps(project.Namespace).Delete(ctx, cmName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("environment %q not found for app %q in project %q", envName, appName, projectName)
	}
	if err != nil {
		return fmt.Errorf("deleting app-environment configmap %s: %w", cmName, err)
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func parseAppConfigMap(cm *corev1.ConfigMap) (*domain.App, error) {
	raw, ok := cm.Data[appConfigMapKey]
	if !ok {
		return nil, fmt.Errorf("configmap %s missing key %q", cm.Name, appConfigMapKey)
	}
	var app domain.App
	if err := json.Unmarshal([]byte(raw), &app); err != nil {
		return nil, fmt.Errorf("unmarshaling app from configmap %s: %w", cm.Name, err)
	}
	return &app, nil
}

func parseAppEnvConfigMap(cm *corev1.ConfigMap) (*domain.AppEnvironment, error) {
	raw, ok := cm.Data[appEnvConfigMapKey]
	if !ok {
		return nil, fmt.Errorf("configmap %s missing key %q", cm.Name, appEnvConfigMapKey)
	}
	var env domain.AppEnvironment
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil, fmt.Errorf("unmarshaling app-environment from configmap %s: %w", cm.Name, err)
	}
	return &env, nil
}
