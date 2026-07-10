// Package gitops defines types and builder functions for generating ArgoCD
// manifests from suparship domain objects.
//
// # Design
//
// Each (App, Environment) pair maps to exactly one ArgoCD Application CRD.
// The naming convention is "<app>-<env>" (e.g. "hello-staging", "hello-pr-42").
//
// This package defines a minimal, self-contained representation of the ArgoCD
// Application CRD. It intentionally avoids importing the full ArgoCD SDK to
// keep the dependency footprint small and the types easy to test.
//
// # Usage
//
//	opts := gitops.BuildOptions{
//	    RepoURL:       "https://github.com/org/gitops",
//	    SyncAutomated: true,
//	}
//	argoApp := gitops.BuildArgoApplication(app, env, opts)
//
// # Determinism
//
// BuildArgoApplication is a pure function: the same inputs always produce
// identical output. No timestamps, random identifiers, or execution metadata
// are included.
package gitops

import (
	"strings"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/secrets"
)

const (
	argoAPIVersion        = "argoproj.io/v1alpha1"
	argoKind              = "Application"
	defaultArgoCDNS       = "argocd"
	defaultDestination    = "https://kubernetes.default.svc"
	defaultTargetRevision = "HEAD"

	// suparshipSystemProject is the ArgoCD AppProject used by all suparship
	// infrastructure Applications (root app, secrets-<cluster>, etc.).
	// It is created by the Helm chart with broad cluster/namespace permissions.
	// Workload Applications use per-project AppProjects with narrower scope.
	suparshipSystemProject = "suparship-system"

	// labelApp, labelProject, labelEnv are well-known labels attached to every
	// generated Application so that ArgoCD list views and external tooling can
	// filter by suparship identity without parsing the object name.
	labelApp     = "suparship.io/app"
	labelProject = "suparship.io/project"
	labelEnv     = "suparship.io/env"
	labelEnvType = "suparship.io/env-type"
	// labelCluster identifies the destination cluster of a generated Application,
	// so the per-cluster Applications of a fan-out env are distinguishable by
	// label (not only by the cluster suffix in the name).
	labelCluster = "suparship.io/cluster"
)

// Application is a minimal, serializable representation of an ArgoCD
// Application CRD. Only the fields required for suparship's app-per-environment
// model are included; advanced ArgoCD features (health checks, ignoreDifferences,
// multi-source, etc.) are out of scope for MVP.
type Application struct {
	APIVersion string          `json:"apiVersion" yaml:"apiVersion"`
	Kind       string          `json:"kind"       yaml:"kind"`
	Metadata   ObjectMeta      `json:"metadata"   yaml:"metadata"`
	Spec       ApplicationSpec `json:"spec"       yaml:"spec"`
}

// ApplicationName returns the canonical ArgoCD Application name for an
// app in a given environment: "<project>-<app>-<env>".
//
// The project prefix is REQUIRED for uniqueness: ArgoCD Applications all
// live in the argocd namespace, so two suparship projects each with an
// app named "color-app" would otherwise produce duplicate Application
// names and break the ApplicationSet reconciler with "duplicate name"
// errors. The kargo Stage namespace is per-project, so Stage names
// don't have this problem and KargoStageName stays "{app}-{env}".
func ApplicationName(projectName, appName, envName string) string {
	return projectName + "-" + appName + "-" + envName
}

// RenderArgoAppName renders a concrete ArgoCD Application name from an org
// ResourceNaming pattern (tokens {project}/{app}/{env}/{cluster}). Pass the
// effective pattern (ResourceNaming.EffectiveArgoAppName); cluster is the
// resolved cluster Name the app deploys to in this env. Used by the Go-side
// status/history/diagnostics readers and the Kargo argocd-update step so the
// name they look up matches what the ApplicationSet renders.
func RenderArgoAppName(pattern, projectName, appName, envName, clusterName string) string {
	if pattern == "" {
		pattern = secrets.DefaultArgoAppName
	}
	return secrets.RenderPattern(pattern, secrets.NamingParams{
		Project: projectName,
		App:     appName,
		Env:     envName,
		Cluster: clusterName,
	})
}

