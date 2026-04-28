package gitops

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

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
// patterns, backend config, and org name). Thread-safe for callers that
// rebuild the publisher when org config changes; for concurrent use call
// this before handing the publisher to goroutines.
func (p *Publisher) SetOrgConfig(orgName string, naming secrets.ResourceNaming, backend *secrets.BackendConfig) {
	p.cfg.OrgName = orgName
	p.cfg.ResourceNaming = naming
	p.cfg.BackendConfig = backend
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
		infraDir := filepath.Join(repoDir, "gitops-output", "_infra")

		for _, env := range envs {
			// Write {envName}-appset.yaml for this env.
			appSet := BuildArgoAppSet(env, p.cfg.ArgoCDRepoURL, AppSetOptions{
				SyncAutomated: p.cfg.SyncAutomated,
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
			stores := BuildSecretStoresForConfig(*p.cfg.BackendConfig, p.cfg.ResourceNaming, orgName)
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
// environment with the appropriate suparship labels. Pure function — no git
// or filesystem side effects — exposed so tests can verify label behaviour
// without exercising the cloned-repo plumbing.
func BuildProjectNamespaceManifest(projectName string, env ProjectNamespaceEnv) ([]byte, error) {
	labels := map[string]string{
		"suparship.io/project":    projectName,
		"suparship.io/managed-by": "suparship",
	}
	if env.ClusterRef != "" {
		labels["suparship.io/cluster"] = env.ClusterRef
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
		infraDir := filepath.Join(repoDir, "gitops-output", "_infra")
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
			data, err := BuildProjectNamespaceManifest(projectName, env)
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
		if err := p.syncChart(repoDir, app.Spec.Template.Name); err != nil {
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
		appMetaPath := filepath.Join(repoDir, "gitops-output", env.EnvName, app.ProjectName, app.Name, "app.yaml")
		if err := p.writeFile(appMetaPath, appMetaBytes); err != nil {
			return err
		}

		// Write values.yaml — Helm values with env-specific baseDomain and
		// the resolved namespace so secretName/configName are consistent.
		hv := helmvalues.MapToHelmValuesForEnv(app, env.EnvName, env.EnvType, env.BaseDomain, env.Namespace)
		hvBytes, err := yaml.Marshal(hv)
		if err != nil {
			return fmt.Errorf("marshal values.yaml for env %s: %w", env.EnvName, err)
		}
		valuesPath := filepath.Join(repoDir, "gitops-output", env.EnvName, app.ProjectName, app.Name, "values.yaml")
		if err := p.writeFile(valuesPath, hvBytes); err != nil {
			return err
		}

		// Write platform-managed per-app resources into the same directory as
		// app.yaml/values.yaml so ArgoCD picks them up with the same ApplicationSet.
		appDir := filepath.Join(repoDir, "gitops-output", env.EnvName, app.ProjectName, app.Name)
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

	// ConfigMap — always written with merged app + env vars.
	cmName := naming.RenderAppConfigMap(np)
	vars := mergeVars(app.Spec.EnvConfig.Vars, env.EnvVars)
	if err := p.WriteAppConfigMap(dir, cmName, namespace, vars); err != nil {
		return fmt.Errorf("writing app ConfigMap: %w", err)
	}

	// ExternalSecret — only when the env has an associated ClusterSecretStore.
	if env.StoreName == "" {
		return nil
	}

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
		}
	}
	content := BuildCollapsedExternalSecretYAML(*esCfg)
	return p.writeFile(filepath.Join(dir, "external-secret.yaml"), []byte(content))
}

// mergeVars merges base vars with overrides. Override values win on conflict.
// Returns nil when both inputs are empty.
func mergeVars(base, overrides map[string]string) map[string]string {
	if len(base) == 0 && len(overrides) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overrides {
		merged[k] = v
	}
	return merged
}

// syncChart copies the Helm chart for templateName from the local templates
// directory into charts/{templateName}/ inside the cloned gitops repo.
// This ensures ArgoCD's ApplicationSet can resolve the chart path
// ("charts/{{template}}") for every template that has been published.
//
// When TemplatesDir is not configured, syncChart is a no-op.
// When the chart directory already exists with identical content, the
// subsequent git commit will be a no-op (via stagedIsEmpty).
func (p *Publisher) syncChart(repoDir, templateName string) error {
	if p.cfg.TemplatesDir == "" {
		return nil
	}

	srcDir := filepath.Join(p.cfg.TemplatesDir, templateName, "chart")
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		slog.Debug("gitops: no chart directory for template, skipping sync", "template", templateName)
		return nil
	}

	dstDir := filepath.Join(repoDir, "charts", templateName)

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
			return fmt.Errorf("reading chart file %s: %w", path, err)
		}
		if err := p.writeFile(dst, data); err != nil {
			return err
		}
		return nil
	})
}

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
	kargoDir := filepath.Join(repoDir, "gitops-output", "_infra", "kargo")

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
	proj := BuildKargoProject(projectNS, projectEnvs)
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
	// EnvVars holds per-env variable overrides to merge into the platform-managed
	// ConfigMap alongside app.Spec.EnvConfig.Vars (env values win on conflict).
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
		metaPath := filepath.Join(repoDir, "gitops-output", "previews", app.ProjectName, preview.PreviewName, "app.yaml")
		if err := p.writeFile(metaPath, metaBytes); err != nil {
			return err
		}

		hv := helmvalues.MapToHelmValuesForEnv(app, preview.PreviewName, domain.AppEnvPreview, preview.BaseDomain, preview.Namespace)
		hvBytes, err := yaml.Marshal(hv)
		if err != nil {
			return fmt.Errorf("marshal preview values.yaml: %w", err)
		}
		valuesPath := filepath.Join(repoDir, "gitops-output", "previews", app.ProjectName, preview.PreviewName, "values.yaml")
		if err := p.writeFile(valuesPath, hvBytes); err != nil {
			return err
		}

		// Write platform-managed ConfigMap and ExternalSecret (same as stable envs).
		previewDir := filepath.Join(repoDir, "gitops-output", "previews", app.ProjectName, preview.PreviewName)
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
		previewDir := filepath.Join(repoDir, "gitops-output", "previews", projectName, previewName)
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
		outputDir := filepath.Join(repoDir, "gitops-output")

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
// with the repo path, and removes the temp directory when done.
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

	return fn(repoDir)
}

// writeFile creates parent directories and writes data to path.
func (p *Publisher) writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dirs for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
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
