package gitops

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/helmvalues"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/tpl"
	"gopkg.in/yaml.v3"
)

// PublisherConfig holds the configuration for the GitOps publisher.
type PublisherConfig struct {
	// RepoURL is the Git repository URL for cloning and pushing (host-accessible URL).
	RepoURL string
	// RepoUser is the Git username for HTTP authentication.
	RepoUser string
	// RepoPassword is the Git password or token for HTTP authentication.
	RepoPassword string
	// ArgoCDRepoURL is the URL ArgoCD uses to sync from the gitops repo.
	// This may be an internal cluster URL (e.g. http://gitea-http.gitea.svc.cluster.local:3000/...).
	// Falls back to RepoURL when empty.
	ArgoCDRepoURL string
	// Branch is the Git branch to commit to. Defaults to "main".
	Branch string
	// SyncAutomated enables automated sync (prune + selfHeal) on generated Applications.
	SyncAutomated bool
	// TemplatesDir is the local filesystem path to suparship templates.
	// When set, PublishApp syncs the Helm chart from templates/{name}/chart/
	// into charts/{name}/ in the gitops repo so ArgoCD can resolve them.
	TemplatesDir string
	// KargoGitRepoURL is the HTTPS Git URL Kargo uses for gitRepoUpdates.
	// Kargo v0.9+ requires HTTPS for credential-based git operations.
	// Falls back to ArgoCDRepoURL when empty.
	KargoGitRepoURL string
	// InsecureRegistry disables TLS verification for Kargo Warehouse image
	// subscriptions. Required when using an HTTP-only registry (e.g. local
	// kind-registry in dev mode).
	InsecureRegistry bool
	// ResourceNaming holds configurable naming patterns for K8s resources and
	// vault items. When zero-value, ResourceNaming defaults apply.
	// Used when writing per-app ExternalSecret and ConfigMap YAMLs.
	ResourceNaming secrets.ResourceNaming
	// OrgName is used for vault item naming in per-app ExternalSecrets.
	// When empty, "default" is used.
	OrgName string
	// BackendConfig is the org-level secret backend configuration.
	// When non-nil and the effective backend is 1Password, PublishEnvInfra
	// also writes ClusterSecretStore YAMLs to _infra/secret-stores/.
	BackendConfig *secrets.BackendConfig
	// ChartFetcher resolves a packaged Helm chart (chart.tgz) by template
	// name when no local TemplatesDir entry exists. Used for templates
	// imported via the BYO-chart flow, where the chart bytes live in a
	// cluster ConfigMap rather than on the suparship pod's filesystem.
	// Optional — when nil and TemplatesDir lacks the chart, syncChart is a
	// no-op (preserves prior behaviour).
	ChartFetcher ChartFetcher
	// TemplateLoader resolves a template's metadata (engine.chart shape,
	// version, etc.) by name. Used to detect external-mode templates so
	// the publisher can route them to envs-external/... and skip
	// syncChart. Optional — when nil, the publisher treats every
	// template as inline-mode (today's behaviour, back-compat).
	TemplateLoader TemplateLoader
	// Branding controls the platform identity stamped onto every manifest
	// the publisher writes (label values + custom label/annotation
	// domain). Zero value applies "suparship" / "suparship.io" defaults
	// — SRE contractors who white-label set Org.Branding once and the
	// publisher picks it up.
	Branding branding.Config
	// RoutingProfiles holds the org-level ingress + cert-manager profiles
	// keyed by ExposeMode (e.g. "internal", "external"). Per-env overrides
	// flow through AppPublishEnv.RoutingProfiles. When empty, the helmvalues
	// mapper falls back to the legacy Expose=true → nginx shim — useful for
	// installs that haven't migrated to the routing-profile model yet.
	RoutingProfiles domain.RoutingProfiles
	// AddonProfiles holds the org-level addon catalog keyed by addon
	// type (e.g. "redis", "postgres"). Each entry pins which wrapper
	// chart and provider serves apps that claim that type. Per-env
	// overrides ride on AppPublishEnv.AddonProfiles.
	AddonProfiles domain.AddonProfiles
	// SubPath is the optional sub-directory inside the gitops repo where
	// platform-managed manifests land. Empty (default) means manifests
	// land at the repo root (`<repo>/_infra/...`, `<repo>/{env}/...`,
	// `<repo>/charts/...`). Operators who want to keep platform output
	// inside a single subdirectory set e.g. "gitops/" — manifests then
	// land at `<repo>/gitops/_infra/...` etc.
	//
	// Leading/trailing slashes and "." / "./" are normalised away. The
	// publisher uses outputDir() and relativeOutputPath() to construct
	// every file/path so a single config flip propagates everywhere.
	SubPath string
}

// ChartFetcher returns the packaged Helm chart bytes for a template at a
// specific pinned version, or nil when no bundle exists. Implementations
// are free to consult any backing store (cluster ConfigMap, OCI registry,
// …); the publisher only needs the .tgz contents.
//
// Empty version means "whatever the current alias points at" — preserves
// behaviour for legacy apps that don't have Template.Version captured.
type ChartFetcher interface {
	LoadChartBundle(ctx context.Context, templateName, version string) ([]byte, error)
}

// TemplateLoader returns the template definition for a given name. Used by
// the publisher to detect external-mode templates (engine.chart points at
// a Helm registry) so it can route them to a separate gitops layout that
// the BuildArgoExternalAppSet picks up. Implementations should treat
// "not found" as a returned (nil, nil) rather than an error so the
// publisher can fall back to inline-mode behaviour without crashing.
type TemplateLoader interface {
	LoadTemplate(ctx context.Context, name string) (*tpl.Template, error)
}

// Publisher writes GitOps manifests to the GitOps repository and commits +
// pushes them so ArgoCD can pick them up automatically.
//
// # Repository layout (Model B — env/cluster-centric)
//
//	gitops-output/
//	  {envName}/
//	    appset.yaml                  ← ApplicationSet for this env's cluster
//	    {project}/
//	      appproject.yaml            ← ArgoCD AppProject
//	      {app}/
//	        app.yaml                 ← Git File generator parameters
//	        values.yaml              ← Helm values for this app/env
//	  previews/
//	    appset.yaml                  ← Preview ApplicationSet
//	    {project}/
//	      {previewName}/
//	        app.yaml                 ← Preview parameters (includes clusterServer + namespace)
//	        values.yaml              ← Helm values for this preview
//
// Each environment directory corresponds to one cluster. The ApplicationSet's
// Git File generator discovers all app.yaml files and deploys one Application
// per file to the cluster configured in appset.yaml.
type Publisher struct {
	cfg PublisherConfig
}

// SetOrgConfig updates the publisher's org-scoped configuration (naming
// patterns, backend config, org name, branding, routing profiles, and
// addon profiles). Thread-safe for callers that rebuild the publisher
// when org config changes; for concurrent use call this before handing
// the publisher to goroutines.
func (p *Publisher) SetOrgConfig(orgName string, naming secrets.ResourceNaming, backend *secrets.BackendConfig, brand branding.Config, routingProfiles domain.RoutingProfiles, addonProfiles domain.AddonProfiles) {
	p.cfg.OrgName = orgName
	p.cfg.ResourceNaming = naming
	p.cfg.BackendConfig = backend
	p.cfg.Branding = brand
	p.cfg.RoutingProfiles = routingProfiles
	p.cfg.AddonProfiles = addonProfiles
}

// usesUnifiedStore reports whether app ExternalSecrets should extract from the
// single per-cluster ClusterSecretStore (1Password backend) instead of the
// per-vault stores (k8s backend).
func (p *Publisher) usesUnifiedStore() bool {
	return p.cfg.BackendConfig != nil && p.cfg.BackendConfig.Effective() == secrets.Backend1Password
}

// NewPublisher creates a Publisher from cfg.
// Returns an error when RepoURL is empty (required).
func NewPublisher(cfg PublisherConfig) (*Publisher, error) {
	if cfg.RepoURL == "" {
		return nil, fmt.Errorf("gitops RepoURL is required")
	}
	if cfg.ArgoCDRepoURL == "" {
		cfg.ArgoCDRepoURL = cfg.RepoURL
	}
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	return &Publisher{cfg: cfg}, nil
}

// argoCDRepoURL returns the gitops repo URL as ArgoCD sees it inside the cluster.
func (p *Publisher) argoCDRepoURL() string {
	return p.cfg.ArgoCDRepoURL
}

// kargoGitRepoURL returns the HTTPS gitops repo URL for Kargo gitRepoUpdates.
func (p *Publisher) kargoGitRepoURL() string {
	if p.cfg.KargoGitRepoURL != "" {
		return p.cfg.KargoGitRepoURL
	}
	return p.cfg.ArgoCDRepoURL
}

