package gitops

import "github.com/suparcloud/suparship/internal/domain"

// AppSetEnv parameterises one environment in an ApplicationSet.
// Each instance maps to one cluster in the env/cluster-centric Model B layout.
type AppSetEnv struct {
	// EnvName is the logical environment name, e.g. "staging".
	EnvName string
	// ClusterServer is the Kubernetes API server URL for this environment's cluster.
	// Written as spec.destination.server in generated Applications (single-cluster
	// / active mode).
	ClusterServer string
	// Clusters lists every cluster this environment fans out to (deployMode
	// "all"). When it has more than one entry, BuildArgoAppSet emits a matrix
	// generator (git-files × cluster list) producing one Application per
	// (app, cluster); otherwise it emits the single-cluster template using
	// ClusterServer. Empty/one entry preserves the legacy behaviour.
	Clusters []ClusterTarget
	// BaseDomain is the ingress base domain for apps in this environment,
	// e.g. "staging.acme.com". Used in values.yaml generation, not in the AppSet itself.
	BaseDomain string
}

// ClusterTarget identifies one destination cluster for an environment's fan-out,
// plus its routing overrides (multi-cloud: each cluster may use its own base
// domain, ingress class, and cert issuer).
type ClusterTarget struct {
	// Name is the registered cluster name (suparship.io/cluster), used in the
	// fan-out Application name suffix and the per-cluster values path.
	Name string
	// Server is the Kubernetes API server URL → spec.destination.server.
	Server string
	// BaseDomain is the cluster's default ingress DNS zone (empty = inherit env).
	BaseDomain string
	// RoutingProfiles overrides env/org routing for apps on this cluster, by
	// ExposeMode. Empty/sparse = inherit env → org.
	RoutingProfiles domain.RoutingProfiles
}

// AppSetOptions carries shared configuration for all ApplicationSet builders.
type AppSetOptions struct {
	// ArgoCDNamespace is the namespace where ApplicationSet objects are created.
	// Defaults to "argocd".
	ArgoCDNamespace string
	// TargetRevision is the Git ref ApplicationSets deploy. Defaults to "HEAD".
	TargetRevision string
	// SyncAutomated enables automated sync with prune and selfHeal on generated Applications.
	SyncAutomated bool
	// SubPath mirrors PublisherConfig.SubPath so the ApplicationSet's
	// Git File generator path glob and per-app source.path point at the
	// same directories the publisher writes to. Empty (default) = repo
	// root.
	SubPath string
	// ArgoAppNamePattern is the org ResourceNaming pattern for generated
	// Application names (tokens {project}/{app}/{env}/{cluster}). Empty →
	// secrets.DefaultArgoAppName ("{project}-{app}-{cluster}").
	ArgoAppNamePattern string
}

// fallbackClusterName is the synthetic cluster name used when an env has no
// resolvable cluster (unbound), so {cluster}-based Application names still
// render uniformly. The destination is then the in-cluster API server.
const fallbackClusterName = "in-cluster"

// appSetClusterTargets returns the env's destination clusters, guaranteeing at
// least one entry: an unbound env (no resolvable cluster) yields a single
// in-cluster fallback so per-cluster naming stays uniform.
func appSetClusterTargets(env AppSetEnv) []ClusterTarget {
	if len(env.Clusters) > 0 {
		return env.Clusters
	}
	server := env.ClusterServer
	if server == "" {
		server = defaultDestination
	}
	return []ClusterTarget{{Name: fallbackClusterName, Server: server}}
}

// matrixOverClusters wraps a git-file generator in a matrix × the env's cluster
// list, producing one Application per (app, cluster). Used for every stable-env
// AppSet now (even single-cluster) so the {{clusterName}} param is always
// available for the Application name and cluster label.
func matrixOverClusters(gitGen ApplicationSetGenerator, clusters []ClusterTarget) []ApplicationSetGenerator {
	elements := make([]map[string]string, 0, len(clusters))
	for _, c := range clusters {
		elements = append(elements, map[string]string{"clusterName": c.Name, "clusterServer": c.Server})
	}
	return []ApplicationSetGenerator{{
		Matrix: &MatrixGenerator{Generators: []ApplicationSetGenerator{
			gitGen,
			{List: &ListGenerator{Elements: elements}},
		}},
	}}
}

