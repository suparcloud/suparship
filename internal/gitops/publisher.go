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
	// Branding controls the platform identity stamped onto every manifest
	// the publisher writes (label values + custom label/annotation
	// domain). Zero value applies "suparship" / "suparship.io" defaults
	// — SRE contractors who white-label set Org.Branding once and the
	// publisher picks it up.
	Branding branding.Config
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

// ChartFetcher returns the packaged Helm chart bytes for a template name, or
// nil when no bundle exists. Implementations are free to consult any
// backing store (cluster ConfigMap, OCI registry, …); the publisher only
// needs the .tgz contents.
type ChartFetcher interface {
	LoadChartBundle(ctx context.Context, templateName string) ([]byte, error)
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
// patterns, backend config, org name, and branding). Thread-safe for
// callers that rebuild the publisher when org config changes; for
// concurrent use call this before handing the publisher to goroutines.
func (p *Publisher) SetOrgConfig(orgName string, naming secrets.ResourceNaming, backend *secrets.BackendConfig, brand branding.Config) {
	p.cfg.OrgName = orgName
	p.cfg.ResourceNaming = naming
	p.cfg.BackendConfig = backend
	p.cfg.Branding = brand
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

			destinations = append(destinations, AppProjectDestination{Server: env.ClusterServer, Namespace: "*"})
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

		// Write ClusterSecretStores when a secret backend is configured.
		// For K8s backend this is the suparship-k8s-store; for 1Password it
		// is one store per provisioned env binding. Stores are idempotent — if
		// nothing changed git will produce no new commit.
		if p.cfg.BackendConfig != nil {
			orgName := p.cfg.OrgName
			if orgName == "" {
				orgName = "default"
			}
			stores := BuildSecretStoresForConfig(*p.cfg.BackendConfig, p.cfg.ResourceNaming, orgName, p.cfg.Branding)
			if err := p.WriteSecretStores(repoDir, stores); err != nil {
				return fmt.Errorf("writing ClusterSecretStores: %w", err)
			}
		}

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

		// Sync the Helm chart into charts/{template}/ so ArgoCD's
		// ApplicationSet can resolve the chart path.
		if err := p.syncChart(ctx, repoDir, app.Spec.Template.Name); err != nil {
			return fmt.Errorf("sync chart for template %s: %w", app.Spec.Template.Name, err)
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
	naming := p.cfg.ResourceNaming
	orgName := p.cfg.OrgName
	if orgName == "" {
		orgName = "default"
	}

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
			Namespace: ns,
		}
		appMetaBytes, err := yaml.Marshal(appMeta)
		if err != nil {
			return fmt.Errorf("marshal app.yaml for env %s: %w", env.EnvName, err)
		}
		appMetaPath := p.appEnvDir(repoDir, env, app.ProjectName, app.Name, "app.yaml")
		if err := p.writeFile(appMetaPath, appMetaBytes); err != nil {
			return err
		}

		// Write values.yaml — Helm values with env-specific baseDomain and
		// the resolved namespace so secretName/configName are consistent.
		var backend secrets.BackendType
		if p.cfg.BackendConfig != nil {
			backend = p.cfg.BackendConfig.Effective()
		}
		hv := helmvalues.MapToHelmValuesForEnv(app, env.EnvName, env.EnvType, env.BaseDomain, env.Namespace, env.ClusterRef, naming, orgName, backend)
		hvBytes, err := yaml.Marshal(hv)
		if err != nil {
			return fmt.Errorf("marshal values.yaml for env %s: %w", env.EnvName, err)
		}
		valuesPath := p.appEnvDir(repoDir, env, app.ProjectName, app.Name, "values.yaml")
		if err := p.writeFile(valuesPath, hvBytes); err != nil {
			return err
		}

		// Write platform-managed per-app resources into the same directory as
		// app.yaml/values.yaml so ArgoCD picks them up with the same ApplicationSet.
		appDir := p.appEnvDir(repoDir, env, app.ProjectName, app.Name)
		if err := p.writeAppPlatformResources(appDir, app, ns, env, naming, orgName); err != nil {
			return fmt.Errorf("writing platform resources for env %s: %w", env.EnvName, err)
		}

		slog.Debug("gitops: wrote app files", "env", env.EnvName, "app", app.Name)
	}
	return nil
}