// RenderArgoAppNameTemplate renders an org ResourceNaming pattern into an
// ApplicationSet *template* string. An ApplicationSet is built per-env, so
// {env} is substituted with the literal env name, while {project}/{app}/{cluster}
// map to the ArgoCD generator params {{project}}/{{name}}/{{clusterName}} that
// ArgoCD expands per generated Application at render time.
func RenderArgoAppNameTemplate(pattern, envName string) string {
	if pattern == "" {
		pattern = secrets.DefaultArgoAppName
	}
	r := strings.NewReplacer(
		"{project}", "{{project}}",
		"{app}", "{{name}}",
		"{cluster}", "{{clusterName}}",
		"{env}", envName,
	)
	return r.Replace(pattern)
}

// ObjectMeta mirrors the Kubernetes ObjectMeta subset used by ArgoCD.
type ObjectMeta struct {
	Name        string            `json:"name"                  yaml:"name"`
	Namespace   string            `json:"namespace"             yaml:"namespace"`
	Labels      map[string]string `json:"labels,omitempty"      yaml:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// ApplicationSpec is the spec section of an ArgoCD Application.
type ApplicationSpec struct {
	// Project is the ArgoCD AppProject that scopes this Application.
	// Defaults to the suparship project name.
	Project     string                 `json:"project"     yaml:"project"`
	Source      ApplicationSource      `json:"source"      yaml:"source"`
	Destination ApplicationDestination `json:"destination" yaml:"destination"`
	// SyncPolicy is optional. When nil, sync must be triggered manually.
	SyncPolicy *SyncPolicy `json:"syncPolicy,omitempty" yaml:"syncPolicy,omitempty"`
}

// ApplicationSource describes one source ArgoCD pulls from. Fits both
// git-tree sources (Path set) and Helm-registry sources (Chart set);
// the two are mutually exclusive in ArgoCD's contract.
type ApplicationSource struct {
	// RepoURL is the source URL. For git sources this is HTTPS/SSH;
	// for Helm-registry sources it's the registry root, e.g.
	// "oci://ghcr.io/myorg/charts" or "https://charts.acme.io".
	RepoURL string `json:"repoURL" yaml:"repoURL"`
	// Path is the directory within a git repo. Mutually exclusive with
	// Chart. omitempty lets Helm-registry sources omit it cleanly.
	Path string `json:"path,omitempty" yaml:"path,omitempty"`
	// Chart is the chart name within a Helm registry. Mutually exclusive
	// with Path. Set by external-mode AppSets; left empty for git sources.
	Chart string `json:"chart,omitempty" yaml:"chart,omitempty"`
	// TargetRevision is the git ref for git sources, or the chart version
	// for Helm-registry sources.
	TargetRevision string `json:"targetRevision" yaml:"targetRevision"`
	// Ref marks this source as a reference-only source. Other sources can then
	// reference files from this source via "$ref-name/path" in their valueFiles.
	// When set, ArgoCD does NOT sync this source to the cluster.
	Ref string `json:"ref,omitempty" yaml:"ref,omitempty"`
	// Helm holds Helm-specific configuration. Used by both git sources
	// pointing at a chart directory and by Helm-registry sources.
	Helm *HelmSource `json:"helm,omitempty" yaml:"helm,omitempty"`
	// Directory configures plain-manifest behaviour: include filters so
	// only specific files in Path are applied. Used by the per-app
	// "platform manifests" source so app.yaml + values.yaml are skipped
	// (they're parameter/values files, not k8s manifests).
	Directory *DirectorySource `json:"directory,omitempty" yaml:"directory,omitempty"`
}

// DirectorySource configures ArgoCD's plain-directory rendering for an
// ApplicationSource. When kustomization.yaml is present in Path, ArgoCD
// auto-detects kustomize and uses it instead — the Include filter still
// limits which files kustomize considers.
type DirectorySource struct {
	// Recurse controls whether ArgoCD walks subdirectories. Default false:
	// suparship-emitted dirs have no nested manifest tree.
	Recurse bool `json:"recurse,omitempty" yaml:"recurse,omitempty"`
	// Include is a glob expression listing the file names to consider.
	// Single curly-brace alternation (e.g. "{a.yaml,b.yaml}") is supported
	// by ArgoCD's directory source.
	Include string `json:"include,omitempty" yaml:"include,omitempty"`
}

// HelmSource configures how ArgoCD renders the Helm chart at ApplicationSource.Path.
type HelmSource struct {
	// ReleaseName overrides the Helm release name. When empty ArgoCD uses the
	// Application name as the release name.
	ReleaseName string `json:"releaseName,omitempty" yaml:"releaseName,omitempty"`
	// ValueFiles is a list of values file paths relative to the chart root.
	// Files are merged left-to-right; later files take precedence.
	ValueFiles []string `json:"valueFiles,omitempty" yaml:"valueFiles,omitempty"`
	// Values is an inline YAML string of Helm values. Applied after ValueFiles.
	Values string `json:"values,omitempty" yaml:"values,omitempty"`
}

// ApplicationDestination describes where the Application should be deployed.
type ApplicationDestination struct {
	// Server is the Kubernetes API server URL.
	Server string `json:"server" yaml:"server"`
	// Namespace is the target Kubernetes namespace. Must match the namespace
	// derived from the domain.AppEnvironment.
	Namespace string `json:"namespace" yaml:"namespace"`
}

// SyncPolicy controls how ArgoCD synchronises the Application.
type SyncPolicy struct {
	// Automated enables automatic sync. When nil, sync is manual.
	Automated *AutomatedSyncPolicy `json:"automated,omitempty" yaml:"automated,omitempty"`
	// SyncOptions is a list of ArgoCD sync option strings.
	// The most commonly needed option is "CreateNamespace=true", which tells
	// ArgoCD to create the destination namespace if it does not already exist.
	SyncOptions []string `json:"syncOptions,omitempty" yaml:"syncOptions,omitempty"`
}

// AutomatedSyncPolicy configures automatic synchronisation behaviour.
type AutomatedSyncPolicy struct {
	// Prune removes cluster resources that are no longer present in Git.
	Prune bool `json:"prune" yaml:"prune"`
	// SelfHeal causes ArgoCD to re-sync when live state drifts from Git.
	SelfHeal bool `json:"selfHeal" yaml:"selfHeal"`
}

// BuildOptions carries the gitops and ArgoCD configuration needed by
// BuildArgoApplication. It is separated from the domain types so that
// the builder stays a pure function and is easy to test.
type BuildOptions struct {
	// RepoURL is the Git repository that ArgoCD syncs from. Required.
	RepoURL string

	// RepoPath is the directory within RepoURL that contains this app+env's
	// chart or rendered manifests. When empty a default path is derived:
	//   gitops-output/<project>/<app>/<env>
	RepoPath string

	// TargetRevision is the Git ref ArgoCD deploys. Defaults to "HEAD".
	TargetRevision string

	// ValuesFiles is a list of values file paths relative to RepoPath.
	// Common usage: []string{"values.yaml"}.
	ValuesFiles []string

	// InlineValues is an optional inline YAML string of Helm values.
	// Applied after ValuesFiles; takes precedence on key conflicts.
	// Typically derived from helmvalues.MapToHelmValues.
	InlineValues string

	// ArgoCDNamespace is the namespace where the Application object is
	// created. Defaults to "argocd".
	ArgoCDNamespace string

	// DestinationServer is the Kubernetes API server URL for the target
	// cluster. Defaults to "https://kubernetes.default.svc" (in-cluster).
	DestinationServer string

	// SyncAutomated enables automated sync with prune and selfHeal.
	SyncAutomated bool

	// ArgoCDProject is the ArgoCD AppProject name. When empty, the
	// suparship project name (app.ProjectName) is used.
	ArgoCDProject string

	// Annotations are merged into the Application metadata.annotations.
	// Values from this map are applied after the default suparship annotations.
	Annotations map[string]string

	// SubPath mirrors PublisherConfig.SubPath. Used to build the default
	// RepoPath when RepoPath is empty so the derived path matches whatever
	// the publisher writes. Empty SubPath places manifests at the repo
	// root.
	SubPath string
}

// BuildArgoApplication maps a suparship App and one of its AppEnvironments to
// an ArgoCD Application CRD object.
//
// Naming: the Application is named "<app.Name>-<env.EnvName>" and placed in
// opts.ArgoCDNamespace (default "argocd").
//
// Source: the Application syncs from opts.RepoURL at opts.RepoPath (or the
// derived default path "gitops-output/<project>/<app>/<env>").
//
// Destination: the Application deploys into env.Namespace on
// opts.DestinationServer (default in-cluster).
//
// Helm values: when opts.ValuesFiles or opts.InlineValues are provided, a
// HelmSource section is included; otherwise the source is treated as plain
// manifests.
//
// BuildArgoApplication is a pure function — identical inputs always produce
// identical output.
func BuildArgoApplication(app *domain.App, env domain.AppEnvironment, opts BuildOptions) *Application {
	opts = applyDefaults(opts, app, env)

	name := ApplicationName(app.ProjectName, app.Name, env.EnvName)

	labels := map[string]string{
		labelApp:     app.Name,
		labelProject: app.ProjectName,
		labelEnv:     env.EnvName,
		labelEnvType: string(env.EnvType),
	}

	var annotations map[string]string
	if len(opts.Annotations) > 0 {
		annotations = make(map[string]string, len(opts.Annotations))
		for k, v := range opts.Annotations {
			annotations[k] = v
		}
	}

	source := ApplicationSource{
		RepoURL:        opts.RepoURL,
		Path:           opts.RepoPath,
		TargetRevision: opts.TargetRevision,
	}
	if len(opts.ValuesFiles) > 0 || opts.InlineValues != "" {
		source.Helm = &HelmSource{
			// Use the app name alone as the Helm release name. Resources are
			// already isolated in the per-environment namespace (e.g.
			// "hello-staging"), so appending the env suffix is redundant and
			// causes Deployment names like "hello-staging" inside that
			// namespace. Keeping it as "hello" makes label selectors, pod
			// discovery, and runtime status lookups straightforward.
			ReleaseName: app.Name,
			ValueFiles:  opts.ValuesFiles,
			Values:      opts.InlineValues,
		}
	}

	var syncPolicy *SyncPolicy
	if opts.SyncAutomated {
		syncPolicy = &SyncPolicy{
			Automated: &AutomatedSyncPolicy{
				Prune:    true,
				SelfHeal: true,
			},
			// CreateNamespace=true is required because suparship uses a
			// dedicated namespace per app-environment (e.g. "hello-staging").
			// Without it ArgoCD fails the first sync with "namespace not found".
			SyncOptions: []string{"CreateNamespace=true"},
		}
	}

	return &Application{
		APIVersion: argoAPIVersion,
		Kind:       argoKind,
		Metadata: ObjectMeta{
			Name:        name,
			Namespace:   opts.ArgoCDNamespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: ApplicationSpec{
			Project: opts.ArgoCDProject,
			Source:  source,
			Destination: ApplicationDestination{
				Server:    opts.DestinationServer,
				Namespace: env.Namespace,
			},
			SyncPolicy: syncPolicy,
		},
	}
}

// applyDefaults fills in zero-valued fields in opts with sensible defaults
// derived from the app and env when available.
func applyDefaults(opts BuildOptions, app *domain.App, env domain.AppEnvironment) BuildOptions {
	if opts.ArgoCDNamespace == "" {
		opts.ArgoCDNamespace = defaultArgoCDNS
	}
	if opts.DestinationServer == "" {
		opts.DestinationServer = defaultDestination
	}
	if opts.TargetRevision == "" {
		opts.TargetRevision = defaultTargetRevision
	}
	if opts.RepoPath == "" {
		opts.RepoPath = defaultRepoPath(opts.SubPath, app.ProjectName, app.Name, env.EnvName)
	}
	if opts.ArgoCDProject == "" {
		opts.ArgoCDProject = app.ProjectName
	}
	return opts
}

// BuildArgoApplicationFromInstance maps a suparship App and an
// EnvironmentInstance to an ArgoCD Application CRD object.
//
// It is the EnvironmentInstance-aware counterpart to BuildArgoApplication,
// which accepts the transitional AppEnvironment type. New code should prefer
// this function; BuildArgoApplication will be deprecated once the migration
// from AppEnvironment is complete.
//
// The function delegates to BuildArgoApplication after projecting the relevant
// fields from inst, so all naming conventions, label schemes, and default
// application logic are identical between the two builders.
func BuildArgoApplicationFromInstance(app *domain.App, inst *domain.EnvironmentInstance, opts BuildOptions) *Application {
	env := domain.AppEnvironment{
		AppName:     inst.AppName,
		ProjectName: inst.ProjectName,
		EnvName:     inst.EnvName,
		EnvType:     inst.EnvType,
		Namespace:   inst.Namespace,
	}
	return BuildArgoApplication(app, env, opts)
}

// defaultRepoPath returns the conventional gitops output path for an app env,
// honouring the publisher's configured SubPath. Empty SubPath produces
// "<project>/<app>/<env>" (repo-root layout); a non-empty SubPath like
// "gitops/" produces "gitops/<project>/<app>/<env>".
func defaultRepoPath(subPath, project, app, env string) string {
	return joinSubPath(subPath, project, app, env)
}