// ApplicationSet is a minimal, serializable representation of an ArgoCD
// ApplicationSet CRD. Only the fields required for suparShip's env/cluster-centric
// model are included.
type ApplicationSet struct {
	APIVersion string             `yaml:"apiVersion"`
	Kind       string             `yaml:"kind"`
	Metadata   ObjectMeta         `yaml:"metadata"`
	Spec       ApplicationSetSpec `yaml:"spec"`
}

// ApplicationSetSpec is the spec section of an ArgoCD ApplicationSet.
type ApplicationSetSpec struct {
	Generators []ApplicationSetGenerator `yaml:"generators"`
	Template   ApplicationSetTemplate    `yaml:"template"`
}

// ApplicationSetGenerator holds one generator configuration. Single-cluster
// envs use a bare Git File generator; multi-cluster envs use a Matrix of the
// Git File generator × a List of clusters.
type ApplicationSetGenerator struct {
	// Git is the Git File generator configuration.
	Git *GitFileGenerator `yaml:"git,omitempty"`
	// Matrix combines child generators (git-files × clusters) — one Application
	// per cross-product element.
	Matrix *MatrixGenerator `yaml:"matrix,omitempty"`
	// List enumerates literal parameter sets (one per destination cluster).
	List *ListGenerator `yaml:"list,omitempty"`
}

// MatrixGenerator is an ArgoCD matrix generator: the cross-product of its
// child generators' parameters.
type MatrixGenerator struct {
	Generators []ApplicationSetGenerator `yaml:"generators"`
}

// ListGenerator is an ArgoCD list generator: each element is a flat map of
// string params injected into the template (e.g. clusterName, clusterServer).
type ListGenerator struct {
	Elements []map[string]string `yaml:"elements"`
}

// GitFileGenerator discovers parameterisation files in a Git repository.
// Note: the ApplicationSet git generator uses "revision" (not "targetRevision"
// which is used in Application source specs). Using the wrong field name causes
// ArgoCD to reject the ApplicationSet with "revision: Required value".
type GitFileGenerator struct {
	RepoURL  string            `yaml:"repoURL"`
	Revision string            `yaml:"revision"`
	Files    []GitFilePathSpec `yaml:"files"`
}

// GitFilePathSpec is one path glob for the Git File generator.
type GitFilePathSpec struct {
	Path string `yaml:"path"`
}

// ApplicationSetTemplate is the Application template inside an ApplicationSet.
type ApplicationSetTemplate struct {
	Metadata ObjectMeta            `yaml:"metadata"`
	Spec     ApplicationSetAppSpec `yaml:"spec"`
}

// ApplicationSetAppSpec is the spec of Applications generated by the AppSet.
// It uses multi-source Applications (ArgoCD ≥ v2.6).
type ApplicationSetAppSpec struct {
	Project     string                 `yaml:"project"`
	Sources     []ApplicationSource    `yaml:"sources"`
	Destination ApplicationDestination `yaml:"destination"`
	SyncPolicy  *SyncPolicy            `yaml:"syncPolicy,omitempty"`
}