// writeAppPlatformResources writes the platform-managed ExternalSecret and
// ConfigMap YAML files into dir (the per-app-env directory under gitops-output).
// These resources are consumed by the Helm chart via envFrom.
//
// ConfigMap (always written, even when empty):
//
//	{dir}/env-configmap.yaml  →  K8s ConfigMap named "{app}-config"
//
// The ConfigMap data is env.EnvVars verbatim — the caller is responsible for
// merging org → env-type → project → app → app-env → cluster scopes before
// publishing, so the YAML committed to git is the audit-trail for what the
// pod will see (no chart-side multi-source merge).
//
// ExternalSecret (only when env.StoreName is non-empty):
//
//	{dir}/external-secret.yaml  →  K8s ExternalSecret named "{app}-secrets"
//
// When p.cfg.BackendConfig is set, the ExternalSecret is rendered as a
// collapsed multi-scope merge across all six hierarchy levels (org → env-type
// → project → app → app-env → cluster) using BuildCollapsedExternalSecretForApp.
// Empty scopes are skipped (per env.ScopeKeys) so ESO doesn't error trying to
// extract from non-existent vault items. When BackendConfig is nil — typically
// in tests or pre-Phase-3 callers — the writer falls back to a single-key
// dataFrom for the app-env scope only, preserving prior behaviour.
func (p *Publisher) writeAppPlatformResources(
	dir string,
	app *domain.App,
	namespace string,
	env AppPublishEnv,
	naming secrets.ResourceNaming,
	orgName string,
) error {
	np := secrets.NamingParams{
		Org:     orgName,
		Env:     env.EnvName,
		Project: app.ProjectName,
		App:     app.Name,
	}

	// ConfigMap — written with the fully-merged map the caller passed in.
	cmName := naming.RenderAppConfigMap(np)
	if err := p.WriteAppConfigMap(dir, cmName, namespace, env.EnvVars); err != nil {
		return fmt.Errorf("writing app ConfigMap: %w", err)
	}

	// ExternalSecret — only when the env has an associated ClusterSecretStore.
	if env.StoreName != "" {
		var esCfg *ESOExternalSecretConfig
		if p.cfg.BackendConfig != nil {
			esCfg = BuildCollapsedExternalSecretForApp(
				AppEnvPublishParams{
					Project:           app.ProjectName,
					App:               app.Name,
					Env:               env.EnvName,
					Namespace:         namespace,
					Cluster:           env.ClusterRef,
					ScopeKeys:         env.ScopeKeys,
					PlatformStoreName: env.PlatformStoreName,
				},
				naming,
				*p.cfg.BackendConfig,
				orgName,
				p.cfg.Branding,
			)
		}
		if esCfg == nil {
			// Fall back to the single-key path when BackendConfig is unavailable
			// (tests / older callers) or when the collapsed builder returned nil
			// (no scopes have keys yet).
			secretName := naming.RenderAppResource(np)
			itemTitle := env.VaultItemTitle
			if itemTitle == "" {
				itemTitle = naming.RenderVaultItem(secrets.LevelAppEnv, np)
			}
			esCfg = &ESOExternalSecretConfig{
				Name:      secretName,
				Namespace: namespace,
				StoreName: env.StoreName,
				Items:     []ESOItemRef{{Key: itemTitle, StoreName: env.StoreName}},
				Branding:  p.cfg.Branding,
			}
		}
		content := BuildCollapsedExternalSecretYAML(*esCfg)
		if err := p.writeFile(filepath.Join(dir, "external-secret.yaml"), []byte(content)); err != nil {
			return err
		}
	}

	// Historical note: this used to also write a kustomization.yaml so
	// ArgoCD would apply the per-app manifests via kustomize. Removed
	// because ArgoCD's `directory:` source (with our include filter)
	// treats every listed file as a plain manifest — it shipped the
	// kustomization.yaml itself to the API server, which then 404'd on
	// "no Kustomization CRD installed". The include filter is sufficient
	// on its own; env-configmap + external-secret get applied directly.
	return nil
}

