// Package kube provides Kubernetes-backed implementations of the server
// dependency interfaces for suparShip.
//
// # Storage model (MVP)
//
// Rather than introducing custom CRDs immediately, all persistent suparShip
// state is stored as Kubernetes ConfigMaps in the suparship-system namespace:
//
//	suparship-project-<name>   — project + service definitions (project.yaml)
//	suparship-preview-<name>   — preview environment metadata (preview.yaml)
//	suparship-template-<name>  — optional cluster-side template (template.yaml)
//
// Runtime status (replicas, images, URLs) is read live from Deployments and
// Ingresses in the conventional {project}-{environment} namespace.
//
// # Future CRD migration
//
// TODO: When suparShip CRDs are introduced (e.g. a typed Project or
// PreviewEnvironment resource), replace the ConfigMap-backed stores
// (project.K8sStore, preview.K8sStore) with typed CRD clients generated
// by controller-gen or written against controller-runtime. The interface
// signatures (project.Store, preview.Store) will not change, so the HTTP
// server and all tests remain unaffected by the swap.
//
// # Relationship to fake package
//
// This package is the production counterpart to internal/fake. Both
// implement the same set of server interfaces (project.Store,
// preview.Store, runtime.Provider, runtime.LogsProvider) but differ in
// their backing store:
//
//	fake  → in-memory seed data, no cluster required
//	kube  → live Kubernetes cluster via client-go
package kube

import (
	"github.com/suparcloud/suparship/internal/compat"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/runtime"
	"k8s.io/client-go/kubernetes"
)

// Compile-time checks: confirm the concrete K8s types satisfy the server
// interface contracts. These mirror the checks in internal/fake/adapters.go
// and make regressions visible at build time.
var (
	_ project.Store        = (*project.K8sStore)(nil)
	_ preview.Store        = (*preview.K8sStore)(nil)
	_ runtime.Provider     = (*runtime.K8sProvider)(nil)
	_ runtime.LogsProvider = (*runtime.K8sLogsProvider)(nil)
	_ domain.AppStore      = (*K8sAppStore)(nil)
)

// ServerDeps bundles all Kubernetes-backed implementations of the
// dependencies expected by server.Config. It is the production counterpart
// to fake.DevServerDeps.
//
// # Wiring guide
//
// Fields of this struct map directly to server.Config fields:
//
//	kubeDeps := kube.NewServerDeps(client)
//	srv := server.New(server.Config{
//	    ProjectStore:    kubeDeps.ProjectStore,
//	    PreviewStore:    kubeDeps.PreviewStore,
//	    RuntimeProvider: kubeDeps.RuntimeProvider,
//	    LogsProvider:    kubeDeps.LogsProvider,
//	    // Auth + org provider are separate because they need credentials:
//	    Authenticator: auth.NewK8sAuthenticator(client),
//	    OrgProvider:   rbac.NewK8sOrgProvider(client, fallbackOrg),
//	    // Templates: use kube.LoadTemplates(ctx, client) or tpl.LoadDir.
//	})
type ServerDeps struct {
	// ProjectStore reads and persists project + service definitions from
	// ConfigMaps in the suparship-system namespace.
	ProjectStore *project.K8sStore

	// PreviewStore reads and persists preview environment metadata from
	// ConfigMaps in the suparship-system namespace.
	PreviewStore *preview.K8sStore

	// RuntimeProvider reads live service status (replica counts, container
	// image, URLs) from Deployments and Ingresses in per-environment
	// namespaces ({project}-{environment}).
	RuntimeProvider *runtime.K8sProvider

	// LogsProvider streams pod log output via the Kubernetes API.
	LogsProvider *runtime.K8sLogsProvider

	// AppStore implements domain.AppStore via the compat bridge.
	// It reads app definitions from legacy service ConfigMaps and enriches
	// them with live runtime status and preview data. Once a native K8s
	// AppStore is available, replace this field with the native implementation.
	AppStore domain.AppStore
}

// NewServerDeps creates a ServerDeps bundle backed by the given Kubernetes
// client. Reads hit the cluster on every call; there is no in-process cache.
// The caller is responsible for providing Authenticator and OrgProvider
// separately (they require access to the admin bootstrap Secret).
func NewServerDeps(client kubernetes.Interface) *ServerDeps {
	projectStore := project.NewK8sStore(client)
	previewStore := preview.NewK8sStore(client)
	runtimeProvider := runtime.NewK8sProvider(client)

	nativeAppStore := NewK8sAppStore(client)
	appStore := compat.NewServiceBackedAppStore(
		nativeAppStore,
		&k8sServiceAdapter{projects: projectStore},
		&k8sProjectDomainAdapter{projects: projectStore},
		&k8sRuntimeStatusAdapter{provider: runtimeProvider},
		&k8sPreviewDomainAdapter{store: previewStore},
	)

	return &ServerDeps{
		ProjectStore:    projectStore,
		PreviewStore:    previewStore,
		RuntimeProvider: runtimeProvider,
		LogsProvider:    runtime.NewK8sLogsProvider(client),
		AppStore:        appStore,
	}
}