// BuildArgoAppSet generates the per-cluster stable-environment ApplicationSet
// for one environment directory (Model B layout).
//
// The generated ApplicationSet:
//   - Uses a Git File generator that discovers all app.yaml files under
//     "gitops-output/{envName}/*/*/app.yaml" to enumerate apps.
//   - Creates multi-source Applications: one source is the Helm chart at
//     "charts/{{template}}", the other is a ref source pointing at the
//     per-app values directory for "$appvalues/values.yaml".
//   - Hardcodes spec.destination.server to env.ClusterServer (constant per AppSet).
//   - Uses "{{namespace}}" as spec.destination.namespace — the resolved
//     namespace is written into each app's app.yaml at publish time, so each
//     app can have a different namespace within the same ApplicationSet.
//
// BuildArgoAppSet is a pure function — identical inputs produce identical output.
func BuildArgoAppSet(env AppSetEnv, repoURL string, opts AppSetOptions) *ApplicationSet {
	if opts.ArgoCDNamespace == "" {
		opts.ArgoCDNamespace = defaultArgoCDNS
	}
	if opts.TargetRevision == "" {
		opts.TargetRevision = defaultTargetRevision
	}
	if env.ClusterServer == "" {
		env.ClusterServer = defaultDestination
	}

	// Source 1: values ref — checks out the ENTIRE gitops repo so $appvalues
	// resolves to the repo root. No `path` is set intentionally: when `path` is
	// set, ArgoCD 2.13 erroneously resolves $ref to the repo root anyway, so we
	// make the intent explicit and use the full path in source 2's valueFiles.
	valuesRefSource := ApplicationSource{
		RepoURL:        repoURL,
		TargetRevision: opts.TargetRevision,
		Ref:            "appvalues",
	}

	// Source 2: Helm chart; reads per-app values from the repo via $appvalues.
	// The full path is used because $appvalues resolves to the repo root (see above).
	// Fan-out (deployMode "all") reads a per-cluster values file under
	// _clusters/{{clusterName}}/ so per-cluster overrides apply; single-cluster
	// reads the shared env values file.
	fanOut := len(env.Clusters) > 1
	valuesFilePath := "$appvalues/" + joinSubPath(opts.SubPath, "envs", env.EnvName, "{{project}}", "{{name}}", "values.yaml")
	if fanOut {
		valuesFilePath = "$appvalues/" + joinSubPath(opts.SubPath, "envs", env.EnvName, "_clusters", "{{clusterName}}", "{{project}}", "{{name}}", "values.yaml")
	}
	chartSource := ApplicationSource{
		RepoURL:        repoURL,
		Path:           joinSubPath(opts.SubPath, "charts", "{{chartPath}}"),
		TargetRevision: opts.TargetRevision,
		Helm: &HelmSource{
			ReleaseName: "{{name}}",
			ValueFiles:  []string{valuesFilePath},
		},
	}

	// Platform-generated manifests (the <app>-config ConfigMap + <app>-secrets
	// ExternalSecret) are NOT shipped here — they are owned by a separate
	// platform ApplicationSet (BuildPlatformAppSet) that syncs them from
	// _app-resources/, keeping this app Application a pure chart + values
	// release and decoupling application charts from suparship internals.

	var syncPolicy *SyncPolicy
	if opts.SyncAutomated {
		syncPolicy = &SyncPolicy{
			Automated:   &AutomatedSyncPolicy{Prune: true, SelfHeal: true},
			SyncOptions: []string{"CreateNamespace=true"},
		}
	} else {
		// Always create the destination namespace even without auto-sync so
		// ArgoCD can deploy into the correct namespace on the first manual sync.
		syncPolicy = &SyncPolicy{
			SyncOptions: []string{"CreateNamespace=true"},
		}
	}

	// Always fan the git-file generator over the env's cluster list (≥1 entry)
	// so the Application name and cluster label render per-cluster — adding a 2nd
	// cluster to an env never renames the 1st cluster's Application. The name
	// itself comes from the org ResourceNaming pattern. (The values-file path
	// above still switches shared vs per-cluster on actual fan-out, matching what
	// the publisher writes.)
	gitGen := ApplicationSetGenerator{
		Git: &GitFileGenerator{
			RepoURL:  repoURL,
			Revision: opts.TargetRevision,
			Files: []GitFilePathSpec{
				{Path: joinSubPath(opts.SubPath, "envs", env.EnvName, "*", "*", "app.yaml")},
			},
		},
	}

	clusters := appSetClusterTargets(env)
	generators := matrixOverClusters(gitGen, clusters)
	appName := RenderArgoAppNameTemplate(opts.ArgoAppNamePattern, env.EnvName)
	destServer := "{{clusterServer}}"

	return &ApplicationSet{
		APIVersion: argoAppSetAPIVersion,
		Kind:       "ApplicationSet",
		Metadata: ObjectMeta{
			Name:      env.EnvName,
			Namespace: opts.ArgoCDNamespace,
			Annotations: map[string]string{
				syncWaveAnnotation: "0",
			},
		},
		Spec: ApplicationSetSpec{
			Generators: generators,
			Template: ApplicationSetTemplate{
				Metadata: ObjectMeta{
					// Single-cluster name MUST mirror gitops.ApplicationName —
					// {project}-{app}-{env} — so two suparship projects can each
					// have an app called e.g. "color-app" without duplicate
					// Application names. Fan-out appends -{{clusterName}} to keep
					// each (app, cluster) Application unique.
					Name:      appName,
					Namespace: opts.ArgoCDNamespace,
					Labels: map[string]string{
						labelApp:     "{{name}}",
						labelProject: "{{project}}",
						labelEnv:     env.EnvName,
						labelCluster: "{{clusterName}}",
					},
					// Authorize the corresponding Kargo Stage to trigger ArgoCD syncs
					// for this Application. Promotion stays env-level (one stage per
					// env), so the value is the same across a fan-out env's clusters.
					Annotations: map[string]string{
						"kargo.akuity.io/authorized-stage": "kargo-{{project}}:{{name}}-" + env.EnvName,
					},
				},
				Spec: ApplicationSetAppSpec{
					Project:     "{{project}}",
					Sources:     []ApplicationSource{valuesRefSource, chartSource},
					Destination: ApplicationDestination{Server: destServer, Namespace: "{{namespace}}"},
					SyncPolicy:  syncPolicy,
				},
			},
		},
	}
}

