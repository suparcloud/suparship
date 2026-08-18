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

// composedPreviewsSubdir is the subtree under composedAppsDir that holds composed
// PREVIEW Application manifests (one per app per preview). A leading underscore
// keeps it disjoint from any stable env name (DNS labels can't start with "_"),
// so the per-stable-env composed root apps (which recurse _composed-apps/{env})
// never pick up preview manifests, and ONE static preview root app recurses this
// whole subtree. Layout: _composed-apps/_previews/{baseEnv}/{project}/{preview}/{app}/.
const composedPreviewsSubdir = "_previews"

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
	// KargoStage, when non-empty, is written verbatim as the
	// kargo.akuity.io/authorized-stage annotation so a Kargo Stage may trigger
	// syncs. It MUST be the project-qualified stage reference
	// "<kargo-project>:<stage>" (e.g. "kargo-voiceai:voiceai-lk-sh-staging") —
	// Kargo rejects an argocd-update whose target Application carries an
	// unqualified stage name. Empty = no annotation (direct apps).
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
	// One chart source per NON-stateful component (name-sorted for deterministic
	// output), each a distinct Helm release named {app}-{component} so component
	// resources never collide in the shared namespace. Stateful components (DBs)
	// are excluded — they render as their own prune-disabled Application via
	// BuildComponentApplication so a shared auto-sync can't prune their data.
	for _, c := range app.Spec.ComposedComponents() {
		if c.Stateful {
			continue
		}
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

// BuildComponentApplication renders ONE composed component as its own ArgoCD
// Application — used for stateful components (databases/caches) whose lifecycle
// must be decoupled from the app's shared multi-source Application. It has two
// sources (the $appvalues ref + the component's own chart source, with the same
// release name and value file it would get inside the composed app) and prune
// DISABLED, so a shared auto-sync can never prune the DB's resources on drift. No
// Kargo annotation — stateful components carry no Images, so they're pinned/direct.
// (Data survival on component REMOVAL still requires the chart to mark its PVC
// helm.sh/resource-policy: keep.) Pure function — identical inputs, identical output.
func BuildComponentApplication(app *domain.App, c domain.ComponentSpec, opts ComposedBuildOptions) *Application {
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

	sources := []ApplicationSource{
		{RepoURL: opts.RepoURL, TargetRevision: tr, Ref: "appvalues"},
		{
			RepoURL:        opts.RepoURL,
			Path:           joinSubPath(opts.SubPath, "charts", chartPathFor(c.Template.Name, c.Template.Version)),
			TargetRevision: tr,
			Helm: &HelmSource{
				ReleaseName: app.Name + "-" + c.Name,
				ValueFiles:  []string{"$appvalues/" + opts.ComponentValues[c.Name]},
			},
		},
	}

	var syncPolicy *SyncPolicy
	if opts.SyncAutomated {
		// Prune disabled: never remove the DB's resources on drift/sync. SelfHeal
		// stays on so config drift is still corrected.
		syncPolicy = &SyncPolicy{
			Automated:   &AutomatedSyncPolicy{Prune: false, SelfHeal: true},
			SyncOptions: []string{"CreateNamespace=true"},
		}
	} else {
		syncPolicy = &SyncPolicy{SyncOptions: []string{"CreateNamespace=true"}}
	}

	return &Application{
		APIVersion: argoAPIVersion,
		Kind:       argoKind,
		Metadata: ObjectMeta{
			Name:      opts.AppName + "-" + c.Name,
			Namespace: argoNS,
			Labels: map[string]string{
				labelApp:     app.Name,
				labelProject: app.ProjectName,
				labelEnv:     opts.EnvName,
				labelCluster: opts.ClusterName,
			},
		},
		Spec: ApplicationSpec{
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

// BuildComposedPreviewRootApp is the single, static App-of-Apps that discovers
// EVERY composed app's preview Application manifests: a recurse directory source
// over _composed-apps/_previews. It parallels the single-source `previews`
// ApplicationSet — written once (idempotent) into _infra/, discovered by the
// platform root App-of-Apps, and it auto-prunes a preview's Application when that
// preview's manifest subtree is removed on teardown.
func BuildComposedPreviewRootApp(repoURL string, opts AppSetOptions) *Application {
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
		// AllowEmpty: an empty previews dir is this app's normal state (no
		// open PRs) — without it ArgoCD refuses the sync that prunes the
		// last preview and its workloads zombie. Pairs with the .gitkeep
		// that keeps the source path present (ensureComposedPreviewsRoot).
		syncPolicy = &SyncPolicy{Automated: &AutomatedSyncPolicy{Prune: true, SelfHeal: true, AllowEmpty: true}}
	}

	return &Application{
		APIVersion: argoAPIVersion,
		Kind:       argoKind,
		Metadata: ObjectMeta{
			Name:      "previews-composed",
			Namespace: argoNS,
		},
		Spec: ApplicationSpec{
			Project: suparshipSystemProject,
			Source: ApplicationSource{
				RepoURL:        repoURL,
				Path:           joinSubPath(opts.SubPath, composedAppsDir, composedPreviewsSubdir),
				TargetRevision: tr,
				Directory:      &DirectorySource{Recurse: true},
			},
			Destination: ApplicationDestination{Server: defaultDestination, Namespace: argoNS},
			SyncPolicy:  syncPolicy,
		},
	}
}
