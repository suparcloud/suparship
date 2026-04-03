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
type RuntimeStatusReader interface {
	GetServiceStatus(ctx context.Context, projectName, serviceName, environment string) (*ServiceStatus, error)
}

// LogReader retrieves recent log output from running service containers.
//
// The K8s implementation proxies pod log streams via client-go. The fake
// implementation returns a small set of static log lines for UI development
// and testing.
type LogReader interface {
	GetLogs(ctx context.Context, projectName, serviceName, environment string, tailLines int) ([]LogLine, error)
}