// BuildArgoExternalAppSet generates the per-cluster ApplicationSet for
// external-mode apps — those whose template's engine.chart references a
// Helm registry rather than a chart bundled in the gitops repo.
//
// Differences from BuildArgoAppSet:
//
//   - The chart source uses source.repoURL + source.chart + source.targetRevision
//     to point at the Helm registry, instead of source.path pointing at
//     a charts/ directory in the gitops repo. ArgoCD's repo-server pulls
//     fresh from the registry; chart bytes never land in the gitops repo.
//
//   - The Git File generator's path glob is envs-external/{env}/{project}/{app}/
//     so this AppSet picks up only external-mode apps. The publisher
//     writes external-mode app.yaml files to that path and inline-mode
//     ones to envs/{env}/... — no overlap, so each app belongs to
//     exactly one AppSet without needing parameter selectors.
//
//   - Per-app platform manifests (env-configmap, external-secret) are
//     still written in the same env-relative directory, so the
//     directory source includes them the same way as inline-mode apps.
//
// Pure function — identical inputs produce identical output.
func BuildArgoExternalAppSet(env AppSetEnv, repoURL string, opts AppSetOptions) *ApplicationSet {
	if opts.ArgoCDNamespace == "" {
		opts.ArgoCDNamespace = defaultArgoCDNS
	}
	if opts.TargetRevision == "" {
		opts.TargetRevision = defaultTargetRevision
	}
	if env.ClusterServer == "" {
		env.ClusterServer = defaultDestination
	}

	// Source 1: values ref — same shape as inline-mode AppSet. Lets the
	// chart source overlay $appvalues onto the env-specific values.yaml.
	valuesRefSource := ApplicationSource{
		RepoURL:        repoURL,
		TargetRevision: opts.TargetRevision,
		Ref:            "appvalues",
	}

	// Source 2: chart from the Helm registry, parameterised per app.
	// chartRepoURL/chartName/chartVersion come from the per-app app.yaml
	// (AppMetadata.ChartRepoURL etc.) which the Git File generator
	// flattens into template parameters.
	fanOut := len(env.Clusters) > 1
	valuesFilePath := "$appvalues/" + joinSubPath(opts.SubPath, "envs-external", env.EnvName, "{{project}}", "{{name}}", "values.yaml")
	if fanOut {
		valuesFilePath = "$appvalues/" + joinSubPath(opts.SubPath, "envs-external", env.EnvName, "_clusters", "{{clusterName}}", "{{project}}", "{{name}}", "values.yaml")
	}
	chartSource := ApplicationSource{
		RepoURL:        "{{chartRepoURL}}",
		Chart:          "{{chartName}}",
		TargetRevision: "{{chartVersion}}",
		Helm: &HelmSource{
			ReleaseName: "{{name}}",
			ValueFiles:  []string{valuesFilePath},
		},
	}

	// Platform manifests are shipped by the platform ApplicationSet, not here
	// (same decoupling as inline-mode).

	var syncPolicy *SyncPolicy
	if opts.SyncAutomated {
		syncPolicy = &SyncPolicy{
			Automated:   &AutomatedSyncPolicy{Prune: true, SelfHeal: true},
			SyncOptions: []string{"CreateNamespace=true"},
		}
	} else {
		syncPolicy = &SyncPolicy{
			SyncOptions: []string{"CreateNamespace=true"},
		}
	}

	// Matches inline-mode: always fan over the env's cluster list (≥1) so the
	// Application name + cluster label render per-cluster; the name comes from the
	// org ResourceNaming pattern.
	gitGen := ApplicationSetGenerator{
		Git: &GitFileGenerator{
			RepoURL:  repoURL,
			Revision: opts.TargetRevision,
			Files: []GitFilePathSpec{
				{Path: joinSubPath(opts.SubPath, "envs-external", env.EnvName, "*", "*", "app.yaml")},
			},
		},
	}
	clusters := appSetClusterTargets(env)
	generators := matrixOverClusters(gitGen, clusters)
	appName := RenderArgoAppNameTemplate(opts.ArgoAppNamePattern, env.EnvName)
	destServer := "{{clusterServer}}"

	return &ApplicationSet{
		APIVersion: argoAppSetAPIVersion,
		Kind:       "ApplicationSet",
		Metadata: ObjectMeta{
			// Distinct name from the inline AppSet so both can coexist
			// in the argocd namespace targeting the same env.
			Name:      env.EnvName + "-external",
			Namespace: opts.ArgoCDNamespace,
			Annotations: map[string]string{
				syncWaveAnnotation: "0",
			},
		},
		Spec: ApplicationSetSpec{
			Generators: generators,
			Template: ApplicationSetTemplate{
				Metadata: ObjectMeta{
					Name:      appName,
					Namespace: opts.ArgoCDNamespace,
					Labels: map[string]string{
						labelApp:     "{{name}}",
						labelProject: "{{project}}",
						labelEnv:     env.EnvName,
						labelCluster: "{{clusterName}}",
					},
					Annotations: map[string]string{
						"kargo.akuity.io/authorized-stage": "kargo-{{project}}:{{name}}-" + env.EnvName,
					},
				},
				Spec: ApplicationSetAppSpec{
					Project:     "{{project}}",
					Sources:     []ApplicationSource{valuesRefSource, chartSource},
					Destination: ApplicationDestination{Server: destServer, Namespace: "{{namespace}}"},
					SyncPolicy:  syncPolicy,
				},
			},
		},
	}
}

