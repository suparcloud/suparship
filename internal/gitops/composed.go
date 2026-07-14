package gitops

import (
	"github.com/suparcloud/suparship/internal/domain"
)

// composedAppsDir is the manifest-only tree that holds the rendered ArgoCD
// Application manifests for composed apps, one per (app, cluster). It is kept
// disjoint from the values tree (envs/) so the per-env composed App-of-Apps can
// point a plain directory source at it and render ONLY Application manifests —
// no values.yaml, so no include-glob is needed.
const composedAppsDir = "_composed-apps"

// ComposedBuildOptions carries everything BuildComposedApplication needs beyond
// the app and its components to render one concrete, fully-resolved ArgoCD
// Application manifest for a single (app, cluster) target of a composed app.
type ComposedBuildOptions struct {
	// RepoURL is the gitops repository ArgoCD syncs from (chart sources + the
	// $appvalues reference source both point here).
	RepoURL string
	// TargetRevision is the git ref every source tracks. Empty → HEAD.
	TargetRevision string
	// SubPath mirrors PublisherConfig.SubPath so each chart source's path lines
	// up with where the publisher wrote the chart bytes. Empty = repo root.
	SubPath string
	// ArgoCDNamespace is where the Application object itself lives. Empty → argocd.
	ArgoCDNamespace string
	// AppName is the fully-rendered ArgoCD Application name (RenderArgoAppName).
	AppName string
	// EnvName / ClusterName populate the identity labels.
	EnvName     string
	ClusterName string
	// ClusterServer is the destination cluster API server. Empty → in-cluster.
	ClusterServer string
	// Namespace is the destination workload namespace (all component sources
	// deploy into the one namespace).
	Namespace string
	// KargoStage, when non-empty, is written as the kargo.akuity.io/authorized-stage
	// annotation so a Kargo Stage may trigger syncs. Empty = no annotation (MVP:
	// composed apps have no Kargo wiring yet).
	KargoStage string
	// SyncAutomated enables prune + selfHeal on the rendered Application.
	SyncAutomated bool
	// ComponentValues maps component name → repo-relative values.yaml path (the
	// file the publisher wrote for that component), referenced via
	// "$appvalues/<path>" from the component's chart source.
	ComponentValues map[string]string
}

// BuildComposedApplication renders the multi-source ArgoCD Application for one
// (app, cluster) target of a composed app: a values-ref source (checks out the
// whole gitops repo so $appvalues resolves to the repo root) plus one chart
// source per component, each with its own release name ({app}-{component}) and
// its own per-component values file. Pure function — identical inputs produce
// identical output.
//
// The Application uses spec.sources (not spec.source); the zero-valued singular
// Source is omitted by the YAML marshaller so ArgoCD never sees both set.
func BuildComposedApplication(app *domain.App, opts ComposedBuildOptions) *Application {
	tr := opts.TargetRevision
	if tr == "" {
		tr = defaultTargetRevision
	}
	argoNS := opts.ArgoCDNamespace
	if argoNS == "" {
		argoNS = defaultArgoCDNS
	}
	server := opts.ClusterServer
	if server == "" {
		server = defaultDestination
	}

	// Source 0: values ref — the whole repo, so component chart sources can
	// reference "$appvalues/<repo-relative values path>". No path is set (see
	// the note in BuildArgoAppSet: ArgoCD resolves $ref to the repo root).
	sources := make([]ApplicationSource, 0, len(app.Spec.Components)+1)
	sources = append(sources, ApplicationSource{
		RepoURL:        opts.RepoURL,
		TargetRevision: tr,
		Ref:            "appvalues",
	})
	// One chart source per component (name-sorted for deterministic output),
	// each a distinct Helm release named {app}-{component} so component
	// resources never collide in the shared namespace.
	for _, c := range app.Spec.ComposedComponents() {
		valuesFile := "$appvalues/" + opts.ComponentValues[c.Name]
		sources = append(sources, ApplicationSource{
			RepoURL:        opts.RepoURL,
			Path:           joinSubPath(opts.SubPath, "charts", chartPathFor(c.Template.Name, c.Template.Version)),
			TargetRevision: tr,
			Helm: &HelmSource{
				ReleaseName: app.Name + "-" + c.Name,
				ValueFiles:  []string{valuesFile},
			},
		})
	}

	var syncPolicy *SyncPolicy
	if opts.SyncAutomated {
		syncPolicy = &SyncPolicy{
			Automated:   &AutomatedSyncPolicy{Prune: true, SelfHeal: true},
			SyncOptions: []string{"CreateNamespace=true"},
		}
	} else {
		syncPolicy = &SyncPolicy{SyncOptions: []string{"CreateNamespace=true"}}
	}

	var annotations map[string]string
	if opts.KargoStage != "" {
		annotations = map[string]string{"kargo.akuity.io/authorized-stage": opts.KargoStage}
	}

	return &Application{
		APIVersion: argoAPIVersion,
		Kind:       argoKind,
		Metadata: ObjectMeta{
			Name:      opts.AppName,
			Namespace: argoNS,
			Labels: map[string]string{
				labelApp:     app.Name,
				labelProject: app.ProjectName,
				labelEnv:     opts.EnvName,
				labelCluster: opts.ClusterName,
			},
			Annotations: annotations,
		},
		Spec: ApplicationSpec{
			// Reuse the per-project AppProject the platform already writes
			// (writeEnvInfra → BuildArgoAppProject, metadata.name = project),
			// which authorizes every one of the project's env clusters.
			Project:     app.ProjectName,
			Sources:     sources,
			Destination: ApplicationDestination{Server: server, Namespace: opts.Namespace},
			SyncPolicy:  syncPolicy,
		},
	}
}

// BuildComposedRootApp renders the per-env App-of-Apps that discovers every
// composed app's rendered Application manifest for one environment. It is a
// plain directory-source Application pointing at _composed-apps/{env} (recurse),
// which renders each child application.yaml as an ArgoCD Application CR in the
// argocd namespace. The publisher writes it to _infra/{env}-composed-appset.yaml
// so the platform root App-of-Apps (which watches _infra/) picks it up. Pure
// function; idempotent per env.
func BuildComposedRootApp(envName, repoURL string, opts AppSetOptions) *Application {
	tr := opts.TargetRevision
	if tr == "" {
		tr = defaultTargetRevision
	}
	argoNS := opts.ArgoCDNamespace
	if argoNS == "" {
		argoNS = defaultArgoCDNS
	}

	var syncPolicy *SyncPolicy
	if opts.SyncAutomated {
		syncPolicy = &SyncPolicy{Automated: &AutomatedSyncPolicy{Prune: true, SelfHeal: true}}
	}

	return &Application{
		APIVersion: argoAPIVersion,
		Kind:       argoKind,
		Metadata: ObjectMeta{
			Name:      envName + "-composed",
			Namespace: argoNS,
		},
		Spec: ApplicationSpec{
			Project: suparshipSystemProject,
			Source: ApplicationSource{
				RepoURL:        repoURL,
				Path:           joinSubPath(opts.SubPath, composedAppsDir, envName),
				TargetRevision: tr,
				Directory:      &DirectorySource{Recurse: true},
			},
			Destination: ApplicationDestination{Server: defaultDestination, Namespace: argoNS},
			SyncPolicy:  syncPolicy,
		},
	}
}