// PublishEnvInfra writes the per-environment ApplicationSet and per-project
// AppProject to the shared _infra directory so that the ArgoCD root "App of
// Apps" can discover them with a simple non-filtered directory watch.
//
// Written files:
//   - gitops-output/_infra/{envName}-appset.yaml         — ApplicationSet for the cluster
//   - gitops-output/_infra/{project}-appproject.yaml     — AppProject (one per project, all env destinations merged)
//   - gitops-output/_infra/previews-appset.yaml          — Preview ApplicationSet (idempotent)
//
// Having all infra manifests under _infra/ avoids the problem of ArgoCD trying
// to apply per-app data files (app.yaml, values.yaml) as Kubernetes manifests.
// It also prevents AppProject duplication that occurs when one file is written
// per-environment for the same project.
//
// PublishEnvInfra is idempotent; it only creates a commit when content changes.
func (p *Publisher) PublishEnvInfra(ctx context.Context, projectName string, envs []AppSetEnv) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		// Collect destinations from all envs to build a single AppProject.
		destinations := make([]AppProjectDestination, 0, len(envs))
		// infra base path: all ArgoCD CRD manifests are written here so the
		// root "App of Apps" can watch a single directory with no include/exclude
		// filter required. Per-app data (app.yaml, values.yaml) stays in the
		// env-specific paths where ApplicationSet git generators discover it.
		infraDir := p.outputDir(repoDir, "_infra")

		for _, env := range envs {
			// Write {envName}-appset.yaml for this env.
			appSet := BuildArgoAppSet(env, p.cfg.ArgoCDRepoURL, AppSetOptions{
				SyncAutomated: p.cfg.SyncAutomated,
				SubPath:       p.cfg.SubPath,
			})
			appSetBytes, err := yaml.Marshal(appSet)
			if err != nil {
				return fmt.Errorf("marshal appset for env %s: %w", env.EnvName, err)
			}
			appSetPath := filepath.Join(infraDir, env.EnvName+"-appset.yaml")
			if err := p.writeFile(appSetPath, appSetBytes); err != nil {
				return err
			}
			slog.Debug("gitops: wrote appset", "path", appSetPath)

			// Platform-owned ApplicationSet that ships per-app ConfigMap +
			// ExternalSecret from _app-resources/ to this env's workload cluster,
			// decoupled from the app's chart Application.
			platformAppSet := BuildPlatformAppSet(env, p.cfg.ArgoCDRepoURL, AppSetOptions{
				SyncAutomated: p.cfg.SyncAutomated,
				SubPath:       p.cfg.SubPath,
			})
			platformBytes, err := yaml.Marshal(platformAppSet)
			if err != nil {
				return fmt.Errorf("marshal platform appset for env %s: %w", env.EnvName, err)
			}
			if err := p.writeFile(filepath.Join(infraDir, env.EnvName+"-platform-appset.yaml"), platformBytes); err != nil {
				return err
			}

			// Authorize every destination cluster the env fans out to (one in
			// active mode, all bound clusters in "all" mode).
			if len(env.Clusters) > 0 {
				for _, c := range env.Clusters {
					destinations = append(destinations, AppProjectDestination{Server: c.Server, Namespace: "*"})
				}
			} else {
				destinations = append(destinations, AppProjectDestination{Server: env.ClusterServer, Namespace: "*"})
			}
		}

		// Write a SINGLE {project}-appproject.yaml per project (not one per env)
		// to avoid ArgoCD rejecting the root app due to duplicate resource names.
		appProject := BuildArgoAppProject(projectName, AppProjectOptions{
			Description:           "suparShip project: " + projectName,
			AllowClusterResources: true,
			Destinations:          destinations,
		})
		appProjectBytes, err := yaml.Marshal(appProject)
		if err != nil {
			return fmt.Errorf("marshal appproject for project %s: %w", projectName, err)
		}
		appProjectPath := filepath.Join(infraDir, projectName+"-appproject.yaml")
		if err := p.writeFile(appProjectPath, appProjectBytes); err != nil {
			return err
		}
		slog.Debug("gitops: wrote appproject", "path", appProjectPath)

		// Write previews-appset.yaml (idempotent; only changes if not present).
		previewAppSet := BuildArgoPreviewAppSet(p.cfg.ArgoCDRepoURL, AppSetOptions{
			SyncAutomated: p.cfg.SyncAutomated,
			SubPath:       p.cfg.SubPath,
		})
		previewAppSetBytes, err := yaml.Marshal(previewAppSet)
		if err != nil {
			return fmt.Errorf("marshal previews appset: %w", err)
		}
		previewAppSetPath := filepath.Join(infraDir, "previews-appset.yaml")
		if err := p.writeFile(previewAppSetPath, previewAppSetBytes); err != nil {
			return err
		}

		// Platform-owned previews ApplicationSet (ships preview ConfigMap +
		// ExternalSecret from _app-resources/previews/).
		previewPlatformAppSet := BuildPreviewPlatformAppSet(p.cfg.ArgoCDRepoURL, AppSetOptions{
			SyncAutomated: p.cfg.SyncAutomated,
			SubPath:       p.cfg.SubPath,
		})
		previewPlatformBytes, err := yaml.Marshal(previewPlatformAppSet)
		if err != nil {
			return fmt.Errorf("marshal previews platform appset: %w", err)
		}
		if err := p.writeFile(filepath.Join(infraDir, "previews-platform-appset.yaml"), previewPlatformBytes); err != nil {
			return err
		}

		// ClusterSecretStores (global + per-env + per-cluster) are provisioned
		// by the env/cluster lifecycle hooks, not on every app publish.

		return p.commitAndPush(ctx, repoDir, "feat(infra): update appsets and appprojects for "+projectName)
	})
}

// ProjectNamespaceEnv carries the resolved namespace for one stable environment
// when emitting project-level Namespace manifests.
type ProjectNamespaceEnv struct {
	// EnvName is the logical environment name, e.g. "staging".
	EnvName string
	// Namespace is the resolved project namespace for this environment.
	// When empty, no manifest is written for this env.
	Namespace string
	// ClusterRef is the registered cluster this environment is bound to. When
	// non-empty it is added as the "suparship.io/cluster" namespace label so
	// the mittwald replicator can deliver cluster-scope Secrets/ConfigMaps
	// (matched via "replicate-to-matching: suparship.io/cluster=<name>") into
	// app namespaces hosted on this cluster.
	ClusterRef string
}

// namespaceManifest is the minimal YAML representation of a Kubernetes Namespace.
type namespaceManifest struct {
	APIVersion string            `yaml:"apiVersion"`
	Kind       string            `yaml:"kind"`
	Metadata   namespaceMetadata `yaml:"metadata"`
}

type namespaceMetadata struct {
	Name   string            `yaml:"name"`
	Labels map[string]string `yaml:"labels"`
}

// BuildProjectNamespaceManifest renders the Namespace YAML for one project
// environment with the appropriate platform labels. Pure function — no git
// or filesystem side effects — exposed so tests can verify label behaviour
// without exercising the cloned-repo plumbing.
//
// Labels written:
//
//	app.kubernetes.io/managed-by: <branding name>
//	<branding domain>/generator-version: <version>
//	<branding domain>/project: <project>
//	<branding domain>/cluster: <cluster> (when bound)
//
// The project + cluster labels match the replicator selectors used by the
// envconfig writers so that an UpperLevelEnvWriter's
// "replicate-to-matching: <domain>/project={name}" annotation finds these
// namespaces — keeping the platform working when an operator white-labels
// with a custom LabelDomain (suparship → acme.io requires both writers to
// emit the same prefix; that's why both consult the same Branding config).
func BuildProjectNamespaceManifest(projectName string, env ProjectNamespaceEnv, brand branding.Config) ([]byte, error) {
	labels := branding.MergeLabels(
		brand.ManagedByLabels(),
		map[string]string{brand.LabelKey("project"): projectName},
	)
	if env.ClusterRef != "" {
		labels[brand.LabelKey("cluster")] = env.ClusterRef
	}
	manifest := namespaceManifest{
		APIVersion: "v1",
		Kind:       "Namespace",
		Metadata: namespaceMetadata{
			Name:   env.Namespace,
			Labels: labels,
		},
	}
	return yaml.Marshal(manifest)
}

// PublishProjectNamespaces writes a Kubernetes Namespace manifest per stable
// environment into gitops-output/_infra/ when the resolved project namespace
// is non-empty. The root ArgoCD App-of-Apps watches _infra/*.yaml and syncs
// these to the cluster, so no direct kubectl apply is needed.
//
// Written files (one per env with a non-empty namespace):
//
//	gitops-output/_infra/{project}-ns-{env}.yaml
//
// Removing the NamespacePattern from a project causes the file to be deleted
// on the next call, which triggers ArgoCD to prune the Namespace resource.
//
// PublishProjectNamespaces is idempotent; it only creates a commit when content
// changes.
func (p *Publisher) PublishProjectNamespaces(ctx context.Context, projectName string, envs []ProjectNamespaceEnv) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		infraDir := p.outputDir(repoDir, "_infra")
		changed := false
		for _, env := range envs {
			filePath := filepath.Join(infraDir, projectName+"-ns-"+env.EnvName+".yaml")
			if env.Namespace == "" {
				// Remove stale manifest when the pattern has been cleared.
				if _, err := os.Stat(filePath); err == nil {
					if err := os.Remove(filePath); err != nil {
						return fmt.Errorf("remove project namespace manifest %s: %w", filePath, err)
					}
					changed = true
					slog.Debug("gitops: removed project namespace manifest", "path", filePath)
				}
				continue
			}
			data, err := BuildProjectNamespaceManifest(projectName, env, p.cfg.Branding)
			if err != nil {
				return fmt.Errorf("marshal namespace manifest for %s/%s: %w", projectName, env.EnvName, err)
			}
			if err := p.writeFile(filePath, data); err != nil {
				return err
			}
			changed = true
			slog.Debug("gitops: wrote project namespace manifest", "path", filePath, "namespace", env.Namespace)
		}
		if !changed {
			return nil
		}
		return p.commitAndPush(ctx, repoDir, "feat(infra): update project namespaces for "+projectName)
	})
}

// firstDeployEnvs returns the subset of envs that should have app.yaml +
// values.yaml written on initial app creation:
//   - all preview environments (they deploy immediately by definition)
//   - the first bound stable environment only (lowest Order, Name tiebreak)
//
// Higher stable environments are intentionally excluded from the initial
// publish so that ArgoCD does not deploy to them until an explicit promotion
// writes their files via PublishAppEnv. Kargo Stage CRs for all bound stable
// envs are written separately and unconditionally — the pipeline wiring is
// ready from day one even though the target files don't exist yet.
func firstDeployEnvs(envs []AppPublishEnv) []AppPublishEnv {
	result := make([]AppPublishEnv, 0, len(envs))

	// Always include preview envs — they are ephemeral and deploy immediately.
	for _, env := range envs {
		if env.EnvType == domain.AppEnvPreview {
			result = append(result, env)
		}
	}

	// Collect bound stable envs and sort to find the first one.
	stable := make([]AppPublishEnv, 0, len(envs))
	for _, env := range envs {
		if env.EnvType != domain.AppEnvPreview && env.Bound {
			stable = append(stable, env)
		}
	}
	sort.Slice(stable, func(i, j int) bool {
		if stable[i].Order != stable[j].Order {
			return stable[i].Order < stable[j].Order
		}
		return stable[i].EnvName < stable[j].EnvName
	})
	if len(stable) > 0 {
		result = append(result, stable[0])
	}
	return result
}