// BuildArgoPreviewAppSet generates the previews ApplicationSet for a GitOps repo.
//
// The generated ApplicationSet:
//   - Uses a Git File generator on "gitops-output/previews/{project}/*/app.yaml".
//   - Each preview's app.yaml must contain: name, project, template, clusterServer, namespace.
//   - The destination.server and namespace come directly from the app.yaml parameters,
//     allowing previews to target any registered cluster.
//
// BuildArgoPreviewAppSet is a pure function — identical inputs produce identical output.
func BuildArgoPreviewAppSet(repoURL string, opts AppSetOptions) *ApplicationSet {
	if opts.ArgoCDNamespace == "" {
		opts.ArgoCDNamespace = defaultArgoCDNS
	}
	if opts.TargetRevision == "" {
		opts.TargetRevision = defaultTargetRevision
	}

	valuesRefSource := ApplicationSource{
		RepoURL:        repoURL,
		TargetRevision: opts.TargetRevision,
		Ref:            "previewvalues",
	}

	chartSource := ApplicationSource{
		RepoURL:        repoURL,
		Path:           joinSubPath(opts.SubPath, "charts", "{{chartPath}}"),
		TargetRevision: opts.TargetRevision,
		Helm: &HelmSource{
			ReleaseName: "{{appName}}",
			// The previews Git File generator reads each preview's app.yaml
			// (PreviewAppMetadata), whose key is "previewName" — not "name". The
			// values file lives at previews/{project}/{previewName}/values.yaml
			// (see Publisher.PublishAppPreview), so the path must interpolate
			// {{previewName}}; {{name}} would stay literal and 404 at render.
			ValueFiles: []string{"$previewvalues/" + joinSubPath(opts.SubPath, "previews", "{{project}}", "{{previewName}}", "values.yaml")},
		},
	}

	var syncPolicy *SyncPolicy
	if opts.SyncAutomated {
		syncPolicy = &SyncPolicy{
			Automated:   &AutomatedSyncPolicy{Prune: true, SelfHeal: true},
			SyncOptions: []string{"CreateNamespace=true"},
		}
	} else {
		syncPolicy = &SyncPolicy{
			SyncOptions: []string{"CreateNamespace=true"},
		}
	}

	return &ApplicationSet{
		APIVersion: argoAppSetAPIVersion,
		Kind:       "ApplicationSet",
		Metadata: ObjectMeta{
			Name:      "previews",
			Namespace: opts.ArgoCDNamespace,
			Annotations: map[string]string{
				syncWaveAnnotation: "0",
			},
		},
		Spec: ApplicationSetSpec{
			Generators: []ApplicationSetGenerator{
				{
					Git: &GitFileGenerator{
						RepoURL:  repoURL,
						Revision: opts.TargetRevision,
						Files: []GitFilePathSpec{
							{Path: joinSubPath(opts.SubPath, "previews", "*", "*", "app.yaml")},
						},
					},
				},
			},
			Template: ApplicationSetTemplate{
				Metadata: ObjectMeta{
					Name:      "{{appName}}-{{previewName}}",
					Namespace: opts.ArgoCDNamespace,
					Labels: map[string]string{
						labelApp:     "{{appName}}",
						labelProject: "{{project}}",
						labelEnv:     "{{previewName}}",
						labelEnvType: string(previewEnvType),
					},
				},
				Spec: ApplicationSetAppSpec{
					Project: "{{project}}",
					Sources: []ApplicationSource{valuesRefSource, chartSource},
					// clusterServer and namespace come from the preview's app.yaml.
					Destination: ApplicationDestination{
						Server:    "{{clusterServer}}",
						Namespace: "{{namespace}}",
					},
					SyncPolicy: syncPolicy,
				},
			},
		},
	}
}

