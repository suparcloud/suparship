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