// PublishApp writes the per-app app.yaml and values.yaml for each environment,
// plus Kargo Warehouse and Stage CRs so promotions are wired automatically.
//
// On initial creation only the first bound stable environment (lowest Order)
// and any preview environments receive app.yaml + values.yaml. Higher stable
// environments are intentionally skipped so ArgoCD does not deploy to them
// until an explicit promotion writes their files via PublishAppEnv.
//
// Written files (first bound stable env + previews):
//   - gitops-output/{envName}/{project}/{app}/app.yaml
//   - gitops-output/{envName}/{project}/{app}/values.yaml
//
// Written Kargo infrastructure files (all bound stable envs):
//   - gitops-output/_infra/kargo/{project}-project.yaml
//   - gitops-output/_infra/kargo/{project}-{app}-warehouse.yaml
//   - gitops-output/_infra/kargo/{project}-{app}-{env}-stage.yaml
//
// PublishApp is idempotent; it only creates a commit when content changes.
func (p *Publisher) PublishApp(ctx context.Context, app *domain.App, envs []AppPublishEnv) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		// Only write app/values files for the first env (+ previews) on create.
		if err := p.publishAppFiles(repoDir, app, firstDeployEnvs(envs)); err != nil {
			return err
		}

		// External-mode templates have no local chart bytes — Argo's
		// repo-server pulls fresh from the Helm registry. Skip the
		// gitops-repo chart copy so we don't bloat history with bytes
		// nobody reads.
		mode, _ := p.resolveTemplateChartMode(app.Spec.Template.Name)
		if mode == AppMetadataChartTypeInline {
			// Sync the Helm chart into charts/{template}/{version}/ so
			// ArgoCD's ApplicationSet can resolve the chart path. The
			// version the app was created against is honored — bumping the
			// templates registry doesn't silently re-version every running
			// app's chart bytes, and two apps pinning different versions of
			// the same template get distinct, non-colliding chart dirs.
			if err := p.syncChart(ctx, repoDir, app.Spec.Template.Name, app.Spec.Template.Version); err != nil {
				return fmt.Errorf("sync chart for template %s@%s: %w", app.Spec.Template.Name, app.Spec.Template.Version, err)
			}
		}

		// Addon wrapper charts are inline templates too (e.g. "valkey"); sync
		// each so the addon Applications publishAppAddons emits can resolve
		// their charts/<chart>/latest/ path.
		if err := p.syncAddonCharts(ctx, repoDir, app, envs); err != nil {
			return err
		}

		// Write Kargo Warehouse + Stage CRs for all bound stable envs so the
		// full promotion pipeline is wired from day one.
		if err := p.publishKargoCRs(repoDir, app, envs); err != nil {
			return fmt.Errorf("write kargo CRs for %s/%s: %w", app.ProjectName, app.Name, err)
		}

		commitMsg := fmt.Sprintf("feat(apps): publish %s/%s\n\nCreated by suparShip.", app.ProjectName, app.Name)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// PublishAppEnv writes app.yaml and values.yaml for a single environment to
// the GitOps repo and commits. This is called on every explicit promotion so
// that the target environment's files land in Git before Kargo / ArgoCD act.
//
// PublishAppEnv is idempotent — if the files already contain identical content
// the resulting git commit is a no-op (stagedIsEmpty check in commitAndPush).
func (p *Publisher) PublishAppEnv(ctx context.Context, app *domain.App, env AppPublishEnv) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		if err := p.publishAppFiles(repoDir, app, []AppPublishEnv{env}); err != nil {
			return err
		}
		commitMsg := fmt.Sprintf("feat(apps): publish %s/%s to %s\n\nPromoted by suparShip.", app.ProjectName, app.Name, env.EnvName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// publishAppFiles writes app.yaml and values.yaml for each bound environment,
// and optionally platform-managed ExternalSecret + ConfigMap YAMLs when the
// env carries a StoreName (1Password-backed envs).
// Unbound environments are skipped with a warning log.
// This is the inner loop extracted from PublishApp for testability.
func (p *Publisher) publishAppFiles(repoDir string, app *domain.App, envs []AppPublishEnv) error {
	orgName := p.cfg.OrgName
	if orgName == "" {
		orgName = "default"
	}

	// Resolve the template once per call so external-mode routing is
	// consistent across all envs. TemplateLoader is optional — when nil
	// the publisher treats every template as inline-mode (back-compat).
	chartMode, chartRef := p.resolveTemplateChartMode(app.Spec.Template.Name)

	for _, env := range envs {
		if !env.Bound {
			slog.Warn("gitops: skipping publish for unbound env — assign a cluster via Settings > Environments",
				"app", app.Name, "env", env.EnvName)
			continue
		}

		// Resolve the namespace: use the pre-computed value from the caller
		// when set; fall back to the legacy "{app}-{env}" default.
		ns := env.Namespace
		if ns == "" {
			ns = app.Name + "-" + env.EnvName
		}

		// Write app.yaml — Git File generator parameters.
		appMeta := AppMetadata{
			Name:      app.Name,
			Project:   app.ProjectName,
			Template:  app.Spec.Template.Name,
			ChartPath: chartPathFor(app.Spec.Template.Name, app.Spec.Template.Version),
			Namespace: ns,
		}
		// External-mode apps carry the chart locator in app.yaml so
		// BuildArgoExternalAppSet's generator can render
		// {{chartRepoURL}}, {{chartName}}, {{chartVersion}} per app.
		if chartMode == AppMetadataChartTypeExternal && chartRef != nil {
			appMeta.ChartType = ChartTypeExternal
			appMeta.ChartRepoURL = chartRef.Repository
			appMeta.ChartName = chartRef.Name
			appMeta.ChartVersion = chartRef.Version
		}
		appMetaBytes, err := yaml.Marshal(appMeta)
		if err != nil {
			return fmt.Errorf("marshal app.yaml for env %s: %w", env.EnvName, err)
		}
		appMetaPath := p.envAppDir(chartMode, repoDir, env, app.ProjectName, app.Name, "app.yaml")
		if err := p.writeFile(appMetaPath, appMetaBytes); err != nil {
			return err
		}

		// Write values.yaml — Helm values with env-specific baseDomain and
		// the resolved namespace so secretName/configName are consistent.
		// Org-level profiles come from PublisherConfig (set by SetOrgConfig
		// from rbac.Org.RoutingProfiles); per-env overrides ride on
		// AppPublishEnv.RoutingProfiles. When both are empty, helmvalues
		// falls back to the legacy Expose=true → nginx shim.
		//
		// marshalValues computes the values for one cluster: the cluster's own
		// baseDomain + routing profiles override the env's (per-cluster /
		// multi-cloud), and its name drives per-cluster value overrides.
		marshalValues := func(c ClusterTarget) ([]byte, error) {
			baseDomain := env.BaseDomain
			if c.BaseDomain != "" {
				baseDomain = c.BaseDomain
			}
			hv := helmvalues.MapToHelmValuesForEnv(app, env.EnvName, env.EnvType, baseDomain, env.Namespace, c.Name, orgName, p.cfg.RoutingProfiles, env.RoutingProfiles, c.RoutingProfiles, p.cfg.AddonProfiles, env.AddonProfiles)
			return yaml.Marshal(hv)
		}
		if len(env.Clusters) > 1 {
			// Fan-out: one values.yaml per cluster under _clusters/<cluster>/,
			// each merged with that cluster's overrides + routing. The matrix
			// ApplicationSet points its valueFile at this path via {{clusterName}}.
			for _, c := range env.Clusters {
				hvBytes, err := marshalValues(c)
				if err != nil {
					return fmt.Errorf("marshal values.yaml for env %s cluster %s: %w", env.EnvName, c.Name, err)
				}
				cvPath := p.envAppDir(chartMode, repoDir, env, "_clusters", c.Name, app.ProjectName, app.Name, "values.yaml")
				if err := p.writeFile(cvPath, hvBytes); err != nil {
					return err
				}
			}
		} else {
			// Single cluster: use the resolved active cluster's routing when
			// present (so a lone remote cluster on another cloud still gets its
			// own domain/ingress), else fall back to the env's ClusterRef.
			target := ClusterTarget{Name: env.ClusterRef}
			if len(env.Clusters) == 1 {
				target = env.Clusters[0]
			}
			hvBytes, err := marshalValues(target)
			if err != nil {
				return fmt.Errorf("marshal values.yaml for env %s: %w", env.EnvName, err)
			}
			valuesPath := p.envAppDir(chartMode, repoDir, env, app.ProjectName, app.Name, "values.yaml")
			if err := p.writeFile(valuesPath, hvBytes); err != nil {
				return err
			}
		}

		// Platform-managed per-app resources (ConfigMap + ExternalSecret) are
		// written to the platform-owned _app-resources/ tree and shipped by the
		// platform ApplicationSet — NOT into the app's chart Application.
		appDir := p.envAppDir(chartMode, repoDir, env, app.ProjectName, app.Name)
		if err := p.writeAppPlatformResources(repoDir, appDir, app, ns, env); err != nil {
			return fmt.Errorf("writing platform resources for env %s: %w", env.EnvName, err)
		}

		// Per-addon claim: parallel ArgoCD Application alongside the
		// main app. Each gets app.yaml + values.yaml under
		// addons/<name>/. Existing ApplicationSet generators pick
		// them up; no AppSet schema change.
		if err := p.publishAppAddons(appDir, app, env, orgName); err != nil {
			return fmt.Errorf("writing addon files for env %s: %w", env.EnvName, err)
		}

		slog.Debug("gitops: wrote app files", "env", env.EnvName, "app", app.Name)
	}
	return nil
}

// writeAppPlatformResources writes the platform-managed ConfigMap + ExternalSecret
// (and a meta.yaml for the platform ApplicationSet generator) into the
// platform-owned tree at _app-resources/{env}/{project}/{app}/, then prunes any
// stale copies left in the app's own directory by older versions (which used to
// ship these alongside the chart). oldAppDir is the per-app chart directory.
//
// The ConfigMap data is env.EnvVars verbatim — the caller merges global → env →
// cluster scopes before publishing, so the committed YAML is the audit-trail for
// what the pod sees. The ExternalSecret merges all present scopes into one
// <app>-secrets Secret; nil (skipped) when no scope has keys.
func (p *Publisher) writeAppPlatformResources(
	repoDir, oldAppDir string,
	app *domain.App,
	namespace string,
	env AppPublishEnv,
) error {
	resDir := p.outputDir(repoDir, "_app-resources", env.EnvName, app.ProjectName, app.Name)
	esCfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:          app.Name,
		Namespace:    namespace,
		Env:          env.EnvName,
		Cluster:      env.ClusterRef,
		Presence:     env.ScopeKeys,
		UnifiedStore: p.usesUnifiedStore(),
		Branding:     p.cfg.Branding,
	})
	meta := PlatformAppMeta{Name: app.Name, Project: app.ProjectName, Namespace: namespace}
	if err := p.writePlatformDir(resDir, app.Name, namespace, env.EnvVars, esCfg, meta); err != nil {
		return err
	}
	// Migration: remove platform manifests that older publishers wrote into the
	// app's own (chart) directory.
	return p.pruneLegacyPlatformFiles(oldAppDir)
}