// AppMetadata is written as app.yaml for each app in each env directory.
// The Git File generator reads these fields as template parameters.
//
// Two chart-source modes are routed via the optional ChartType /
// ChartRepoURL / ChartName / ChartVersion fields:
//
//   - Inline mode (today's behaviour): ChartType is "inline" or empty.
//     The chart lives at charts/{template}/ in the gitops repo; the
//     existing ApplicationSet (BuildArgoAppSet) renders an Application
//     whose source.path points there.
//   - External mode: ChartType is "external" with the chart fields
//     populated. The chart lives in a Helm registry, never in the
//     gitops repo; the parallel ApplicationSet (BuildArgoExternalAppSet)
//     renders a multi-source Application with source.repoURL +
//     source.chart + source.targetRevision pointing at the registry.
//
// Each ApplicationSet's generator filters on chartType so an app's
// app.yaml is consumed by exactly one AppSet.
type AppMetadata struct {
	Name     string `yaml:"name"`
	Project  string `yaml:"project"`
	Template string `yaml:"template"`
	// ChartPath is the gitops chart directory for this app relative to charts/,
	// i.e. "{template}/{versionDir}". The inline ApplicationSet substitutes it
	// into the chart source path ({{chartPath}}) so apps pinning different
	// versions of the same template resolve to distinct, non-colliding dirs.
	ChartPath string `yaml:"chartPath,omitempty"`
	// Namespace is the resolved Kubernetes namespace for this app+env instance.
	// Resolved at publish time by ResolveNamespace so each app can have a
	// different namespace even within the same ApplicationSet.
	Namespace string `yaml:"namespace"`

	// ChartType selects the chart-source flavour: "inline" (default,
	// omitted) or "external". Drives which ApplicationSet picks up this
	// app's app.yaml via generator selectors.
	ChartType string `yaml:"chartType,omitempty"`
	// ChartRepoURL is the Helm registry URL for external-mode apps.
	// Examples: "oci://ghcr.io/myorg/charts", "https://charts.acme.io".
	// Empty for inline-mode apps.
	ChartRepoURL string `yaml:"chartRepoURL,omitempty"`
	// ChartName is the chart name within the registry. Empty for inline.
	ChartName string `yaml:"chartName,omitempty"`
	// ChartVersion is the chart version pin. Empty for inline.
	ChartVersion string `yaml:"chartVersion,omitempty"`
}

