package domain

import "context"

// ProjectStore reads and persists project definitions.
//
// The K8s implementation stores each project as a ConfigMap in the
// suparship-system namespace. The fake implementation returns in-memory
// fixture projects seeded at startup.
type ProjectStore interface {
	ListProjects(ctx context.Context) ([]*Project, error)
	GetProject(ctx context.Context, name string) (*Project, error)
	SaveProject(ctx context.Context, p *Project) error
	DeleteProject(ctx context.Context, name string) error
}

// ServiceStore reads and persists service definitions within a project.
//
// Services are nested under projects; all operations are scoped by project
// name. The K8s implementation co-locates services inside the project's
// ConfigMap. The fake implementation keeps services in memory.
//
// Deprecated: ServiceStore is a legacy interface retained for the
// service-centric compatibility layer (internal/compat). New features should
// use AppStore instead. ServiceStore will be removed once all service data has
// been migrated to the app model.
// See docs/migration-app-model.md for the transition guide.
type ServiceStore interface {
	ListServices(ctx context.Context, projectName string) ([]*Service, error)
	GetService(ctx context.Context, projectName, serviceName string) (*Service, error)
	SaveService(ctx context.Context, projectName string, svc *Service) error
	DeleteService(ctx context.Context, projectName, serviceName string) error
}

// TemplateStore reads available service templates.
//
// In MVP, templates are loaded from the local templates/ directory or
// embedded at build time. A future implementation may resolve templates
// from a remote registry.
type TemplateStore interface {
	ListTemplates(ctx context.Context) ([]*Template, error)
	GetTemplate(ctx context.Context, name string) (*Template, error)
}

// PreviewStore manages the preview environment lifecycle.
//
// The K8s implementation stores each preview as a ConfigMap and reads live
// status from the cluster. The fake implementation maintains previews in
// memory and returns a fixed "not_deployed" status.
type PreviewStore interface {
	ListPreviews(ctx context.Context) ([]*Preview, error)
	GetPreview(ctx context.Context, name string) (*Preview, error)
	CreatePreview(ctx context.Context, p *Preview) error
	DeletePreview(ctx context.Context, name string) error
}

// AppStore reads and persists app definitions within a project.
//
// Apps are nested under projects; all operations are scoped by project name.
// The fake implementation keeps apps in memory; a future K8s implementation
// will store each app as part of the project ConfigMap or as its own resource.
type AppStore interface {
	ListApps(ctx context.Context, projectName string) ([]*App, error)
	GetApp(ctx context.Context, projectName, appName string) (*App, error)
	ListAppEnvironments(ctx context.Context, projectName, appName string) ([]*AppEnvironment, error)
	// GetAppEnvironment resolves an environment by its name. For the well-known
	// environments (staging, prod) the name equals the environment type string.
	// Preview environments are resolved by their specific name (e.g. "pr-42").
	GetAppEnvironment(ctx context.Context, projectName, appName, envName string) (*AppEnvironment, error)
	// ListAppPreviews returns all preview AppEnvironments, optionally filtered
	// by project and/or app. Pass an empty string to skip that filter.
	ListAppPreviews(ctx context.Context, projectName, appName string) ([]*AppEnvironment, error)

	// DeleteApp removes an app and all its environment instances from a project.
	// Returns an error if the app does not exist.
	DeleteApp(ctx context.Context, projectName, appName string) error
	// SaveApp upserts an app definition within a project. Implementations
	// should return an error if the project does not exist. The ProjectName
	// field on app is set by the implementation to projectName.
	SaveApp(ctx context.Context, projectName string, app *App) error
	// SaveAppEnvironment upserts an environment instance for an app. The
	// caller is responsible for populating all fields; the implementation
	// stores the record verbatim and sets env.ProjectName = projectName.
	SaveAppEnvironment(ctx context.Context, projectName string, env *AppEnvironment) error
	// DeleteAppEnvironment removes an environment instance (typically a
	// preview) for the given app. Returns an error if the environment does
	// not exist.
	DeleteAppEnvironment(ctx context.Context, projectName, appName, envName string) error
}

// RuntimeStatusReader reads the live cluster state of a service.
//
// The K8s implementation queries Deployments and Ingresses in the
// {project}-{environment} namespace. The fake implementation returns a
// static ServiceStatus so the UI can be developed without a cluster.
//
// Deprecated: RuntimeStatusReader uses service-centric terminology. It is
// consumed by the legacy HTTP endpoints under /api/v1/projects/{project}/services/.
// New code should read runtime state through AppStore or a future
// AppRuntimeReader that speaks app/environment coordinates.
// See docs/migration-app-model.md for the transition guide.
type RuntimeStatusReader interface {
	GetServiceStatus(ctx context.Context, projectName, serviceName, environment string) (*ServiceStatus, error)
}

// ClusterStore manages the registry of Kubernetes clusters that suparShip
// deploys to. The tooling cluster (where suparShip itself runs) is not
// registered here — only workload clusters are.
//
// The K8s implementation stores cluster metadata as ConfigMaps and kubeconfigs
// as Secrets in the suparship-system namespace, and registers each cluster with
// ArgoCD by writing the corresponding ArgoCD cluster Secret. The fake
// implementation keeps clusters in memory for local development.
type ClusterStore interface {
	// ListClusters returns all registered clusters.
	ListClusters(ctx context.Context) ([]Cluster, error)
	// GetCluster returns a single cluster by name.
	GetCluster(ctx context.Context, name string) (*Cluster, error)
	// CreateCluster registers a new cluster and stores its kubeconfig.
	// The kubeconfig is used to build a k8s client for log streaming and to
	// register the cluster with ArgoCD.
	CreateCluster(ctx context.Context, cluster Cluster, kubeconfig []byte) error
	// DeleteCluster removes a cluster registration. Returns an error if the
	// cluster does not exist or has apps currently deployed.
	DeleteCluster(ctx context.Context, name string) error
}

// LogReader retrieves recent log output from running service containers.
//
// The K8s implementation proxies pod log streams via client-go. The fake
// implementation returns a small set of static log lines for UI development
// and testing.
//
// Deprecated: LogReader uses service-centric terminology and is consumed by the
// legacy /api/v1/projects/{project}/services/{service}/logs endpoint. New code
// should use the app-scoped logs endpoint
// (GET /api/v1/projects/{project}/apps/{app}/logs) and its backing provider.
// See docs/migration-app-model.md for the transition guide.
type LogReader interface {
	GetLogs(ctx context.Context, projectName, serviceName, environment string, tailLines int) ([]LogLine, error)
}