// writePlatformDir writes meta.yaml + the <app>-config ConfigMap + the
// <app>-secrets ExternalSecret (esCfg may be nil → pruned) into resDir.
func (p *Publisher) writePlatformDir(resDir, appName, namespace string, envVars map[string]string, esCfg *ESOExternalSecretConfig, meta PlatformAppMeta) error {
	metaBytes, err := yaml.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal platform meta: %w", err)
	}
	if err := p.writeFile(filepath.Join(resDir, "meta.yaml"), metaBytes); err != nil {
		return err
	}
	if err := p.WriteAppConfigMap(resDir, secrets.AppConfigMapName(appName), namespace, envVars); err != nil {
		return fmt.Errorf("writing app ConfigMap: %w", err)
	}
	return p.WriteAppExternalSecret(resDir, esCfg)
}

// pruneLegacyPlatformFiles removes platform manifests that earlier publishers
// wrote into the app's chart directory (before they moved to _app-resources/).
func (p *Publisher) pruneLegacyPlatformFiles(appDir string) error {
	for _, name := range []string{
		"env-configmap.yaml", "external-secret.yaml",
		"external-secret-global.yaml", "external-secret-env.yaml", "external-secret-cluster.yaml",
	} {
		path := filepath.Join(appDir, name)
		if _, err := os.Stat(path); err == nil {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("prune legacy platform file %s: %w", name, err)
			}
		}
	}
	return nil
}

// syncChart materialises the Helm chart for templateName at
// charts/{templateName}/{version}/ inside the cloned gitops repo so ArgoCD's
// ApplicationSet can resolve "charts/{{chartPath}}".
//
// Resolution order:
//  1. Local disk: TemplatesDir/{templateName}/chart/ (built-ins & dev mode).
//  2. Cluster bundle: ChartFetcher.LoadChartBundle(templateName, version),
//     the packaged .tgz stored alongside templates imported via the
//     BYO-chart flow. When version is empty (legacy apps without a pinned
//     Template.Version), the fetcher resolves to the current alias.
//
// Both paths are no-ops when their source is missing — same as before — so
// existing callers that don't configure ChartFetcher keep prior behaviour.
// When the chart directory already exists with identical content the
// subsequent git commit is a no-op via stagedIsEmpty.
// chartVersionDir is the version-scoped subdirectory under charts/{template}/
// that holds one chart version, so two apps pinning different versions of the
// same template don't collide. A pinned version is sanitized DNS-1123-safe
// (mirrors kube.sanitizeVersionForName — see the note there); an unpinned app
// ("" version, tracks latest) uses "latest".
func chartVersionDir(version string) string {
	if version == "" {
		return "latest"
	}
	var b strings.Builder
	b.Grow(len(version))
	for _, r := range strings.ToLower(version) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "latest"
	}
	return s
}

// chartPathFor returns the gitops chart path for an app's template+version,
// relative to charts/ — i.e. "{template}/{versionDir}". Written into app.yaml
// as chartPath and substituted into the ApplicationSet chart source path.
func chartPathFor(templateName, version string) string {
	return templateName + "/" + chartVersionDir(version)
}

func (p *Publisher) syncChart(ctx context.Context, repoDir, templateName, version string) error {
	dstDir := p.outputDir(repoDir, "charts", templateName, chartVersionDir(version))

	if p.cfg.TemplatesDir != "" {
		srcDir := filepath.Join(p.cfg.TemplatesDir, templateName, "chart")
		if _, err := os.Stat(srcDir); err == nil {
			// Disk-based templates are dev-mode only; we don't keep
			// per-version snapshots on disk. Whatever's at templatesDir
			// is what gets copied — version arg is informational here.
			return p.copyChartDir(srcDir, dstDir)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat chart dir %s: %w", srcDir, err)
		}
	}

	if p.cfg.ChartFetcher == nil {
		slog.Debug("gitops: no local chart and no ChartFetcher configured", "template", templateName)
		return nil
	}
	data, err := p.cfg.ChartFetcher.LoadChartBundle(ctx, templateName, version)
	if err != nil {
		return fmt.Errorf("fetch chart bundle for %s@%s: %w", templateName, version, err)
	}
	if len(data) == 0 {
		slog.Debug("gitops: no chart bundle for template, skipping sync", "template", templateName)
		return nil
	}
	return p.extractChartTGZ(data, dstDir)
}

// copyChartDir copies a chart from a local directory into the gitops repo,
// preserving subdirectory structure. Extracted from syncChart so the cluster
// fallback path can reuse the writeFile machinery without duplicating walks.
func (p *Publisher) copyChartDir(srcDir, dstDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read chart file %s: %w", path, err)
		}
		return p.writeFile(dst, data)
	})
}

// extractChartTGZ untars a packaged Helm chart into dstDir, stripping the
// archive's root chart directory so files land directly under dstDir.
//
// Helm's `helm package` wraps everything in a top-level "<chartName>/"
// directory; the gitops layout expects files directly under
// charts/{templateName}/, so we rebase paths during extraction. This keeps
// the on-repo layout identical to the local-disk path, which means existing
// ApplicationSets keep working without changes.
func (p *Publisher) extractChartTGZ(data []byte, dstDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		// Rebase: drop the first path component (the chart's root dir).
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("unsafe path in chart bundle: %s", hdr.Name)
		}
		parts := strings.SplitN(clean, string(filepath.Separator), 2)
		if len(parts) < 2 {
			continue // top-level entry with no chart-internal path
		}
		dst := filepath.Join(dstDir, parts[1])

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", dst, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			buf, err := io.ReadAll(io.LimitReader(tr, maxChartEntrySize))
			if err != nil {
				return fmt.Errorf("read tar entry %s: %w", hdr.Name, err)
			}
			if err := p.writeFile(dst, buf); err != nil {
				return err
			}
		}
	}
	return nil
}

// maxChartEntrySize bounds a single tar entry to 4 MiB during extraction,
// matching the chart-import budget. A misbehaving uploader can't fill the
// gitops repo's tmpdir with a single oversize file.
const maxChartEntrySize = 4 * 1024 * 1024

// publishKargoCRs writes the Kargo Namespace, Warehouse, and Stage manifests
// for app into gitops-output/_infra/kargo/ so ArgoCD syncs them to the cluster.
//
// Only stable environments (non-preview) that are Bound (have a registered
// cluster) get Stage CRs; preview environments and unbound environments are
// skipped because previews don't participate in the Kargo promotion pipeline
// and unbound envs have no cluster to deploy to.
//
// The promotion pipeline is derived from the org environment Order field.
// Environments are sorted by Order (then Name for tie-breaking); each stage
// declares the previous stage as its upstream gate. The first stage (lowest
// Order) pulls directly from the Warehouse with auto-promotion enabled.
func (p *Publisher) publishKargoCRs(repoDir string, app *domain.App, envs []AppPublishEnv) error {
	projectNS := KargoNamespaceForProject(app.ProjectName)
	kargoDir := p.outputDir(repoDir, "_infra", "kargo")

	// ── Build ordered stable env list ──────────────────────────────────────────
	// Exclude preview envs and unbound envs; sort by Order (then Name) for determinism.
	stableEnvs := make([]AppPublishEnv, 0, len(envs))
	for _, env := range envs {
		if env.EnvType == domain.AppEnvPreview {
			continue
		}
		if !env.Bound {
			slog.Warn("gitops: skipping kargo stage for unbound env — assign a cluster via Settings > Environments",
				"app", app.Name, "env", env.EnvName)
			continue
		}
		stableEnvs = append(stableEnvs, env)
	}
	sort.Slice(stableEnvs, func(i, j int) bool {
		if stableEnvs[i].Order != stableEnvs[j].Order {
			return stableEnvs[i].Order < stableEnvs[j].Order
		}
		return stableEnvs[i].EnvName < stableEnvs[j].EnvName
	})

	// ── Project CR (Kargo v0.9+) ───────────────────────────────────────────────
	// The Project CR replaces the Namespace-label approach and also holds
	// PromotionPolicies so that the Kargo v0.9 admission webhook permits
	// Promotion CR creation for each stable environment.
	var projectEnvs []KargoProjectEnv
	for i, env := range stableEnvs {
		projectEnvs = append(projectEnvs, KargoProjectEnv{
			AppName:      app.Name,
			EnvName:      env.EnvName,
			IsFirstStage: i == 0,
		})
	}
	proj := BuildKargoProject(projectNS, projectEnvs, p.cfg.Branding)
	projBytes, err := yaml.Marshal(proj)
	if err != nil {
		return fmt.Errorf("marshal kargo project: %w", err)
	}
	if err := p.writeFile(filepath.Join(kargoDir, projectNS+"-project.yaml"), projBytes); err != nil {
		return err
	}
	slog.Debug("gitops: wrote kargo project", "project", projectNS)

	// ── Warehouse ──────────────────────────────────────────────────────────────
	// Use the app's explicitly-set image repository when available.
	// Falls back to the default ghcr.io/{project}/{app} placeholder.
	whOpts := KargoBuildOptions{
		InsecureSkipTLSVerify: p.cfg.InsecureRegistry,
		Branding:              p.cfg.Branding,
		SubPath:               p.cfg.SubPath,
	}
	if repo, ok := app.Spec.Values["image_repository"].(string); ok && repo != "" {
		whOpts.ImageRepoURL = repo
		// When the app specifies a concrete image, accept any tag (the tag
		// pattern can always be tightened via the Warehouse directly).
		whOpts.ImageTagPattern = ".*"
	} else {
		// No image set — applyKargoDefaults will use the ghcr.io/{project}/{app}
		// placeholder, which won't resolve and leaves pods in InvalidImageName.
		// Warn loudly rather than failing silently.
		slog.Warn("gitops: app has no image_repository — Kargo Warehouse will use a placeholder that won't pull; set the app's image repository",
			"project", app.ProjectName, "app", app.Name,
			"placeholder", DefaultImageRepoURL(app.ProjectName, app.Name))
	}
	warehouse := BuildKargoWarehouse(app, whOpts)
	whBytes, err := yaml.Marshal(warehouse)
	if err != nil {
		return fmt.Errorf("marshal kargo warehouse for %s: %w", app.Name, err)
	}
	whPath := filepath.Join(kargoDir, projectNS+"-"+app.Name+"-warehouse.yaml")
	if err := p.writeFile(whPath, whBytes); err != nil {
		return err
	}
	slog.Debug("gitops: wrote kargo warehouse", "app", app.Name)

	// ── Stages ─────────────────────────────────────────────────────────────────
	// Build a linear chain: stableEnvs[0] pulls from Warehouse, each subsequent
	// stage gates on the previous stage by name.
	for i, env := range stableEnvs {
		var upstreams []string
		if i > 0 {
			upstreams = []string{stableEnvs[i-1].EnvName}
		}

		appEnv := domain.AppEnvironment{
			AppName:     app.Name,
			ProjectName: app.ProjectName,
			EnvName:     env.EnvName,
			EnvType:     env.EnvType,
		}
		stageOpts := KargoBuildOptions{
			InsecureSkipTLSVerify: p.cfg.InsecureRegistry,
			ImageRepoURL:          whOpts.ImageRepoURL,
			GitOpsRepoURL:         p.kargoGitRepoURL(),
			GitOpsRepoInsecure:    p.cfg.InsecureRegistry,
			Branding:              p.cfg.Branding,
			SubPath:               p.cfg.SubPath,
		}
		stage := BuildKargoStage(app, appEnv, upstreams, stageOpts)
		stageBytes, err := yaml.Marshal(stage)
		if err != nil {
			return fmt.Errorf("marshal kargo stage for %s/%s: %w", app.Name, env.EnvName, err)
		}
		stagePath := filepath.Join(kargoDir, projectNS+"-"+app.Name+"-"+env.EnvName+"-stage.yaml")
		if err := p.writeFile(stagePath, stageBytes); err != nil {
			return err
		}
		slog.Debug("gitops: wrote kargo stage", "app", app.Name, "env", env.EnvName)
	}

	return nil
}