// Constants for the ChartType values to keep callers consistent.
const (
	ChartTypeInline   = "inline"
	ChartTypeExternal = "external"
)

// PreviewAppMetadata is written as app.yaml for each preview instance.
// The previews ApplicationSet reads clusterServer and namespace from here.
type PreviewAppMetadata struct {
	AppName     string `yaml:"appName"`
	PreviewName string `yaml:"previewName"`
	Project     string `yaml:"project"`
	Template    string `yaml:"template"`
	// ChartPath mirrors AppMetadata.ChartPath — the preview reuses the stable
	// app's version-scoped chart directory (no separate chart copy).
	ChartPath     string `yaml:"chartPath,omitempty"`
	ClusterServer string `yaml:"clusterServer"`
	Namespace     string `yaml:"namespace"`
}

const (
	argoAppSetAPIVersion = "argoproj.io/v1alpha1"
	previewEnvType       = "preview"
)

// PlatformAppMeta is written as meta.yaml under _app-resources/.../{app}/. The
// platform ApplicationSet's Git File generator reads these fields as template
// parameters to fan out one Application per app that ships the
// platform-generated ConfigMap + ExternalSecret to the workload cluster.
type PlatformAppMeta struct {
	Name      string `yaml:"name"`
	Project   string `yaml:"project"`
	Namespace string `yaml:"namespace"`
	// ClusterServer is set only for previews (whose destination cluster is
	// per-preview); stable envs bake the cluster into the ApplicationSet.
	ClusterServer string `yaml:"clusterServer,omitempty"`
}

// platformResourcesInclude is the directory-source filter listing the
// platform-generated manifests ArgoCD should apply (plain-manifest mode).
const platformResourcesInclude = "{env-configmap.yaml,external-secret.yaml}"

func platformSyncPolicy(opts AppSetOptions) *SyncPolicy {
	if opts.SyncAutomated {
		return &SyncPolicy{
			Automated:   &AutomatedSyncPolicy{Prune: true, SelfHeal: true},
			SyncOptions: []string{"CreateNamespace=true"},
		}
	}
	return &SyncPolicy{SyncOptions: []string{"CreateNamespace=true"}}
}