// syncChart materialises the Helm chart for templateName at
// charts/{templateName}/ inside the cloned gitops repo so ArgoCD's
// ApplicationSet can resolve "charts/{{template}}".
//
// Resolution order:
//  1. Local disk: TemplatesDir/{templateName}/chart/ (built-ins & dev mode).
//  2. Cluster bundle: ChartFetcher.LoadChartBundle(templateName), the
//     packaged .tgz stored alongside templates imported via the BYO-chart
//     flow.
//
// Both paths are no-ops when their source is missing — same as before — so
// existing callers that don't configure ChartFetcher keep prior behaviour.
// When the chart directory already exists with identical content the
// subsequent git commit is a no-op via stagedIsEmpty.
func (p *Publisher) syncChart(ctx context.Context, repoDir, templateName string) error {
	dstDir := p.outputDir(repoDir, "charts", templateName)

	if p.cfg.TemplatesDir != "" {
		srcDir := filepath.Join(p.cfg.TemplatesDir, templateName, "chart")
		if _, err := os.Stat(srcDir); err == nil {
			return p.copyChartDir(srcDir, dstDir)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat chart dir %s: %w", srcDir, err)
		}
	}

	if p.cfg.ChartFetcher == nil {
		slog.Debug("gitops: no local chart and no ChartFetcher configured", "template", templateName)
		return nil
	}
	data, err := p.cfg.ChartFetcher.LoadChartBundle(ctx, templateName)
	if err != nil {
		return fmt.Errorf("fetch chart bundle for %s: %w", templateName, err)
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
	// StoreName is the ClusterSecretStore name for this env.
	// When non-empty, publishAppFiles writes a platform-managed ExternalSecret YAML
	// for this env's namespace so ESO materialises the app secrets.
	StoreName string
	// VaultItemTitle is the vault item title used in the ExternalSecret's dataFrom.
	// Typically rendered as "{project}-{app}-{env}" from ResourceNaming.
	// Ignored when StoreName is empty.
	VaultItemTitle string
	// EnvVars is the fully-merged env-var map the publisher writes into the
	// per-app ConfigMap ({app}-config) for this env. The caller is expected to
	// merge all six scope levels (org → env-type → project → app → app-env →
	// cluster) before publishing so the committed YAML is the source-of-truth
	// for what the pod sees — no chart-side multi-source merge.
	EnvVars map[string]string
	// ClusterRef is the registered cluster bound to this env. Used for the
	// cluster-scope vault item title in the collapsed ExternalSecret. Empty
	// when the env is unbound — the cluster scope is then omitted from the
	// merge regardless of ScopeKeys.
	ClusterRef string
	// ScopeKeys reports which scope levels actually have keys in the vault for
	// this app-env. Used by BuildCollapsedExternalSecretForApp to skip
	// dataFrom entries for empty scopes, since ESO would otherwise error
	// trying to extract from a non-existent vault item. The map keys are the
	// secrets.Level* constants; missing entries mean "no keys present".
	//
	// Populated by the adapter (cmd/suparship/server.go) at publish time. When
	// nil, every scope is included — back-compat behaviour for callers that
	// haven't been updated yet.
	ScopeKeys map[string]bool
	// PlatformStoreName is the ClusterSecretStore name for the platform vault
	// when running a separate-store deployment. Empty for the single-store
	// model (ClusterSecretStore lists both vaults), which is the default
	// behaviour today.
	PlatformStoreName string
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
		var previewBackend secrets.BackendType
		if p.cfg.BackendConfig != nil {
			previewBackend = p.cfg.BackendConfig.Effective()
		}
		hv := helmvalues.MapToHelmValuesForEnv(app, preview.PreviewName, domain.AppEnvPreview, preview.BaseDomain, preview.Namespace, "", p.cfg.ResourceNaming, previewOrgName, previewBackend)
		hvBytes, err := yaml.Marshal(hv)
		if err != nil {
			return fmt.Errorf("marshal preview values.yaml: %w", err)
		}
		valuesPath := p.outputDir(repoDir, "previews", app.ProjectName, preview.PreviewName, "values.yaml")
		if err := p.writeFile(valuesPath, hvBytes); err != nil {
			return err
		}

		// Write platform-managed ConfigMap and ExternalSecret (same as stable envs).
		previewDir := p.outputDir(repoDir, "previews", app.ProjectName, preview.PreviewName)
		previewPublishEnv := AppPublishEnv{
			EnvName:        preview.PreviewName,
			StoreName:      preview.StoreName,
			VaultItemTitle: preview.VaultItemTitle,
			EnvVars:        preview.EnvVars,
		}
		orgName := p.cfg.OrgName
		if orgName == "" {
			orgName = "default"
		}
		if err := p.writeAppPlatformResources(previewDir, app, preview.Namespace, previewPublishEnv, p.cfg.ResourceNaming, orgName); err != nil {
			return fmt.Errorf("writing preview platform resources: %w", err)
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
	// StoreName is the ClusterSecretStore name for this preview env.
	// When non-empty, PublishPreview writes a platform-managed ExternalSecret YAML.
	StoreName string
	// VaultItemTitle is the vault item title used in the ExternalSecret's dataFrom.
	VaultItemTitle string
	// EnvVars holds per-preview variable overrides to merge into the platform-managed
	// ConfigMap alongside app.Spec.EnvConfig.Vars.
	EnvVars map[string]string
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

// UnpublishApp removes all GitOps manifests for an app — specifically the
// app's subdirectory under every stable-env directory in gitops-output/ —
// and commits + pushes the deletion. It is a no-op if no files are found.
func (p *Publisher) UnpublishApp(ctx context.Context, projectName, appName string) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		outputDir := p.outputDir(repoDir)

		// Walk the top-level entries of gitops-output/ (each is an env dir or
		// the special "previews" directory) and remove {envDir}/{project}/{app}/.
		entries, err := os.ReadDir(outputDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("reading gitops-output: %w", err)
		}

		removed := false
		for _, entry := range entries {
			if !entry.IsDir() || entry.Name() == "previews" || entry.Name() == "_infra" {
				continue
			}
			appDir := filepath.Join(outputDir, entry.Name(), projectName, appName)
			if _, err := os.Stat(appDir); os.IsNotExist(err) {
				continue
			}
			if err := os.RemoveAll(appDir); err != nil {
				return fmt.Errorf("removing app dir %s: %w", appDir, err)
			}
			slog.Debug("gitops: removed app directory", "dir", appDir)
			removed = true
		}

		if !removed {
			slog.Debug("gitops: no app directories found — nothing to delete",
				"project", projectName, "app", appName)
			return nil
		}

		commitMsg := fmt.Sprintf("feat(apps): delete app %s/%s\n\nDeleted by suparShip.", projectName, appName)
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
└── charts/{template}/                     # bundled Helm chart sources
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