// AppPublishEnv carries per-environment publish context for PublishApp.
type AppPublishEnv struct {
	// EnvName is the logical environment name, e.g. "staging".
	EnvName string
	// EnvType classifies the environment for Helm values mapping and preview
	// detection. Pipeline ordering is controlled by Order, not this field.
	EnvType domain.AppEnvironmentType
	// Order defines the position of this environment in the promotion pipeline.
	// Lower values are deployed/promoted earlier. Preview environments have
	// Order=0 and are excluded from the Kargo Stage chain.
	Order int
	// Bound indicates that this environment has a registered cluster assigned
	// and is eligible for GitOps publishing. When false, PublishApp skips
	// writing app.yaml/values.yaml and publishKargoCRs skips the Kargo Stage
	// for this env, logging a warning instead.
	//
	// AppEnvironment rows are still persisted for unbound envs so the UI can
	// surface them with a "bind a cluster" prompt.
	Bound bool
	// BaseDomain is used to derive routing.host in values.yaml.
	// When empty, "localhost" is used.
	BaseDomain string
	// Namespace is the resolved Kubernetes namespace for this app+env instance.
	// Resolved by domain.ResolveNamespace before calling PublishApp.
	// When empty, PublishApp falls back to "{app}-{env}" for backward compatibility.
	Namespace string
	// EnvVars is the fully-merged env-var map the publisher writes into the
	// per-app ConfigMap for this env. The caller merges the global → env →
	// cluster scopes before publishing so the committed YAML is the
	// source-of-truth for what the pod sees — no chart-side multi-source merge.
	EnvVars map[string]string
	// ClusterRef is the registered cluster bound to this env. Empty when the
	// env is unbound — the cluster-scope ExternalSecret is then omitted.
	ClusterRef string
	// ScopeKeys reports which (scope, tier) items currently have keys, so the
	// publisher only emits ExternalSecrets/dataFrom entries that resolve.
	// Populated by the adapter (cmd/suparship/server.go) at publish time.
	ScopeKeys ScopePresence
	// RoutingProfiles is a sparse override map for this env. Entries here
	// replace the org-level profile (PublisherConfig.RoutingProfiles) of the
	// same name; absent names inherit the org default. Populated by the
	// publish adapter from rbac.OrgEnvironment.RoutingProfiles when present.
	RoutingProfiles domain.RoutingProfiles
	// AddonProfiles is the sparse per-env override for the addon
	// catalog. Entries replace the org-level profile of the same type
	// (e.g. swap valkey-operator → crossplane-elasticache for prod).
	// Populated by the publish adapter from
	// rbac.OrgEnvironment.AddonProfiles when present.
	AddonProfiles domain.AddonProfiles
	// Clusters is the env's fan-out target set (deployMode "all"). When it has
	// more than one entry the publisher writes a per-cluster values.yaml under
	// envs/{env}/_clusters/{cluster}/... (each merged with that cluster's
	// overrides) instead of a single shared env values.yaml. Empty / single =
	// the legacy single values.yaml.
	Clusters []ClusterTarget
}