// BuildPlatformAppSet builds the platform-owned ApplicationSet for one stable
// environment. It fans out one Application per app (discovered from
// _app-resources/{env}/*/*/meta.yaml) that syncs that app's platform-generated
// ConfigMap + ExternalSecret from _app-resources/{env}/{project}/{app}/ to the
// env's workload cluster and the app's namespace. This keeps platform manifests
// off the app's own (chart) Application.
func BuildPlatformAppSet(env AppSetEnv, repoURL string, opts AppSetOptions) *ApplicationSet {
	if opts.ArgoCDNamespace == "" {
		opts.ArgoCDNamespace = defaultArgoCDNS
	}
	if opts.TargetRevision == "" {
		opts.TargetRevision = defaultTargetRevision
	}
	if env.ClusterServer == "" {
		env.ClusterServer = defaultDestination
	}

	manifestsSource := ApplicationSource{
		RepoURL:        repoURL,
		Path:           joinSubPath(opts.SubPath, "_app-resources", env.EnvName, "{{project}}", "{{name}}"),
		TargetRevision: opts.TargetRevision,
		Directory:      &DirectorySource{Recurse: false, Include: platformResourcesInclude},
	}

	// Fan the per-app ConfigMap + ExternalSecret out to every cluster the env
	// targets, so pods on each cluster get their config/secrets (mirrors the
	// app chart AppSet's fan-out).
	gitGen := ApplicationSetGenerator{Git: &GitFileGenerator{
		RepoURL:  repoURL,
		Revision: opts.TargetRevision,
		Files: []GitFilePathSpec{
			{Path: joinSubPath(opts.SubPath, "_app-resources", env.EnvName, "*", "*", "meta.yaml")},
		},
	}}
	// Platform app name = the workload Application name + "-platform", so the
	// diagnostics reader can derive it from the rendered workload name. Always
	// per-cluster (matrix over the env's cluster list, ≥1).
	clusters := appSetClusterTargets(env)
	generators := matrixOverClusters(gitGen, clusters)
	appName := RenderArgoAppNameTemplate(opts.ArgoAppNamePattern, env.EnvName) + "-platform"
	destServer := "{{clusterServer}}"

	return &ApplicationSet{
		APIVersion: argoAppSetAPIVersion,
		Kind:       "ApplicationSet",
		Metadata: ObjectMeta{
			Name:        env.EnvName + "-platform",
			Namespace:   opts.ArgoCDNamespace,
			Annotations: map[string]string{syncWaveAnnotation: "0"},
		},
		Spec: ApplicationSetSpec{
			Generators: generators,
			Template: ApplicationSetTemplate{
				Metadata: ObjectMeta{
					Name:      appName,
					Namespace: opts.ArgoCDNamespace,
					Labels: map[string]string{
						labelApp:     "{{name}}",
						labelProject: "{{project}}",
						labelEnv:     env.EnvName,
						labelCluster: "{{clusterName}}",
					},
				},
				Spec: ApplicationSetAppSpec{
					Project:     "{{project}}",
					Sources:     []ApplicationSource{manifestsSource},
					Destination: ApplicationDestination{Server: destServer, Namespace: "{{namespace}}"},
					SyncPolicy:  platformSyncPolicy(opts),
				},
			},
		},
	}
}

// BuildPreviewPlatformAppSet builds the platform-owned ApplicationSet for
// preview apps. Like BuildPlatformAppSet but the destination cluster is
// per-preview ({{clusterServer}} from meta.yaml), mirroring the previews AppSet.
func BuildPreviewPlatformAppSet(repoURL string, opts AppSetOptions) *ApplicationSet {
	if opts.ArgoCDNamespace == "" {
		opts.ArgoCDNamespace = defaultArgoCDNS
	}
	if opts.TargetRevision == "" {
		opts.TargetRevision = defaultTargetRevision
	}

	manifestsSource := ApplicationSource{
		RepoURL:        repoURL,
		Path:           joinSubPath(opts.SubPath, "_app-resources", "previews", "{{project}}", "{{name}}"),
		TargetRevision: opts.TargetRevision,
		Directory:      &DirectorySource{Recurse: false, Include: platformResourcesInclude},
	}

	return &ApplicationSet{
		APIVersion: argoAppSetAPIVersion,
		Kind:       "ApplicationSet",
		Metadata: ObjectMeta{
			Name:        "previews-platform",
			Namespace:   opts.ArgoCDNamespace,
			Annotations: map[string]string{syncWaveAnnotation: "0"},
		},
		Spec: ApplicationSetSpec{
			Generators: []ApplicationSetGenerator{
				{Git: &GitFileGenerator{
					RepoURL:  repoURL,
					Revision: opts.TargetRevision,
					Files: []GitFilePathSpec{
						{Path: joinSubPath(opts.SubPath, "_app-resources", "previews", "*", "*", "meta.yaml")},
					},
				}},
			},
			Template: ApplicationSetTemplate{
				Metadata: ObjectMeta{
					Name:      "{{project}}-{{name}}-preview-platform",
					Namespace: opts.ArgoCDNamespace,
					Labels: map[string]string{
						labelProject: "{{project}}",
						labelEnvType: string(previewEnvType),
					},
				},
				Spec: ApplicationSetAppSpec{
					Project:     "{{project}}",
					Sources:     []ApplicationSource{manifestsSource},
					Destination: ApplicationDestination{Server: "{{clusterServer}}", Namespace: "{{namespace}}"},
					SyncPolicy:  platformSyncPolicy(opts),
				},
			},
		},
	}
}
