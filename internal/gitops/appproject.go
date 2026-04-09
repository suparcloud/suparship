// Package gitops — AppProject builder.
//
// BuildArgoAppProject generates a minimal ArgoCD AppProject CRD for a given
// suparship project.  The generated project:
//
//   - Allows any sourceRepo (wildcard) so new gitops repos can be added
//     without updating the project config.
//   - Allows deployment to any namespace whose name starts with the project
//     name, as well as a bare wildcard so ad-hoc environments work.
//   - Carries a sync-wave annotation of "-1" so ArgoCD always creates the
//     AppProject before any Application that references it.
//
// This file intentionally avoids importing the ArgoCD SDK to keep the
// dependency surface small and the types easy to audit.
package gitops

// AppProject is a minimal, serializable representation of an ArgoCD
// AppProject CRD. Only the fields needed by suparShip are included.
type AppProject struct {
	APIVersion string         `json:"apiVersion" yaml:"apiVersion"`
	Kind       string         `json:"kind"       yaml:"kind"`
	Metadata   ObjectMeta     `json:"metadata"   yaml:"metadata"`
	Spec       AppProjectSpec `json:"spec"       yaml:"spec"`
}

// AppProjectSpec mirrors the relevant fields of the ArgoCD AppProject spec.
type AppProjectSpec struct {
	// Description is a human-readable description of the project.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// SourceRepos lists the Git repositories that Applications in this project
	// may sync from.  Use ["*"] to allow any repository.
	SourceRepos []string `json:"sourceRepos" yaml:"sourceRepos"`
	// Destinations lists the clusters and namespaces that Applications in this
	// project may deploy to.
	Destinations []AppProjectDestination `json:"destinations" yaml:"destinations"`
	// ClusterResourceWhitelist lists cluster-scoped resource types that the
	// project is allowed to manage.  An empty list denies all cluster-scoped
	// resources.
	ClusterResourceWhitelist []GroupKind `json:"clusterResourceWhitelist,omitempty" yaml:"clusterResourceWhitelist,omitempty"`
	// NamespaceResourceWhitelist lists namespace-scoped resource types that the
	// project may manage.  Omitting it (nil) means all namespace resources are
	// allowed (ArgoCD default).
	NamespaceResourceWhitelist []GroupKind `json:"namespaceResourceWhitelist,omitempty" yaml:"namespaceResourceWhitelist,omitempty"`
}

// AppProjectDestination mirrors argoproj.io/v1alpha1 ApplicationDestination
// inside the AppProject spec.
type AppProjectDestination struct {
	// Server is the Kubernetes API endpoint.  Use "*" to match any cluster.
	Server string `json:"server" yaml:"server"`
	// Namespace pattern.  ArgoCD supports glob matching ("*", "foo-*", etc.).
	Namespace string `json:"namespace" yaml:"namespace"`
}

// GroupKind represents a Kubernetes GroupKind pair used in whitelist entries.
type GroupKind struct {
	Group string `json:"group" yaml:"group"`
	Kind  string `json:"kind"  yaml:"kind"`
}

const (
	argoAppProjectKind = "AppProject"

	// syncWaveAnnotation is the ArgoCD hook annotation that controls the order
	// in which resources are applied during a sync.  A wave of -1 means
	// AppProject is applied before all wave-0 Applications.
	syncWaveAnnotation = "argocd.argoproj.io/sync-wave"
	appProjectSyncWave = "-1"
)

// AppProjectOptions carries configuration for BuildArgoAppProject.
type AppProjectOptions struct {
	// Description is an optional free-text description of the project.
	Description string
	// ArgoCDNamespace is the namespace in which the AppProject is created.
	// Defaults to "argocd".
	ArgoCDNamespace string
	// DestinationServer is the Kubernetes API server URL allowed for
	// deployment.  Defaults to "https://kubernetes.default.svc".
	DestinationServer string
	// NamespaceGlob is the namespace glob that Applications in this project
	// may deploy to.  Defaults to "*" (all namespaces).
	NamespaceGlob string
	// AllowClusterResources controls whether cluster-scoped resources (e.g.
	// Namespace, ClusterRole) may be managed.  Default: true.
	AllowClusterResources bool
}

// BuildArgoAppProject creates an ArgoCD AppProject for a suparship project.
//
// The generated object:
//   - Is placed in opts.ArgoCDNamespace (default "argocd")
//   - Allows any source repository ("*")
//   - Allows deployment to opts.DestinationServer / opts.NamespaceGlob (defaults: in-cluster / "*")
//   - Carries argocd.argoproj.io/sync-wave: "-1" so it is applied before
//     any Application that references it in the same sync
//
// BuildArgoAppProject is a pure function — identical inputs always produce
// identical output.
func BuildArgoAppProject(projectName string, opts AppProjectOptions) *AppProject {
	if opts.ArgoCDNamespace == "" {
		opts.ArgoCDNamespace = defaultArgoCDNS
	}
	if opts.DestinationServer == "" {
		opts.DestinationServer = defaultDestination
	}
	if opts.NamespaceGlob == "" {
		opts.NamespaceGlob = "*"
	}

	annotations := map[string]string{
		syncWaveAnnotation: appProjectSyncWave,
	}

	spec := AppProjectSpec{
		Description: opts.Description,
		SourceRepos: []string{"*"},
		Destinations: []AppProjectDestination{
			{
				Server:    opts.DestinationServer,
				Namespace: opts.NamespaceGlob,
			},
		},
	}

	if opts.AllowClusterResources {
		spec.ClusterResourceWhitelist = []GroupKind{
			{Group: "*", Kind: "*"},
		}
	}

	return &AppProject{
		APIVersion: argoAPIVersion,
		Kind:       argoAppProjectKind,
		Metadata: ObjectMeta{
			Name:        projectName,
			Namespace:   opts.ArgoCDNamespace,
			Annotations: annotations,
		},
		Spec: spec,
	}
}