// PublishPreview writes a preview app.yaml and values.yaml so ArgoCD
// deploys the preview via the previews ApplicationSet.
//
// Written files:
//   - gitops-output/previews/{project}/{previewName}/app.yaml
//   - gitops-output/previews/{project}/{previewName}/values.yaml
//   - gitops-output/previews/{project}/{previewName}/env-configmap.yaml
//   - gitops-output/previews/{project}/{previewName}/external-secret.yaml (when StoreName is set)
//
// PublishPreview is idempotent; it only creates a commit when content changes.
func (p *Publisher) PublishPreview(ctx context.Context, app *domain.App, preview PreviewPublishSpec) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		previewMeta := PreviewAppMetadata{
			AppName:       app.Name,
			PreviewName:   preview.PreviewName,
			Project:       app.ProjectName,
			Template:      app.Spec.Template.Name,
			ChartPath:     chartPathFor(app.Spec.Template.Name, app.Spec.Template.Version),
			ClusterServer: preview.ClusterServer,
			Namespace:     preview.Namespace,
		}
		metaBytes, err := yaml.Marshal(previewMeta)
		if err != nil {
			return fmt.Errorf("marshal preview app.yaml: %w", err)
		}
		metaPath := p.outputDir(repoDir, "previews", app.ProjectName, preview.PreviewName, "app.yaml")
		if err := p.writeFile(metaPath, metaBytes); err != nil {
			return err
		}

		previewOrgName := p.cfg.OrgName
		if previewOrgName == "" {
			previewOrgName = "default"
		}
		// Org-level routing profiles apply uniformly to previews; per-env
		// overrides don't make sense for ephemeral preview envs (their
		// names are PR-specific and have no static config).
		hv := helmvalues.MapToHelmValuesForEnv(app, preview.PreviewName, domain.AppEnvPreview, preview.BaseDomain, preview.Namespace, "", previewOrgName, p.cfg.RoutingProfiles, nil, nil, p.cfg.AddonProfiles, nil)
		hvBytes, err := yaml.Marshal(hv)
		if err != nil {
			return fmt.Errorf("marshal preview values.yaml: %w", err)
		}
		valuesPath := p.outputDir(repoDir, "previews", app.ProjectName, preview.PreviewName, "values.yaml")
		if err := p.writeFile(valuesPath, hvBytes); err != nil {
			return err
		}

		// Platform-managed ConfigMap + ExternalSecret go to the platform-owned
		// _app-resources/previews/ tree (shipped by the preview platform
		// ApplicationSet), not the preview's chart directory.
		previewDir := p.outputDir(repoDir, "previews", app.ProjectName, preview.PreviewName)
		resDir := p.outputDir(repoDir, "_app-resources", "previews", app.ProjectName, preview.PreviewName)
		esCfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
			App:          app.Name,
			Namespace:    preview.Namespace,
			Env:          preview.PreviewName,
			Presence:     preview.ScopeKeys,
			UnifiedStore: p.usesUnifiedStore(),
			Branding:     p.cfg.Branding,
		})
		meta := PlatformAppMeta{
			Name:          preview.PreviewName,
			Project:       app.ProjectName,
			Namespace:     preview.Namespace,
			ClusterServer: preview.ClusterServer,
		}
		if err := p.writePlatformDir(resDir, app.Name, preview.Namespace, preview.EnvVars, esCfg, meta); err != nil {
			return fmt.Errorf("writing preview platform resources: %w", err)
		}
		if err := p.pruneLegacyPlatformFiles(previewDir); err != nil {
			return err
		}

		commitMsg := fmt.Sprintf("feat(previews): create preview %s/%s\n\nCreated by suparShip.", app.ProjectName, preview.PreviewName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// PreviewPublishSpec carries the parameters for publishing a preview environment.
type PreviewPublishSpec struct {
	// PreviewName is the sanitized preview identifier (e.g. "pr-42").
	PreviewName string
	// ClusterServer is the API server URL for the cluster where this preview runs.
	ClusterServer string
	// Namespace is the Kubernetes namespace for this preview.
	Namespace string
	// BaseDomain is used to derive routing.host in values.yaml.
	// When empty, "localhost" is used.
	BaseDomain string
	// EnvVars holds per-preview variable overrides to merge into the platform-managed
	// ConfigMap alongside app.Spec.EnvConfig.Vars.
	EnvVars map[string]string
	// ScopeKeys reports which (scope, tier) items have keys, so PublishPreview
	// emits only the ExternalSecrets that resolve.
	ScopeKeys ScopePresence
}

// DeletePreview removes the preview directory from the GitOps repo and commits.
// It is a no-op (without error) if the preview directory does not exist.
func (p *Publisher) DeletePreview(ctx context.Context, projectName, previewName string) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		previewDir := p.outputDir(repoDir, "previews", projectName, previewName)
		if _, err := os.Stat(previewDir); os.IsNotExist(err) {
			slog.Debug("gitops: preview directory not found, nothing to delete", "preview", previewName)
			return nil
		}
		if err := os.RemoveAll(previewDir); err != nil {
			return fmt.Errorf("removing preview dir: %w", err)
		}
		commitMsg := fmt.Sprintf("feat(previews): delete preview %s/%s\n\nDeleted by suparShip.", projectName, previewName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// unpublishHelper removes path if present, tracking whether anything was
// actually deleted so callers can skip the commit when nothing changed.
type unpublishHelper struct{ removed bool }

func (u *unpublishHelper) rm(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	slog.Debug("gitops: removed", "path", path)
	u.removed = true
	return nil
}

// UnpublishApp removes all GitOps manifests for an app and commits + pushes
// the deletion. Covered layouts:
//
//   - envs/{env}/{project}/{app}/            — app Application + values
//   - _app-resources/{env}/{project}/{app}/  — platform ConfigMap/ExternalSecret
//   - _infra/kargo/{ns}-{app}-*.yaml         — Kargo Warehouse + Stage CRs
//   - {env}/{project}/{app}/                 — legacy pre-envs/ layout
//
// Preview trees (previews/{project}/{preview}) are keyed by preview name, not
// app name, and are removed by the preview-delete flow. No-op if nothing found.
func (p *Publisher) UnpublishApp(ctx context.Context, projectName, appName string) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		var u unpublishHelper

		// envs/{env}/{project}/{app} and _app-resources/{env}/{project}/{app}.
		for _, base := range []string{"envs", "_app-resources"} {
			baseDir := p.outputDir(repoDir, base)
			entries, err := os.ReadDir(baseDir)
			if err != nil {
				continue // tree absent — nothing to remove
			}
			for _, e := range entries {
				if !e.IsDir() || e.Name() == "previews" {
					continue
				}
				if err := u.rm(filepath.Join(baseDir, e.Name(), projectName, appName)); err != nil {
					return err
				}
				// Per-cluster fan-out values: envs/{env}/_clusters/{cluster}/{project}/{app}.
				clustersDir := filepath.Join(baseDir, e.Name(), "_clusters")
				if cents, cerr := os.ReadDir(clustersDir); cerr == nil {
					for _, c := range cents {
						if !c.IsDir() {
							continue
						}
						if err := u.rm(filepath.Join(clustersDir, c.Name(), projectName, appName)); err != nil {
							return err
						}
					}
				}
			}
		}

		// Kargo Warehouse + Stage CRs for this app. Match on the stamped
		// suparship.io/app label rather than the filename prefix: a sibling app
		// whose name extends this one (e.g. "web-admin" vs "web") shares the
		// filename prefix and would otherwise be wrongly pruned.
		kargoDir := p.outputDir(repoDir, "_infra", "kargo")
		if entries, err := os.ReadDir(kargoDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				path := filepath.Join(kargoDir, e.Name())
				if kargoManifestLabel(path, labelApp) != appName {
					continue
				}
				if err := u.rm(path); err != nil {
					return err
				}
			}
		}

		// Legacy pre-envs/ layout: top-level {env}/{project}/{app}.
		outputDir := p.outputDir(repoDir)
		if entries, err := os.ReadDir(outputDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() || isReservedTopLevelDir(e.Name()) {
					continue
				}
				if err := u.rm(filepath.Join(outputDir, e.Name(), projectName, appName)); err != nil {
					return err
				}
			}
		}

		if !u.removed {
			slog.Debug("gitops: no app files found — nothing to delete",
				"project", projectName, "app", appName)
			return nil
		}

		commitMsg := fmt.Sprintf("feat(apps): delete app %s/%s\n\nDeleted by suparShip.", projectName, appName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// kargoManifestLabel reads a Kargo manifest file and returns the value of the
// given metadata label, or "" when the file is unreadable or lacks the label.
// Used to prune an app/project's Kargo CRs by their stamped suparship.io/app
// or suparship.io/project label rather than by a collision-prone filename
// prefix.
func kargoManifestLabel(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var m struct {
		Metadata struct {
			Labels map[string]string `yaml:"labels"`
		} `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return ""
	}
	return m.Metadata.Labels[key]
}

// isReservedTopLevelDir reports whether a top-level gitops-output entry is a
// platform-owned tree rather than a legacy env directory.
func isReservedTopLevelDir(name string) bool {
	switch name {
	case "previews", "envs", "charts", "_infra", "_app-resources", "_secret-stores":
		return true
	}
	return false
}

// UnpublishProjectApps is PHASE 1 of project deletion: it removes every app
// directory in every layout, the project's preview trees, and its Kargo CRs —
// everything EXCEPT the ArgoCD AppProject — and commits + pushes the deletion.
//
// The AppProject must outlive the generated Applications: when an Application
// is pruned, its cleanup finalizer resolves the AppProject to cascade-delete
// the deployed resources. Removing both in one commit races ArgoCD and leaves
// Applications stuck in Terminating ("appproject not found"). Call
// UnpublishProjectInfra (phase 2) once the project's Applications are gone.
//
// Per-env ApplicationSets are shared across projects and stay. No-op if
// nothing found.
func (p *Publisher) UnpublishProjectApps(ctx context.Context, projectName string) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		var u unpublishHelper

		// envs/{env}/{project} and _app-resources/{env}/{project} — for
		// _app-resources the "previews" entry also nests by project, so it is
		// intentionally NOT skipped here.
		for _, base := range []string{"envs", "_app-resources"} {
			baseDir := p.outputDir(repoDir, base)
			entries, err := os.ReadDir(baseDir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if err := u.rm(filepath.Join(baseDir, e.Name(), projectName)); err != nil {
					return err
				}
			}
		}

		// Preview chart trees.
		if err := u.rm(p.outputDir(repoDir, "previews", projectName)); err != nil {
			return err
		}

		// Kargo Project CR + every app's Warehouse/Stage CRs, matched on the
		// stamped suparship.io/project label (the Project, Warehouse, and Stage
		// CRs all carry it) — avoids the filename-prefix collision between a
		// project and a hyphen-extended sibling (e.g. "web" vs "web-admin").
		kargoDir := p.outputDir(repoDir, "_infra", "kargo")
		if entries, err := os.ReadDir(kargoDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				path := filepath.Join(kargoDir, e.Name())
				if kargoManifestLabel(path, labelProject) != projectName {
					continue
				}
				if err := u.rm(path); err != nil {
					return err
				}
			}
		}

		// Legacy pre-envs/ layout: top-level {env}/{project}.
		outputDir := p.outputDir(repoDir)
		if entries, err := os.ReadDir(outputDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() || isReservedTopLevelDir(e.Name()) {
					continue
				}
				if err := u.rm(filepath.Join(outputDir, e.Name(), projectName)); err != nil {
					return err
				}
			}
		}

		if !u.removed {
			slog.Debug("gitops: no project app files found — nothing to delete", "project", projectName)
			return nil
		}

		commitMsg := fmt.Sprintf("feat(projects): delete project %s apps (phase 1/2)\n\nDeleted by suparShip.", projectName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// UnpublishProjectInfra is PHASE 2 of project deletion: it removes the
// project's ArgoCD AppProject. Only call this after the project's generated
// Applications have been pruned (see UnpublishProjectApps). No-op if absent.
func (p *Publisher) UnpublishProjectInfra(ctx context.Context, projectName string) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		var u unpublishHelper
		if err := u.rm(p.outputDir(repoDir, "_infra", projectName+"-appproject.yaml")); err != nil {
			return err
		}
		if !u.removed {
			slog.Debug("gitops: no project infra files found — nothing to delete", "project", projectName)
			return nil
		}
		commitMsg := fmt.Sprintf("feat(projects): delete project %s infra (phase 2/2)\n\nDeleted by suparShip.", projectName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// ── internal helpers ──────────────────────────────────────────────────────────

// withClonedRepo clones the gitops repository into a temp directory, runs fn
// with the repo path, and removes the temp directory when done. Before
// invoking fn it seeds gitops-output/README.md when missing, so every
// publish operation also lands the take-over contract for new SREs
// inheriting the repo.
func (p *Publisher) withClonedRepo(ctx context.Context, fn func(repoDir string) error) error {
	tmpDir, err := os.MkdirTemp("", "suparship-gitops-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	repoDir := filepath.Join(tmpDir, "gitops")
	cloneURL := p.embedCredentials(p.cfg.RepoURL)

	slog.Debug("gitops: cloning repo", "repo", p.cfg.RepoURL, "branch", p.cfg.Branch)
	if err := p.git(ctx, tmpDir, "clone", "--depth=1", "--branch="+p.cfg.Branch, cloneURL, repoDir); err != nil {
		return fmt.Errorf("clone gitops repo: %w", err)
	}

	if err := p.git(ctx, repoDir, "config", "user.email", "suparship@suparcloud.io"); err != nil {
		return err
	}
	if err := p.git(ctx, repoDir, "config", "user.name", "suparShip"); err != nil {
		return err
	}

	if err := p.ensureRepoREADME(repoDir); err != nil {
		// README is operator-facing documentation, not infrastructure —
		// log and continue rather than aborting a publish if the write
		// fails for some odd reason.
		slog.Warn("gitops: README seeding failed; continuing publish", "err", err)
	}

	return fn(repoDir)
}

// ensureRepoREADME writes gitops-output/README.md when absent. Operator
// edits to an existing README are preserved — the file is checked for
// existence, not content. The README explains directory layout, ownership
// labels, and the take-over recipe so a new SRE inheriting the repo can
// orient themselves without reading suparship's source.
func (p *Publisher) ensureRepoREADME(repoDir string) error {
	path := p.outputDir(repoDir, "README.md")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat README: %w", err)
	}
	return p.writeFile(path, []byte(buildRepoREADME(p.cfg.Branding, p.cfg.SubPath)))
}

// appEnvDir builds the per-app directory for one publish env. Stable envs
// land under "envs/{envName}/{project}/{app}/" so the top-level layout is
// self-documenting (`envs/`, `previews/`, `_infra/`, `charts/`). Preview
// envs keep their legacy "{previewName}/{project}/{app}/" placement —
// publishAppFiles handles both kinds, but the dedicated PublishPreview
// flow writes to "previews/{project}/{previewName}/" so the locations
// differ on purpose.
func (p *Publisher) appEnvDir(repoDir string, env AppPublishEnv, parts ...string) string {
	if env.EnvType == domain.AppEnvPreview {
		all := append([]string{env.EnvName}, parts...)
		return p.outputDir(repoDir, all...)
	}
	all := append([]string{"envs", env.EnvName}, parts...)
	return p.outputDir(repoDir, all...)
}

// appEnvDirExternal mirrors appEnvDir but routes external-mode apps to
// envs-external/{envName}/{project}/{app}/. The path glob is what
// BuildArgoExternalAppSet's Git File generator picks up; keeping it
// disjoint from the inline-mode envs/ glob means each app belongs to
// exactly one ApplicationSet without parameter selectors.
//
// Preview envs always use the inline path — external-mode previews are
// out of scope for PR2.
func (p *Publisher) appEnvDirExternal(repoDir string, env AppPublishEnv, parts ...string) string {
	if env.EnvType == domain.AppEnvPreview {
		return p.appEnvDir(repoDir, env, parts...)
	}
	all := append([]string{"envs-external", env.EnvName}, parts...)
	return p.outputDir(repoDir, all...)
}

// AppMetadataChartType is an internal flag the publisher uses to route
// per-app file writes between inline-mode and external-mode paths.
// Distinct from the wire-format AppMetadata.ChartType string so the
// publisher's call sites don't have to compare against magic strings.
type AppMetadataChartType int

const (
	// AppMetadataChartTypeInline is the default — chart bytes live in
	// the gitops repo at charts/{template}/, app.yaml lives at
	// envs/{env}/{project}/{app}/.
	AppMetadataChartTypeInline AppMetadataChartType = iota
	// AppMetadataChartTypeExternal — chart lives in a Helm registry,
	// app.yaml lives at envs-external/{env}/{project}/{app}/, no chart
	// bytes touched by the publisher.
	AppMetadataChartTypeExternal
)

// envAppDir routes per-app file writes between the inline and external
// path globs based on the resolved chart mode for the current template.
// Inline-mode → appEnvDir; external-mode → appEnvDirExternal.
func (p *Publisher) envAppDir(mode AppMetadataChartType, repoDir string, env AppPublishEnv, parts ...string) string {
	if mode == AppMetadataChartTypeExternal {
		return p.appEnvDirExternal(repoDir, env, parts...)
	}
	return p.appEnvDir(repoDir, env, parts...)
}

// resolveTemplateChartMode looks up the template via TemplateLoader and
// returns the resolved chart mode plus, for external-mode templates,
// the chart locator (so the publisher can populate AppMetadata's chart
// fields without re-loading).
//
// Falls back to inline-mode when:
//   - TemplateLoader is nil (back-compat).
//   - Lookup returns a non-fatal error or nil template.
//   - The template's engine.chart isn't external (bundled or inline ./chart).
//
// The fallback is silent because external-mode is opt-in; treating an
// unresolvable template as a fatal publish error would break every app
// when the template store is briefly unavailable.
func (p *Publisher) resolveTemplateChartMode(templateName string) (AppMetadataChartType, *tpl.ChartRef) {
	if p.cfg.TemplateLoader == nil {
		return AppMetadataChartTypeInline, nil
	}
	tmpl, err := p.cfg.TemplateLoader.LoadTemplate(context.Background(), templateName)
	if err != nil {
		slog.Warn("gitops: TemplateLoader failed; falling back to inline-mode publish",
			"template", templateName, "err", err)
		return AppMetadataChartTypeInline, nil
	}
	if tmpl == nil || !tmpl.Spec.Engine.Chart.IsExternal() {
		return AppMetadataChartTypeInline, nil
	}
	return AppMetadataChartTypeExternal, tmpl.Spec.Engine.Chart.Ref
}

// outputDir builds an absolute filesystem path inside the gitops output
// area: <repoDir>/<SubPath>/<parts...>. SubPath of "" / "." / "./" puts
// the output at the repo root; otherwise it acts as a single-level
// containment dir. Used everywhere the publisher needs to write a file
// so a config flip in PublisherConfig.SubPath moves the entire layout
// in lockstep.
func (p *Publisher) outputDir(repoDir string, parts ...string) string {
	sub := normalizeSubPath(p.cfg.SubPath)
	elems := []string{repoDir}
	if sub != "" {
		elems = append(elems, sub)
	}
	elems = append(elems, parts...)
	return filepath.Join(elems...)
}

// relativeOutputPath returns "<SubPath>/<parts>" using slash separators —
// the right shape for paths inside YAML manifests (ApplicationSet path
// globs, ArgoCD source.path, Kargo gitRepoUpdates.helm.valuesFilePath,
// etc.). Empty SubPath returns just the joined parts so the path is
// repo-relative.
func (p *Publisher) relativeOutputPath(parts ...string) string {
	sub := normalizeSubPath(p.cfg.SubPath)
	all := parts
	if sub != "" {
		all = append([]string{sub}, parts...)
	}
	// Use forward-slash join: Git paths are always slash-separated even
	// on Windows, and ArgoCD parses them as URL-style.
	return strings.Join(all, "/")
}

// normalizeSubPath strips whitespace and slashes, treating "." and "./"
// as the empty (repo-root) form. Returns the cleaned sub-path with no
// leading/trailing slashes — callers join slashes themselves.
func normalizeSubPath(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "/")
	if s == "" || s == "." {
		return ""
	}
	return s
}

// joinSubPath builds a slash-separated path with the cleaned sub-path
// prepended. Used by pure builder functions (BuildKargoStage,
// BuildArgoAppSet, …) that don't have a *Publisher receiver but still
// need to emit paths consistent with whatever PublisherConfig.SubPath
// the publisher writes to.
func joinSubPath(subPath string, parts ...string) string {
	sub := normalizeSubPath(subPath)
	all := parts
	if sub != "" {
		all = append([]string{sub}, parts...)
	}
	return strings.Join(all, "/")
}

// writeFile creates parent directories and writes data to path. For YAML
// files under the gitops output area (excluding bundled chart sources) it
// prepends a short generated-by header so an SRE opening the file knows
// it's regenerated on every publish and where to look to take it over.
func (p *Publisher) writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dirs for %s: %w", path, err)
	}
	if header := generatedByHeader(path, p.cfg.Branding); header != "" {
		data = append([]byte(header), data...)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// generatedByHeader returns a YAML comment block to prepend to platform-
// emitted YAML files, or "" when path is not eligible. Eligible:
// path ends in .yaml/.yml AND has no `/charts/` segment.
//
// The /charts/ carve-out keeps bundled Helm chart sources clean — they
// are the chart's own templates, not platform-generated YAML; adding a
// "generated by" preamble would mislead operators reviewing the chart
// and could confuse Helm tooling that parses leading comments. Detection
// is path-based (rather than gating on PublisherConfig.SubPath) so the
// rule applies wherever charts/ actually lives, repo-root or under SubPath.
func generatedByHeader(path string, brand branding.Config) string {
	if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
		return ""
	}
	sep := string(filepath.Separator)
	if strings.Contains(path, sep+"charts"+sep) {
		return ""
	}
	return fmt.Sprintf(
		"# Generated by %s. Edits will be overwritten on the next publish.\n"+
			"# Take-over recipe: see the README.md at the gitops output root.\n",
		brand.EffectiveName(),
	)
}

// buildRepoREADME returns the markdown body for gitops-output/README.md,
// branded to the configured platform identity. Written verbatim on first
// publish; operator edits are preserved on subsequent publishes (see
// ensureRepoREADME).
func buildRepoREADME(brand branding.Config, subPath string) string {
	name := brand.EffectiveName()
	domain := brand.EffectiveLabelDomain()
	// rootLabel is what appears at the top of the layout tree. Empty
	// SubPath → "<repo root>/" so the diagram still shows a single root
	// node. Non-empty → "<subpath>/" so the diagram matches the actual
	// directory the operator will see in their git host.
	rootLabel := "<repo root>/"
	sub := normalizeSubPath(subPath)
	if sub != "" {
		rootLabel = sub + "/"
	}
	return fmt.Sprintf(`# GitOps repository

This directory holds the desired-state Kubernetes manifests that ArgoCD
syncs to your clusters. It is written by **%[1]s**, but operates as plain
YAML on top of standard CNCF components — ArgoCD, Kargo, External Secrets
Operator, Sealed Secrets, Stakater Replicator. Nothing here requires
%[1]s to be running: an SRE can edit files directly, detach an app, or
walk away from the platform entirely. The repo keeps syncing.

## Layout

`+"```"+`
%[3]s
├── _infra/                                # platform glue (root-app synced)
│   ├── {env}-appset.yaml                  # ApplicationSet per env (cluster)
│   ├── previews-appset.yaml               # preview ApplicationSet
│   ├── {project}-appproject.yaml          # ArgoCD AppProject per project
│   ├── {project}-ns-{env}.yaml            # project Namespace manifests
│   ├── eso-stores.yaml                    # K8s-backend ClusterSecretStore
│   ├── eso-secrets-{level}-*.yaml         # upper-level ExternalSecrets
│   ├── secrets-{cluster}-app.yaml         # ArgoCD Application syncing _secret-stores/{env}/
│   └── kargo/                             # Kargo Project / Warehouse / Stage CRs
├── _secret-stores/                        # per-env 1Password Connect (synced by secrets-{cluster})
│   └── {env}/
│       ├── sealed-token.yaml              # SealedSecret of the Connect read token
│       └── store.yaml                     # ClusterSecretStore (1Password backend)
├── envs/                                  # stable environments
│   └── {env}/                             # staging, prod, …
│       └── {project}/
│           └── {app}/
│               ├── app.yaml               # ArgoCD File-generator parameters
│               ├── values.yaml            # rendered Helm values
│               ├── env-configmap.yaml     # merged env vars (org→cluster)
│               └── external-secret.yaml   # platform-managed ExternalSecret
├── previews/{project}/{previewName}/      # per-PR preview environments
└── charts/{template}/{version}/           # bundled Helm chart sources (version-scoped)
`+"```"+`

The `+"`envs/`"+` wrapper makes the top-level self-documenting: stable envs
under `+"`envs/`"+`, ephemeral previews under `+"`previews/`"+`, platform glue under
`+"`_infra/`"+`, secret-store bootstraps under `+"`_secret-stores/`"+`. There is no
per-app `+"`kustomization.yaml`"+` — ArgoCD's directory generator applies the
files in each app folder directly, so adding/removing a manifest is just
adding/removing the file.

## Naming conventions

- **ArgoCD Applications**: `+"`{project}-{app}-{env}`"+` — project-prefixed
  because all Applications share the `+"`argocd`"+` namespace and would
  collide otherwise (e.g. two projects each with an app named `+"`api`"+`).
- **Kargo Stages**: `+"`{app}-{env}`"+` — Kargo runs each project in its own
  namespace, so the project prefix is implicit in the location.
- **Project namespaces**: `+"`{project}-{env}`"+` (org pattern overridable).
- **App namespaces**: `+"`{app}-{env}`"+`.
- **Secret-store Application**: `+"`secrets-{cluster}`"+` — one per cluster,
  syncing `+"`_secret-stores/{env}/`"+` for every env bound to that cluster.

## What is platform-managed

Every YAML file written by **%[1]s** opens with a header comment:

`+"```"+`
# Generated by %[1]s. Edits will be overwritten on the next publish.
# Take-over recipe: see the README.md at the gitops output root.
`+"```"+`

Resources also carry these labels:

`+"```"+`
app.kubernetes.io/managed-by: %[1]s
%[2]s/generator-version: v...
%[2]s/{env,cluster,project,app}: ...   # resource-specific
`+"```"+`

Find every platform-managed resource in a cluster with:

`+"```"+`
kubectl get -A -l app.kubernetes.io/managed-by=%[1]s
`+"```"+`

The bundled chart sources under `+"`charts/`"+` are NOT regenerated — they are
the Helm templates the platform deploys. Edit them in source control if
you want to fork the chart for your needs.

## Connect token recovery

The 1Password Connect read token is stored two places:

1. **Sealed in the repo** — `+"`_secret-stores/{env}/sealed-token.yaml`"+` is a
   SealedSecret encrypted to the target cluster's `+"`sealed-secrets-controller`"+`
   public key. Only that cluster can decrypt it.
2. **Stashed in the platform cluster** — `+"`%[1]s-system/%[1]s-onepassword-connect-token-{env}`"+`
   holds the plaintext token so %[1]s can re-seal it if the gitops repo is
   wiped or a new cluster is bound to an env.

If you stop using %[1]s, the in-repo SealedSecret is enough: the cluster's
sealed-secrets-controller will keep decrypting it on every sync. The stash
is only needed if you want %[1]s to self-heal the repo.

## Take-over recipes

Four escalating levels of "stop using the platform for this":

### Detach one app

1. Open the platform UI and delete the app, OR
2. Delete the cluster-side record (e.g. `+"`kubectl delete configmap -n %[1]s-system %[1]s-app-{project}-{app}`"+`)
3. Remove `+"`envs/{env}/{project}/{app}/`"+` for every env, plus the matching
   `+"`_infra/kargo/{project}-{app}-*.yaml`"+` Kargo CRs.
4. ArgoCD will prune the live workload on its next sync.

### Detach one project

1. Remove every app under the project (steps above), then
2. Remove `+"`_infra/{project}-appproject.yaml`"+` and
   `+"`_infra/{project}-ns-*.yaml`"+`.
3. Delete the project record from %[1]s's store
   (e.g. the `+"`%[1]s-project-{name}`"+` ConfigMap).

### Detach one environment

1. Remove `+"`envs/{env}/`"+` and `+"`_secret-stores/{env}/`"+`.
2. Remove `+"`_infra/{env}-appset.yaml`"+`.
3. If no other env uses the cluster, remove `+"`_infra/secrets-{cluster}-app.yaml`"+`.
4. Delete the env record from %[1]s's store.

### Operate the entire repo without %[1]s

1. Stop publishing — disable the platform's GitOps integration.
2. Take ownership of the existing files: delete the header comment on the
   YAMLs you want to manage manually. Nothing in the manifests references
   the platform at runtime — labels are inert, the SealedSecret decrypts
   with the cluster's standard sealed-secrets-controller, and the
   ApplicationSets/AppProjects are vanilla ArgoCD.
3. ArgoCD, Kargo, ESO, Sealed Secrets, and Stakater Replicator keep
   running on their own. They have no knowledge of %[1]s.

The bundled `+"`charts/`"+` directory is yours to keep — fork it, replace it
with your own charts, or leave it alone.

---

*This README was seeded by **%[1]s** on first publish. It is not regenerated
— feel free to edit, replace, or delete it.*
`, name, domain, rootLabel)
}

// commitAndPush stages all changes, commits with msg (when there is something
// to commit), and pushes to origin. It is a no-op if the working tree is clean.
func (p *Publisher) commitAndPush(ctx context.Context, repoDir, msg string) error {
	if err := p.git(ctx, repoDir, "add", "."); err != nil {
		return err
	}
	if empty, err := p.stagedIsEmpty(ctx, repoDir); err != nil {
		return err
	} else if empty {
		slog.Debug("gitops: nothing to commit — already up to date")
		return nil
	}
	slog.Debug("gitops: committing", "msg", msg)
	if err := p.git(ctx, repoDir, "commit", "-m", msg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	slog.Debug("gitops: pushing to origin", "branch", p.cfg.Branch)
	if err := p.git(ctx, repoDir, "push", "origin", p.cfg.Branch); err != nil {
		return fmt.Errorf("push to gitops repo: %w", err)
	}
	slog.Debug("gitops: push complete")
	return nil
}

// stagedIsEmpty returns true when there are no staged changes (nothing to commit).
func (p *Publisher) stagedIsEmpty(ctx context.Context, repoDir string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(strings.TrimSpace(string(out))) == 0, nil
}

// embedCredentials injects user:password into an HTTP/HTTPS URL so that git
// does not prompt interactively during clone/push.
func (p *Publisher) embedCredentials(repoURL string) string {
	if p.cfg.RepoUser == "" {
		return repoURL
	}
	for _, scheme := range []string{"https://", "http://"} {
		if strings.HasPrefix(repoURL, scheme) {
			return scheme + p.cfg.RepoUser + ":" + p.cfg.RepoPassword + "@" + repoURL[len(scheme):]
		}
	}
	return repoURL
}

// git runs a git subcommand inside dir and returns a combined-output error on failure.
func (p *Publisher) git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// marshalHelmValues serializes a HelmValues struct to a YAML string suitable
// for use as inline Helm values inside an ArgoCD Application spec.
// Kept for backward compatibility with tests that use BuildArgoApplication directly.
func marshalHelmValues(hv helmvalues.HelmValues) (string, error) {
	b, err := yaml.Marshal(hv)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// syncAddonCharts materialises each addon's wrapper chart (an inline template
// such as "valkey") into charts/<chart>/latest/ so the addon Applications that
// publishAppAddons emits resolve their chart path. Mirrors the app-template
// sync in PublishApp. Addon profiles carry no version (AddonProfile has only
// Chart + Provider), so addon charts are unpinned → the "latest" dir. Distinct
// charts are synced once. External-mode addon charts are pulled by Argo from
// the registry and need no local copy.
func (p *Publisher) syncAddonCharts(ctx context.Context, repoDir string, app *domain.App, envs []AppPublishEnv) error {
	if len(app.Spec.Addons) == 0 {
		return nil
	}
	synced := map[string]bool{}
	for _, env := range envs {
		for _, claim := range app.Spec.Addons {
			profile, err := domain.ResolveAddonProfile(p.cfg.AddonProfiles, env.AddonProfiles, claim.Type)
			if err != nil || profile.Chart == "" || synced[profile.Chart] {
				continue
			}
			if mode, _ := p.resolveTemplateChartMode(profile.Chart); mode != AppMetadataChartTypeInline {
				continue // external addon chart — Argo pulls it, no local sync
			}
			if err := p.syncChart(ctx, repoDir, profile.Chart, ""); err != nil {
				return fmt.Errorf("sync addon chart %q: %w", profile.Chart, err)
			}
			synced[profile.Chart] = true
		}
	}
	return nil
}

// publishAppAddons writes one Application + values.yaml pair per
// addon claim on app.Spec.Addons under
// {appDir}/addons/<addon-name>/. Each pair is picked up by the same
// ApplicationSet that publishes the main app, so no AppSet schema
// change is needed — Argo just reconciles N+1 Applications instead
// of 1 per app+env.
//
// Each addon's app.yaml uses Template = the resolved AddonProfile.Chart
// so the ApplicationSet's Helm path resolves to the addon wrapper
// chart under charts/<wrapper-template-name>/. values.yaml carries
// the AddonInstanceValues shape (App, Addon, Suparship).
//
// Failure to resolve an addon's profile is logged and skipped — the
// app save path runs Validate first; reaching publish with an
// unresolved type is a configuration race we don't want to block on.
func (p *Publisher) publishAppAddons(
	appDir string,
	app *domain.App,
	env AppPublishEnv,
	orgName string,
) error {
	if len(app.Spec.Addons) == 0 {
		return nil
	}
	for _, claim := range app.Spec.Addons {
		profile, err := domain.ResolveAddonProfile(p.cfg.AddonProfiles, env.AddonProfiles, claim.Type)
		if err != nil {
			slog.Warn("gitops: skipping addon — no AddonProfile configured",
				"app", app.Name, "env", env.EnvName, "addon", claim.Name, "type", claim.Type, "err", err)
			continue
		}

		hv, err := helmvalues.MapAddonToHelmValuesForEnv(
			app, claim, env.EnvName, env.EnvType, env.ClusterRef,
			orgName,
			p.cfg.AddonProfiles, env.AddonProfiles,
		)
		if err != nil {
			return fmt.Errorf("addon %q: %w", claim.Name, err)
		}

		// addon app.yaml — same shape as the main app.yaml so the
		// existing ApplicationSet generator picks it up. Template
		// points at the resolved wrapper chart name.
		ns := env.Namespace
		if ns == "" {
			ns = app.Name + "-" + env.EnvName
		}
		meta := AppMetadata{
			Name:     fmt.Sprintf("%s-addon-%s", app.Name, claim.Name),
			Project:  app.ProjectName,
			Template: profile.Chart,
			// The shared ApplicationSet sources charts/{{chartPath}}; addon
			// wrapper charts are unpinned inline templates, synced to
			// charts/<chart>/latest/ by syncAddonCharts. Without this the
			// addon Application resolves an empty chart path and never syncs.
			ChartPath: chartPathFor(profile.Chart, ""),
			Namespace: ns,
		}
		metaBytes, err := yaml.Marshal(meta)
		if err != nil {
			return fmt.Errorf("addon %q: marshal app.yaml: %w", claim.Name, err)
		}
		hvBytes, err := yaml.Marshal(hv)
		if err != nil {
			return fmt.Errorf("addon %q: marshal values.yaml: %w", claim.Name, err)
		}

		base := filepath.Join(appDir, "addons", claim.Name)
		if err := p.writeFile(filepath.Join(base, "app.yaml"), metaBytes); err != nil {
			return err
		}
		if err := p.writeFile(filepath.Join(base, "values.yaml"), hvBytes); err != nil {
			return err
		}
		slog.Debug("gitops: wrote addon files",
			"app", app.Name, "env", env.EnvName, "addon", claim.Name, "chart", profile.Chart)
	}
	return nil
}
