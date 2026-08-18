package gitops

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/helmvalues"
	"github.com/suparcloud/suparship/internal/platform"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/tpl"
	"gopkg.in/yaml.v3"
)

// Default Git author for commits suparship makes to the gitops repo, used when
// the operator hasn't configured a custom CommitAuthorName / CommitAuthorEmail.
const (
	DefaultCommitAuthorName  = "suparship"
	DefaultCommitAuthorEmail = "suparship@suparcloud.io"
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
	// CommitAuthorName / CommitAuthorEmail set the Git author on commits the
	// publisher makes. Empty falls back to DefaultCommitAuthorName / Email.
	CommitAuthorName  string
	CommitAuthorEmail string
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
	//
	// Prefer BackendConfigFunc. This is a boot-time snapshot, and the backend can
	// be switched at runtime.
	BackendConfig *secrets.BackendConfig
	// BackendConfigFunc returns the CURRENT backend config, and takes precedence
	// over BackendConfig.
	//
	// The backend decides what every app's ExternalSecret says: which
	// ClusterSecretStore it names (unified for 1Password/Vault vs per-vault for
	// k8s — storeForScope) and, for Vault, whether the remoteRef key is qualified
	// with its container path (itemKeyFor). Rendering that from a stale snapshot
	// points apps at a store that does not exist on their cluster, and ESO fails
	// with a config error that says nothing about the cause. Nil falls back to
	// BackendConfig, then to BackendK8s.
	BackendConfigFunc func() secrets.BackendConfig
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
	// repoCacheDir is a persistent working clone of the gitops repo, refreshed
	// with fetch+reset instead of a full clone on every publish (see
	// withClonedRepo). Derived once from RepoURL+Branch in NewPublisher.
	repoCacheDir string
}

// repoLocks serializes access to each persistent repo cache dir across all
// Publisher instances that share it (keyed by cache dir), so concurrent
// publishes can't corrupt a single working tree. One in-flight publish per
// gitops repo+branch; git push already serializes at the origin anyway.
var repoLocks sync.Map // cacheDir(string) -> *sync.Mutex

func repoLockFor(dir string) *sync.Mutex {
	m, _ := repoLocks.LoadOrStore(dir, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// SetOrgConfig updates the publisher's org-scoped configuration (naming
// patterns, backend config, org name, branding, routing profiles).
// Thread-safe for callers that rebuild the publisher when org config changes;
// for concurrent use call this before handing the publisher to goroutines.
func (p *Publisher) SetOrgConfig(orgName string, naming secrets.ResourceNaming, backend *secrets.BackendConfig, brand branding.Config, routingProfiles domain.RoutingProfiles) {
	p.cfg.OrgName = orgName
	p.cfg.ResourceNaming = naming
	p.cfg.BackendConfig = backend
	p.cfg.Branding = brand
	p.cfg.RoutingProfiles = routingProfiles
}

// externalSecretRefreshInterval is the org-configured ExternalSecret refresh
// interval (secrets.DefaultRefreshInterval when unset / no backend config).
func (p *Publisher) externalSecretRefreshInterval() string {
	if p.cfg.BackendConfig == nil {
		return secrets.DefaultRefreshInterval
	}
	return p.cfg.BackendConfig.ExternalSecrets.EffectiveRefreshInterval()
}

// effectiveBackend returns the org's secret backend type (k8s when no backend
// config is set), which selects the ESO store/key layout — see
// WorkloadExternalSecretParams.Backend.
func (p *Publisher) effectiveBackend() secrets.BackendType {
	if p.cfg.BackendConfigFunc != nil {
		cfg := p.cfg.BackendConfigFunc()
		return cfg.Effective()
	}
	if p.cfg.BackendConfig == nil {
		return secrets.BackendK8s
	}
	return p.cfg.BackendConfig.Effective()
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
	// Stable, credential-free cache dir per (repo, branch) so the working clone
	// is reused across publishes (and shared/serialized between Publisher
	// instances pointing at the same repo via repoLocks).
	sum := sha256.Sum256([]byte(cfg.RepoURL + "\n" + cfg.Branch))
	cacheDir := filepath.Join(os.TempDir(), "suparship-gitops-"+hex.EncodeToString(sum[:8]), "gitops")
	return &Publisher{cfg: cfg, repoCacheDir: cacheDir}, nil
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
		if err := p.writeEnvInfra(repoDir, projectName, envs); err != nil {
			return err
		}
		return p.commitAndPush(ctx, repoDir, "feat(infra): update appsets and appprojects for "+projectName)
	})
}

// writeEnvInfra writes the per-env ApplicationSets + per-project AppProject into
// repoDir (no git). Extracted from PublishEnvInfra so the batch publisher can
// write many apps' infra into one clone and commit once.
func (p *Publisher) writeEnvInfra(repoDir, projectName string, envs []AppSetEnv) error {
	{
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
				SyncAutomated:      p.cfg.SyncAutomated,
				SubPath:            p.cfg.SubPath,
				ArgoAppNamePattern: p.cfg.ResourceNaming.EffectiveArgoAppName(),
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
				SyncAutomated:      p.cfg.SyncAutomated,
				SubPath:            p.cfg.SubPath,
				ArgoAppNamePattern: p.cfg.ResourceNaming.EffectiveArgoAppName(),
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
			Description:           "suparship project: " + projectName,
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
	}
	return nil
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

// enabledDeployEnvs returns the deploy set for a direct-delivery app: previews
// always, plus the bound stable envs the app opts into. The base env (lowest
// Order) deploys by default; higher envs are opt-in; an explicit per-env Deploy
// override (app.Spec.EnvironmentDefaults[env].Deploy) wins either way. A
// disabled env is simply omitted — its existing files are left untouched, so the
// workload keeps running until an explicit removal. Unbound envs are skipped.
func enabledDeployEnvs(app *domain.App, envs []AppPublishEnv) []AppPublishEnv {
	// Determine the base (lowest-Order) bound stable env.
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
	baseEnv := ""
	if len(stable) > 0 {
		baseEnv = stable[0].EnvName
	}

	result := make([]AppPublishEnv, 0, len(envs))
	for _, env := range envs {
		if env.EnvType == domain.AppEnvPreview {
			result = append(result, env)
			continue
		}
		if env.Bound && app.Spec.DeploysToEnv(env.EnvName, env.EnvName == baseEnv) {
			result = append(result, env)
		}
	}
	return result
}

// promotedDeployEnvs extends a pipeline app's first-deploy set with every bound
// stable env whose app files ALREADY exist in the cloned repo — an env a
// promotion has materialized via PublishAppEnv. firstDeployEnvs keeps a
// never-promoted higher env absent (ArgoCD must not deploy it before its first
// promotion), but once the env is live its files must track config edits the
// same way the base env's do — otherwise saving prod values commits nothing
// until the next promotion. An env explicitly decommissioned (Deploy=false) is
// left untouched, matching enabledDeployEnvs; promoted image tags survive the
// republish via the existing preserveTag / pinnedTag handling.
func (p *Publisher) promotedDeployEnvs(repoDir string, app *domain.App, envs, deployEnvs []AppPublishEnv) []AppPublishEnv {
	included := make(map[string]bool, len(deployEnvs))
	for _, env := range deployEnvs {
		included[env.EnvName] = true
	}
	out := deployEnvs
	for _, env := range envs {
		if env.EnvType == domain.AppEnvPreview || !env.Bound || included[env.EnvName] {
			continue
		}
		if ov, ok := app.Spec.EnvironmentDefaults[env.EnvName]; ok && ov.Deploy != nil && !*ov.Deploy {
			continue
		}
		if p.envMaterialized(repoDir, env, app.ProjectName, app.Name) {
			out = append(out, env)
		}
	}
	return out
}

// envMaterialized reports whether an env already carries this app's files in
// the repo, in any layout: the inline values tree (single-source values and
// _targets, and composed per-component values, all under envs/), the
// external-mode tree, or the composed rendered-Application tree.
func (p *Publisher) envMaterialized(repoDir string, env AppPublishEnv, projectName, appName string) bool {
	return dirNonEmpty(p.appEnvDir(repoDir, env, projectName, appName)) ||
		dirNonEmpty(p.appEnvDirExternal(repoDir, env, projectName, appName)) ||
		dirNonEmpty(p.composedAppDir(repoDir, env, projectName, appName))
}

// PublishApp writes the per-app app.yaml and values.yaml for each environment,
// plus Kargo Warehouse and Stage CRs so promotions are wired automatically.
//
// On initial creation only the first bound stable environment (lowest Order)
// and any preview environments receive app.yaml + values.yaml. Higher stable
// environments are intentionally skipped so ArgoCD does not deploy to them
// until an explicit promotion writes their files via PublishAppEnv. Once an
// env has been promoted (its files exist in the repo), republishes keep
// updating it so config edits reach every deployed env, not just the base.
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
		if err := p.writeAppTree(ctx, repoDir, app, envs); err != nil {
			return err
		}
		commitMsg := fmt.Sprintf("feat(apps): publish %s/%s\n\nCreated by suparship.", app.ProjectName, app.Name)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// writeAppTree writes one app's full gitops tree into repoDir (no git): the
// deploy-env values/app.yaml, the inline chart bytes, addon charts, and the
// Kargo Warehouse/Stage CRs (or their cleanup for direct apps). Extracted from
// PublishApp so the batch publisher (PublishApps) can write many apps into one
// clone and commit once.
func (p *Publisher) writeAppTree(ctx context.Context, repoDir string, app *domain.App, envs []AppPublishEnv) error {
	// Composed apps (>=1 component carries its own Template) render as one
	// multi-source Application per (app, cluster) — a separate tree with its own
	// per-env App-of-Apps. Handled entirely by writeComposedAppTree; the
	// single-chart path below is bypassed. MVP: no Kargo / addons wiring yet.
	if app.Spec.IsComposed() {
		// The app may have just become composed (edit added components). Drop any
		// single-chart leftovers so the single-chart ApplicationSet stops
		// generating an orphaned Application and no stale Kargo stages linger.
		if err := p.pruneSingleSourceArtifacts(repoDir, app, envs); err != nil {
			return err
		}
		return p.writeComposedAppTree(ctx, repoDir, app, envs)
	}

	// The app may have just become single (edit removed components down to one).
	// Drop any composed tree left behind so its multi-source Application is pruned.
	if err := p.pruneComposedArtifacts(repoDir, app, envs); err != nil {
		return err
	}

	// Pipeline apps deploy only the first stable env (+ previews) on create;
	// prod waits for a promotion — but an env a promotion has already
	// materialized keeps receiving republishes so config edits reach it.
	// Direct apps have no promotion, so every bound stable env deploys from
	// its own values immediately.
	deployEnvs := firstDeployEnvs(envs)
	if app.Spec.IsDirect() {
		deployEnvs = enabledDeployEnvs(app, envs)
	} else {
		deployEnvs = p.promotedDeployEnvs(repoDir, app, envs, deployEnvs)
	}
	if err := p.publishAppFiles(repoDir, app, deployEnvs); err != nil {
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
		// Env-scoped template pins may put an env on a DIFFERENT version than
		// the app-wide pin — sync each distinct one too (syncChart dedups an
		// already-present immutable version).
		for _, env := range deployEnvs {
			envApp := domain.AppForEnvTemplateVersions(app, env.EnvName)
			if v := envApp.Spec.Template.Version; v != app.Spec.Template.Version {
				if err := p.syncChart(ctx, repoDir, app.Spec.Template.Name, v); err != nil {
					return fmt.Errorf("sync chart for template %s@%s (env %s): %w", app.Spec.Template.Name, v, env.EnvName, err)
				}
			}
		}
	}

	// Write Kargo Warehouse + Stage CRs for all bound stable envs so the
	// full promotion pipeline is wired from day one. Direct-delivery apps have
	// no pipeline — remove any Kargo CRs the app may have had (e.g. it was
	// switched from pipeline→direct) so they don't linger and keep watching.
	if app.Spec.IsDirect() {
		if err := p.cleanupKargoCRs(repoDir, app); err != nil {
			return fmt.Errorf("cleanup kargo CRs for %s/%s: %w", app.ProjectName, app.Name, err)
		}
	} else if err := p.publishKargoCRs(repoDir, app, envs); err != nil {
		return fmt.Errorf("write kargo CRs for %s/%s: %w", app.ProjectName, app.Name, err)
	}
	return nil
}

// writeComposedAppTree is the composed-app counterpart of writeAppTree: it syncs
// each component's chart, writes one per-component values.yaml plus a fully
// rendered multi-source Application manifest per (app, cluster), and the per-env
// composed App-of-Apps. MVP scope: single target cluster, all-inline component
// charts, no previews, no Kargo / addon wiring (later phases).
func (p *Publisher) writeComposedAppTree(ctx context.Context, repoDir string, app *domain.App, envs []AppPublishEnv) error {
	deployEnvs := firstDeployEnvs(envs)
	if app.Spec.IsDirect() {
		deployEnvs = enabledDeployEnvs(app, envs)
	} else {
		deployEnvs = p.promotedDeployEnvs(repoDir, app, envs, deployEnvs)
	}

	// Sync each component's chart into charts/{template}/{versionDir}/ so the
	// rendered Application's chart sources resolve. syncChart dedups an
	// already-present immutable version (dirNonEmpty), so two components sharing
	// a template@version copy the bytes once. Resolve each component's canonical
	// values key (the fixed key its chart reads, e.g. web-service→"web") once
	// here, where the context is available, and pass the map down.
	componentKeys := make(map[string]string, len(app.Spec.Components))
	// Per-component values mode: a canonical (suparship-common) component gets the
	// full canonical doc; a BYO/passthrough component gets only its overlay +
	// ((platform.*)) tokens — resolved here where the template loader is available.
	componentCanonical := make(map[string]bool, len(app.Spec.Components))
	for _, c := range app.Spec.ComposedComponents() {
		if err := p.syncChart(ctx, repoDir, c.Template.Name, c.Template.Version); err != nil {
			return fmt.Errorf("sync chart for component %s (%s@%s): %w", c.Name, c.Template.Name, c.Template.Version, err)
		}
		componentKeys[c.Name] = p.resolveComponentKey(ctx, c.Template.Name, c.Name)
		componentCanonical[c.Name] = p.resolveComponentCanonical(ctx, c.Template.Name)
	}
	// Env-scoped template pins: sync any per-env component versions that differ
	// from the app-wide pins (syncChart dedups already-present versions).
	for _, env := range deployEnvs {
		envApp := domain.AppForEnvTemplateVersions(app, env.EnvName)
		if envApp == app {
			continue
		}
		for _, c := range envApp.Spec.ComposedComponents() {
			if err := p.syncChart(ctx, repoDir, c.Template.Name, c.Template.Version); err != nil {
				return fmt.Errorf("sync chart for component %s (%s@%s, env %s): %w", c.Name, c.Template.Name, c.Template.Version, env.EnvName, err)
			}
		}
	}

	if err := p.publishComposedAppFiles(repoDir, app, deployEnvs, componentKeys, componentCanonical); err != nil {
		return err
	}

	// Kargo wiring (Phase 2): pipeline composed apps get a Warehouse (subscribing
	// to every component image) + per-env Stages whose promotion writes each
	// component's tag into its own components/<name>/values.yaml. Direct apps have
	// no promotion — drop any Kargo CRs a pipeline→direct switch left behind.
	if app.Spec.IsDirect() {
		if err := p.cleanupKargoCRs(repoDir, app); err != nil {
			return fmt.Errorf("cleanup kargo CRs for %s/%s: %w", app.ProjectName, app.Name, err)
		}
	} else if err := p.publishKargoCRs(repoDir, app, envs); err != nil {
		return fmt.Errorf("write kargo CRs for %s/%s: %w", app.ProjectName, app.Name, err)
	}
	return nil
}

// resolveComponentKey returns the values key a component template's chart reads
// its config under — its first declared component name (web-service → "web").
// Falls back to fallback (the component's own name) when no TemplateLoader is
// configured or the template declares no components, so a chart authored to read
// components.<its-own-name> works without a loader.
func (p *Publisher) resolveComponentKey(ctx context.Context, templateName, fallback string) string {
	if p.cfg.TemplateLoader == nil {
		return fallback
	}
	tmpl, err := p.cfg.TemplateLoader.LoadTemplate(ctx, templateName)
	if err != nil || tmpl == nil || len(tmpl.Spec.Components) == 0 {
		return fallback
	}
	return tmpl.Spec.Components[0].Name
}

// resolveComponentCanonical reports whether a composed component's template wants
// the canonical suparship-common values injected (default), or is a BYO/passthrough
// chart (InjectCanonicalValues:false) that gets ONLY its own overlay + ((platform.*))
// tokens — mirroring the single-source path's AppPublishEnv.SkipCanonicalBase. When
// no TemplateLoader is configured or the load fails, defaults to canonical (true) so
// a canonical chart is never wrongly stripped of its values.
func (p *Publisher) resolveComponentCanonical(ctx context.Context, templateName string) bool {
	if p.cfg.TemplateLoader == nil {
		return true
	}
	tmpl, err := p.cfg.TemplateLoader.LoadTemplate(ctx, templateName)
	if err != nil || tmpl == nil {
		return true
	}
	return tmpl.Spec.CanonicalValues()
}

// pruneSingleSourceArtifacts removes the single-chart tree an app leaves behind
// when it becomes composed: the per-env values.yaml/app.yaml AND the per-cluster
// _targets/<cluster>/app.yaml files, so the single-chart ApplicationSet stops
// generating an orphaned Application. Removing the flat values.yaml without also
// removing _targets/ leaves a phantom single Application whose ValuesPath points at
// the deleted values.yaml → "no such file or directory" at render. Kargo CRs are
// NOT pruned here — composed apps publish their own Warehouse/Stages under the same
// filenames, so writeComposedAppTree's publishKargoCRs overwrites them. Safe to
// call unconditionally — a no-op for an app that was never single.
func (p *Publisher) pruneSingleSourceArtifacts(repoDir string, app *domain.App, envs []AppPublishEnv) error {
	for _, env := range envs {
		dir := p.appEnvDir(repoDir, env, app.ProjectName, app.Name)
		for _, f := range []string{"values.yaml", "app.yaml"} {
			if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("prune single-source %s (env %s): %w", f, env.EnvName, err)
			}
		}
		// The stable-env layout writes each target's app.yaml under
		// _targets/<cluster>/ (and fan-out values under _clusters/). Remove the whole
		// _targets dir so no per-cluster app.yaml lingers to generate a phantom
		// single-source Application after the switch to composed.
		if err := os.RemoveAll(filepath.Join(dir, "_targets")); err != nil {
			return fmt.Errorf("prune single-source _targets (env %s): %w", env.EnvName, err)
		}
	}
	return nil
}

// pruneComposedArtifacts removes the composed tree an app leaves behind when it
// becomes single: the per-env components/ values dir and the rendered multi-source
// Application manifests under _composed-apps/. Safe to call unconditionally — a
// no-op for an app that was never composed.
func (p *Publisher) pruneComposedArtifacts(repoDir string, app *domain.App, envs []AppPublishEnv) error {
	for _, env := range envs {
		if err := os.RemoveAll(p.appEnvDir(repoDir, env, app.ProjectName, app.Name, "components")); err != nil {
			return fmt.Errorf("prune composed components (env %s): %w", env.EnvName, err)
		}
		if err := os.RemoveAll(p.composedAppDir(repoDir, env, app.ProjectName, app.Name)); err != nil {
			return fmt.Errorf("prune composed manifests (env %s): %w", env.EnvName, err)
		}
	}
	return nil
}

// composedAppDir builds a path under the manifest-only _composed-apps/{env} tree
// where a composed app's rendered Application manifests live — disjoint from the
// values tree (envs/) so the per-env composed App-of-Apps directory source
// renders only Application manifests. Composed apps don't support previews yet,
// so this is always the stable-env layout.
func (p *Publisher) composedAppDir(repoDir string, env AppPublishEnv, parts ...string) string {
	all := append([]string{composedAppsDir, env.EnvName}, parts...)
	return p.outputDir(repoDir, all...)
}

// publishComposedAppFiles writes, for each deployable stable env: one
// values.yaml per component (a single-component projection of the canonical
// values), the rendered multi-source Application manifest for the env's active
// cluster, the app's platform resources (ConfigMap + ExternalSecret), and the
// per-env composed App-of-Apps into _infra/. Preview and unbound envs are
// skipped with a warning.
// componentConfigProjection returns, for a component that opts OUT of the app-wide
// vars (InheritAppVars=false), the name of its curated ConfigMap and the resolved
// vars to put in it: each EnvVar is a literal Value or the value of the selected
// app-config key (FromConfig). FromSecret is reserved for a future secret-subset
// increment and is skipped here. Returns ("", nil) when the component inherits.
func componentConfigProjection(appName string, c domain.ComponentSpec, appVars map[string]string) (string, map[string]string) {
	if c.InheritAppVars == nil || *c.InheritAppVars {
		return "", nil
	}
	name := secrets.AppComponentConfigMapName(appName, c.Name)
	vars := make(map[string]string, len(c.EnvVars))
	for _, e := range c.EnvVars {
		switch {
		case e.FromConfig != "":
			if v, ok := appVars[e.FromConfig]; ok {
				vars[e.Name] = v
			}
		case e.FromSecret != "":
			// deferred (secret subset/rename) — see the plan.
		default:
			vars[e.Name] = e.Value
		}
	}
	return name, vars
}

// writeComponentConfigMap writes a curated per-component ConfigMap (the object
// behind that component's platform.configMapName) into the app's _app-resources
// tree, alongside the app-wide env-configmap, so the platform ApplicationSet ships it.
func (p *Publisher) writeComponentConfigMap(repoDir string, env AppPublishEnv, app *domain.App, component, name, namespace string, vars map[string]string) error {
	resDir := p.outputDir(repoDir, "_app-resources", env.EnvName, app.ProjectName, app.Name)
	content := BuildAppConfigMapYAML(name, namespace, vars, p.cfg.Branding)
	return p.writeFile(filepath.Join(resDir, "component-"+component+"-configmap.yaml"), []byte(content))
}

// componentSecretRenames returns target-key → source-key for a component that
// curates a SUBSET of the app's secret keys (opt-out + FromSecret entries), or nil.
func componentSecretRenames(c domain.ComponentSpec) map[string]string {
	if c.InheritAppVars == nil || *c.InheritAppVars {
		return nil
	}
	m := make(map[string]string)
	for _, e := range c.EnvVars {
		if e.FromSecret != "" {
			m[e.Name] = e.FromSecret
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// buildComponentExternalSecret builds the curated per-component ExternalSecret (the
// object behind platform.secretName) that projects the selected+renamed app secret
// keys, or nil when no requested key resolves in this env's scope.
func (p *Publisher) buildComponentExternalSecret(env AppPublishEnv, app *domain.App, component, namespace string, renames map[string]string) *ESOExternalSecretConfig {
	name := secrets.AppComponentSecretName(app.Name, component)
	return BuildComponentExternalSecret(WorkloadExternalSecretParams{
		App:             app.Name,
		Namespace:       namespace,
		Env:             env.EnvName,
		Project:         app.ProjectName,
		Stack:           app.Spec.Stack,
		Cluster:         env.ClusterRef,
		Presence:        env.ScopeKeys,
		SecretKeys:      env.ScopeSecretKeys,
		Backend:         p.effectiveBackend(),
		Branding:        p.cfg.Branding,
		RefreshInterval: p.externalSecretRefreshInterval(),
	}, name, renames)
}

// writeComponentExternalSecret writes the given per-component ExternalSecret into
// the app's platform-owned _app-resources tree.
func (p *Publisher) writeComponentExternalSecret(repoDir string, env AppPublishEnv, app *domain.App, component string, esCfg *ESOExternalSecretConfig) error {
	resDir := p.outputDir(repoDir, "_app-resources", env.EnvName, app.ProjectName, app.Name)
	content := BuildExternalSecretYAML(*esCfg)
	return p.writeFile(filepath.Join(resDir, "component-"+component+"-externalsecret.yaml"), []byte(content))
}

// pruneComponentProjections removes every per-component projection object
// (component-*-configmap.yaml / component-*-externalsecret.yaml) in an app-env's
// _app-resources dir. Callers invoke it once per env publish BEFORE writing the
// current set, so a component that reverts to inherit, is renamed, or is removed
// leaves no orphaned ConfigMap/ExternalSecret behind (the app-wide env-configmap /
// external-secret files use different names, so they're untouched).
func (p *Publisher) pruneComponentProjections(repoDir string, env AppPublishEnv, app *domain.App) error {
	resDir := p.outputDir(repoDir, "_app-resources", env.EnvName, app.ProjectName, app.Name)
	for _, pat := range []string{"component-*-configmap.yaml", "component-*-externalsecret.yaml"} {
		matches, err := filepath.Glob(filepath.Join(resDir, pat))
		if err != nil {
			return fmt.Errorf("glob component projections %s: %w", pat, err)
		}
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				return fmt.Errorf("prune component projection %s: %w", filepath.Base(m), err)
			}
		}
	}
	return nil
}

func (p *Publisher) publishComposedAppFiles(repoDir string, app *domain.App, envs []AppPublishEnv, componentKeys map[string]string, componentCanonical map[string]bool) error {
	orgName := p.cfg.OrgName
	if orgName == "" {
		orgName = "default"
	}
	namePattern := p.cfg.ResourceNaming.EffectiveArgoAppName()

	touchedEnvs := make(map[string]bool)
	for _, env := range envs {
		if !env.Bound {
			slog.Warn("gitops: skipping composed publish for unbound env — assign a cluster via Settings > Environments",
				"app", app.Name, "env", env.EnvName)
			continue
		}
		if env.EnvType == domain.AppEnvPreview {
			slog.Warn("gitops: composed apps do not support preview envs yet — skipping",
				"app", app.Name, "env", env.EnvName)
			continue
		}

		// Env-scoped template pins: everything below renders THIS env's effective
		// template versions (chart source paths in the Application), so an
		// upgraded staging runs the new chart while production keeps its own.
		app := domain.AppForEnvTemplateVersions(app, env.EnvName)

		ns := env.Namespace
		if ns == "" {
			ns = app.Name + "-" + env.EnvName
		}

		// Target clusters for this env: the app's resolved selection (fan-out to
		// several, or a single / non-active cluster), mirroring the single-source
		// path. Fallback to the env's active/sole cluster ref when unset.
		targets := env.Clusters
		if len(targets) == 0 {
			targets = []ClusterTarget{{Name: env.ClusterRef, Server: activeTarget(env).Server, BaseDomain: env.BaseDomain}}
		}
		fanOut := len(targets) > 1
		// Per-component values path: shared under {project}/{app}/components/ for a
		// single target, or fanned out under _clusters/{cluster}/… so each cluster
		// carries its own component values (its own routing host, cluster overlay,
		// and Kargo-committed tag).
		compValueParts := func(cluster, comp string) []string {
			if fanOut {
				return []string{"_clusters", cluster, app.ProjectName, app.Name, "components", comp, "values.yaml"}
			}
			return []string{app.ProjectName, app.Name, "components", comp, "values.yaml"}
		}

		// Prune stale composed trees for this app-env first, then rewrite — so a
		// removed component's values.yaml / a de-selected cluster's manifest don't
		// linger and generate a phantom resource.
		// CD.Managed: capture each component's Kargo-committed image tag(s) per
		// target cluster BEFORE the prune wipes the values files, so the republish
		// re-applies the promoted tag instead of resetting it to the overlay seed
		// (the composed analog of the single-source preserveTag). Keyed
		// cluster → component → tagKey → tag.
		preservedTags := map[string]map[string]map[string]string{}
		if app.Spec.CD.Managed {
			for _, target := range targets {
				for _, c := range app.Spec.ComposedComponents() {
					vp := p.appEnvDir(repoDir, env, compValueParts(target.Name, c.Name)...)
					for _, img := range c.Images {
						if tag := existingImageTag(vp, img.TagKey); tag != "" {
							if preservedTags[target.Name] == nil {
								preservedTags[target.Name] = map[string]map[string]string{}
							}
							if preservedTags[target.Name][c.Name] == nil {
								preservedTags[target.Name][c.Name] = map[string]string{}
							}
							preservedTags[target.Name][c.Name][img.TagKey] = tag
						}
					}
				}
			}
		}
		// A pinned stable env freezes every component's image tag to the pinned tag
		// (a promoted preview build). Applied per component below AFTER the preserved
		// tag so the pin wins and holds until unpinned — the composed analog of the
		// single-source pinnedTag. Preview envs are skipped above, so this is a stable
		// env's pin.
		pinnedTag := app.Spec.EnvironmentDefaults[env.EnvName].PinnedImageTag
		// suspended writes each component's suspend toggle (its template's SuspendKey)
		// to true so every workload scales down while the env stays published (no data
		// loss, unlike undeploy). Resume clears the override, so nothing is written and
		// the chart default (running) applies on the next republish. The composed analog
		// of the single-source suspend in publishAppFiles.
		suspended := false
		if ov, ok := app.Spec.EnvironmentDefaults[env.EnvName]; ok && ov.Suspend != nil {
			suspended = *ov.Suspend
		}
		if err := os.RemoveAll(p.appEnvDir(repoDir, env, app.ProjectName, app.Name, "components")); err != nil {
			return fmt.Errorf("prune composed component values for env %s: %w", env.EnvName, err)
		}
		// Drop every per-cluster component-values tree for this app (all clusters,
		// selected or not) so a de-selected cluster leaves no orphaned values; the
		// selected clusters are rewritten below.
		if matches, _ := filepath.Glob(p.appEnvDir(repoDir, env, "_clusters", "*", app.ProjectName, app.Name, "components")); len(matches) > 0 {
			for _, m := range matches {
				if err := os.RemoveAll(m); err != nil {
					return fmt.Errorf("prune per-cluster composed values for env %s: %w", env.EnvName, err)
				}
			}
		}
		if err := os.RemoveAll(p.composedAppDir(repoDir, env, app.ProjectName, app.Name, "_targets")); err != nil {
			return fmt.Errorf("prune composed manifests for env %s: %w", env.EnvName, err)
		}
		// Also drop stale per-component config/secret projections in _app-resources
		// so a component that reverted to inherit (or was renamed/removed) leaves no
		// orphaned ConfigMap/ExternalSecret. The current opt-out set is rewritten below.
		if err := p.pruneComponentProjections(repoDir, env, app); err != nil {
			return fmt.Errorf("prune component projections for env %s: %w", env.EnvName, err)
		}

		// Interpolate the merged app config once so both the app-wide ConfigMap and
		// any per-component projection resolve ((platform.*))/((vars.*)) the same way.
		resolvedVars := env.EnvVars
		if hasInterpToken(resolvedVars) {
			resolvedVars = p.platformVarsContext(app, env, orgName).InterpolateMap(resolvedVars)
		}

		// Env-level per-component config/secret projections (one object per env,
		// shared across the env's target clusters). A component that opts out of the
		// app-wide vars points platform.configMapName at its own curated ConfigMap
		// and gets no app secrets (platform.secretName="") unless it curates a
		// renamed subset. suparship renders the projection objects here once; the
		// per-cluster loop below only sets the two platform names on each cluster's hv.
		type componentProjection struct{ configName, secretName string }
		projections := map[string]componentProjection{}
		for _, c := range app.Spec.ComposedComponents() {
			projName, projVars := componentConfigProjection(app.Name, c, resolvedVars)
			if projName == "" {
				continue // inherits the app-wide config/secret — hv keeps the mapper defaults
			}
			proj := componentProjection{configName: projName, secretName: ""}
			if err := p.writeComponentConfigMap(repoDir, env, app, c.Name, projName, ns, projVars); err != nil {
				return fmt.Errorf("writing component config for %s env %s: %w", c.Name, env.EnvName, err)
			}
			// A component may curate a SUBSET of app secret keys (renamed) into its
			// own <app>-<component>-secrets, which platform.secretName then points at;
			// suparship renders the ExternalSecret data[] projection.
			if renames := componentSecretRenames(c); renames != nil {
				if esCfg := p.buildComponentExternalSecret(env, app, c.Name, ns, renames); esCfg != nil {
					if err := p.writeComponentExternalSecret(repoDir, env, app, c.Name, esCfg); err != nil {
						return fmt.Errorf("writing component secret for %s env %s: %w", c.Name, env.EnvName, err)
					}
					proj.secretName = esCfg.Name
				}
			}
			projections[c.Name] = proj
		}

		// Pipeline apps: authorize this env's Kargo Stage to sync the Application
		// (argocd-update). The kargo.akuity.io/authorized-stage annotation must be
		// the PROJECT-QUALIFIED stage reference "<kargo-project>:<stage>" (e.g.
		// "kargo-voiceai:voiceai-lk-sh-staging"); an unqualified stage name makes
		// Kargo reject the argocd-update with "…is not authorized". This mirrors the
		// single-source AppSet ("kargo-{{project}}:{{name}}-{env}"). Direct apps have
		// no Kargo, so no annotation.
		kargoStage := ""
		if !app.Spec.IsDirect() {
			kargoStage = KargoNamespaceForProject(app.ProjectName) + ":" + KargoStageName(app.Name, env.EnvName)
		}

		// Fan out over the env's target clusters: each writes its own per-component
		// values (single-component projection of the canonical values, with the
		// component's own Values overlay + this cluster's platform-value overlay +
		// preserved tag) and its own rendered multi-source Application manifest.
		for _, target := range targets {
			baseDomain := env.BaseDomain
			if target.BaseDomain != "" {
				baseDomain = target.BaseDomain
			}
			targetName := target.Name
			if targetName == "" {
				targetName = fallbackClusterName
			}
			server := target.Server
			if server == "" {
				server = defaultDestination
			}

			componentValues := make(map[string]string, len(app.Spec.Components))
			for _, c := range app.Spec.ComposedComponents() {
				hv := helmvalues.MapComponentHelmValuesForEnv(app, c, componentKeys[c.Name], env.EnvName, env.EnvType, baseDomain, ns, target.Name, orgName,
					p.cfg.RoutingProfiles, env.RoutingProfiles, target.RoutingProfiles)
				// Curated component: point the two platform names at the env-level
				// projection objects rendered above. The chart just envFroms them.
				if proj, ok := projections[c.Name]; ok {
					hv.Platform.ConfigMapName = proj.configName
					hv.Platform.SecretName = proj.secretName
				}
				// Component overlay, layered low→high (each later layer wins):
				//  1. the Platform-Engineer value overlays for THIS component's template —
				//     the template's DefaultValues + EnvValues[env] AND the org-level
				//     TemplateOverride (Default + Env[env] + Cluster[thisCluster]),
				//     threaded from the server as env.ComponentPlatformValues[name];
				//  2. the component's own ComponentSpec.Values;
				//  3. the per-env override (EnvironmentDefaults[env].ComponentValues[name]).
				overlay := map[string]any{}
				if pv, ok := env.ComponentPlatformValues[c.Name]; ok {
					overlay = deepMerge(deepCopyMap(pv.Default), deepCopyMap(pv.Env))
					if target.Name != "" && pv.Cluster != nil {
						overlay = deepMerge(overlay, deepCopyMap(pv.Cluster[target.Name]))
					}
				}
				overlay = deepMerge(overlay, deepCopyMap(c.Values))
				if ov, ok := app.Spec.EnvironmentDefaults[env.EnvName]; ok && len(ov.ComponentValues[c.Name]) > 0 {
					overlay = deepMerge(overlay, deepCopyMap(ov.ComponentValues[c.Name]))
				}
				// Re-apply this cluster's Kargo-committed tag(s) captured above so a
				// promoted tag survives republish (setStringAtPath on the overlay: for
				// a passthrough component the overlay IS the values; for a canonical
				// one it deep-merges over the generated doc, so the preserved tag wins).
				if tags := preservedTags[target.Name][c.Name]; len(tags) > 0 {
					o := deepCopyMap(overlay)
					if o == nil {
						o = map[string]any{}
					}
					for k, v := range tags {
						setStringAtPath(o, k, v)
					}
					overlay = o
				}
				// Pin wins over the preserved/promoted tag: freeze each of this
				// component's image tag key(s) to the pinned tag so a republish (or a
				// newer promoted image) can't override the pin until it's cleared.
				if pinnedTag != "" && len(c.Images) > 0 {
					for _, img := range c.Images {
						setStringAtPath(overlay, img.TagKey, pinnedTag)
					}
				}
				// Suspend: toggle this component's suspend key on. Only written when
				// suspended, so resume drops back to the chart default (running). Use the
				// component template's own SuspendKey; fall back to the app-level key
				// (the primary template's, defaulting to "suspend") if unset.
				if suspended {
					suspendKey := env.ComponentPlatformValues[c.Name].SuspendKey
					if suspendKey == "" {
						suspendKey = env.SuspendKey
					}
					if suspendKey != "" {
						setValueAtPath(overlay, suspendKey, true)
					}
				}
				// A BYO/passthrough component gets ONLY its own overlay (the chart's own
				// values.yaml is the Helm base); platform.* is available via ((platform.*))
				// tokens. suparship injects no canonical app/components/routing/image
				// schema. A canonical component gets the full suparship-common doc.
				var hvBytes []byte
				var err error
				if componentCanonical[c.Name] {
					hvBytes, err = marshalValuesWithOverlay(hv, overlay, env.EnvVars)
				} else {
					hvBytes, err = marshalPassthroughValues(hv.Platform, overlay, env.EnvVars)
				}
				if err != nil {
					return fmt.Errorf("marshal values.yaml for component %s env %s cluster %s: %w", c.Name, env.EnvName, targetName, err)
				}
				valuesAbs := p.appEnvDir(repoDir, env, compValueParts(target.Name, c.Name)...)
				if err := p.writeFile(valuesAbs, hvBytes); err != nil {
					return err
				}
				componentValues[c.Name] = p.envAppRelPath(AppMetadataChartTypeInline, env, compValueParts(target.Name, c.Name)...)
			}

			composedOpts := ComposedBuildOptions{
				RepoURL:         p.cfg.ArgoCDRepoURL,
				SubPath:         p.cfg.SubPath,
				AppName:         RenderArgoAppName(namePattern, app.ProjectName, app.Name, env.EnvName, targetName),
				EnvName:         env.EnvName,
				ClusterName:     targetName,
				ClusterServer:   server,
				Namespace:       ns,
				SyncAutomated:   p.cfg.SyncAutomated,
				ComponentValues: componentValues,
				KargoStage:      kargoStage,
			}
			// Main multi-source Application — the non-stateful components. Skip writing
			// it when the app is all-stateful (it would carry only the $appvalues ref
			// source, no chart sources).
			if manifest := BuildComposedApplication(app, composedOpts); len(manifest.Spec.Sources) > 1 {
				manifestBytes, err := yaml.Marshal(manifest)
				if err != nil {
					return fmt.Errorf("marshal Application for env %s cluster %s: %w", env.EnvName, targetName, err)
				}
				manifestPath := p.composedAppDir(repoDir, env, app.ProjectName, app.Name, "_targets", targetName, "application.yaml")
				if err := p.writeFile(manifestPath, manifestBytes); err != nil {
					return err
				}
			}
			// Each stateful component: its OWN prune-disabled Application, auto-discovered
			// by the recurse composed root app. Written alongside the main manifest.
			for _, c := range app.Spec.StatefulComponents() {
				compManifest := BuildComponentApplication(app, c, composedOpts)
				compBytes, err := yaml.Marshal(compManifest)
				if err != nil {
					return fmt.Errorf("marshal stateful component Application %s env %s: %w", c.Name, env.EnvName, err)
				}
				compPath := p.composedAppDir(repoDir, env, app.ProjectName, app.Name, "_targets", targetName, c.Name+"-application.yaml")
				if err := p.writeFile(compPath, compBytes); err != nil {
					return err
				}
			}
		}

		// Platform-managed per-app resources (the app-wide <app>-config +
		// <app>-secrets — the default platform.configMapName/secretName), shipped by
		// the platform ApplicationSet exactly as for a single-chart app. Env-level:
		// one set of objects per env, shared across the env's target clusters.
		appDir := p.appEnvDir(repoDir, env, app.ProjectName, app.Name)
		if err := p.writeAppPlatformResources(repoDir, appDir, app, ns, env, resolvedVars); err != nil {
			return fmt.Errorf("writing platform resources for env %s: %w", env.EnvName, err)
		}

		touchedEnvs[env.EnvName] = true
		slog.Debug("gitops: wrote composed app files", "env", env.EnvName, "app", app.Name,
			"components", len(app.Spec.ComposedComponents()), "clusters", len(targets))
	}

	// Per-env composed App-of-Apps (idempotent): a directory source over
	// _composed-apps/{env} that renders the child Application manifests. Written
	// into _infra/ so the platform root App-of-Apps discovers it.
	for envName := range touchedEnvs {
		rootApp := BuildComposedRootApp(envName, p.cfg.ArgoCDRepoURL, AppSetOptions{
			SyncAutomated: p.cfg.SyncAutomated,
			SubPath:       p.cfg.SubPath,
		})
		rootBytes, err := yaml.Marshal(rootApp)
		if err != nil {
			return fmt.Errorf("marshal composed root app for env %s: %w", envName, err)
		}
		if err := p.writeFile(p.outputDir(repoDir, "_infra", envName+"-composed-appset.yaml"), rootBytes); err != nil {
			return err
		}
	}
	return nil
}

// PublishAppEnv writes app.yaml and values.yaml for a single environment to
// the GitOps repo and commits. This is called on every explicit promotion so
// that the target environment's files land in Git before Kargo / ArgoCD act.
//
// PublishAppEnv is idempotent — if the files already contain identical content
// the resulting git commit is a no-op (stagedIsEmpty check in commitAndPush).
func (p *Publisher) PublishAppEnv(ctx context.Context, app *domain.App, env AppPublishEnv) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		// Composed: write just this env's component values + rendered Application
		// (+ authorized-stage annotation) so the target env materializes on
		// promotion. Single-source: the flat app.yaml + values.yaml. The
		// Warehouse/Stages already exist from the full publish, so they're not
		// rewritten here.
		if err := p.publishEnvFiles(ctx, repoDir, app, env); err != nil {
			return err
		}
		commitMsg := fmt.Sprintf("feat(apps): publish %s/%s to %s\n\nPromoted by suparship.", app.ProjectName, app.Name, env.EnvName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// publishComposedAppEnv writes a single env's composed tree (per-component values
// + rendered multi-source Application + per-env composed App-of-Apps), resolving
// each component's values key/canonical mode and syncing its chart. It's
// writeComposedAppTree scoped to one env and without the Kargo CR write — the
// promotion path that materializes a higher env on promote.
func (p *Publisher) publishComposedAppEnv(ctx context.Context, repoDir string, app *domain.App, env AppPublishEnv) error {
	componentKeys := make(map[string]string, len(app.Spec.Components))
	componentCanonical := make(map[string]bool, len(app.Spec.Components))
	// Sync THIS env's effective chart versions (env-scoped template pins) so a
	// promotion into an env that pins a different version finds its bytes.
	envApp := domain.AppForEnvTemplateVersions(app, env.EnvName)
	for _, c := range envApp.Spec.ComposedComponents() {
		if err := p.syncChart(ctx, repoDir, c.Template.Name, c.Template.Version); err != nil {
			return fmt.Errorf("sync chart for component %s (%s@%s): %w", c.Name, c.Template.Name, c.Template.Version, err)
		}
		componentKeys[c.Name] = p.resolveComponentKey(ctx, c.Template.Name, c.Name)
		componentCanonical[c.Name] = p.resolveComponentCanonical(ctx, c.Template.Name)
	}
	return p.publishComposedAppFiles(repoDir, app, []AppPublishEnv{env}, componentKeys, componentCanonical)
}

// publishEnvFiles writes ONE env's tree for an app into an already-cloned repo,
// routing a COMPOSED app to its per-component / multi-source writer and a
// single-source app to the flat writer. It is the shared per-env body behind
// PublishAppEnv and the batched focus-env loops (PublishApps / PublishAppsEnv), so a
// pinned / suspended / promoted env of a composed app materializes as composed —
// not as an orphaned single-source app.yaml + values.yaml (using the app's
// "primary" template), which is what broke pin-to-env for app-component apps.
func (p *Publisher) publishEnvFiles(ctx context.Context, repoDir string, app *domain.App, env AppPublishEnv) error {
	if app.Spec.IsComposed() {
		return p.publishComposedAppEnv(ctx, repoDir, app, env)
	}
	return p.publishAppFiles(repoDir, app, []AppPublishEnv{env})
}

// AppEnvPublish pairs an app with one resolved env to publish. It is the unit of
// PublishAppsEnv, the batched form of PublishAppEnv.
type AppEnvPublish struct {
	App *domain.App
	Env AppPublishEnv
}

// PublishAppsEnv writes one env's values.yaml/app.yaml for MANY apps in a SINGLE
// clone/commit/push — the batched form of PublishAppEnv. It collapses a stack
// fan-out's N×(clone+commit+push) into one git round-trip (fixing the 504 on
// stack suspend/resume). Each item reuses the exact per-env writer PublishAppEnv
// uses; only the git transaction is shared.
func (p *Publisher) PublishAppsEnv(ctx context.Context, items []AppEnvPublish) error {
	if len(items) == 0 {
		return nil
	}
	return p.withClonedRepo(ctx, func(repoDir string) error {
		for _, it := range items {
			if err := p.publishEnvFiles(ctx, repoDir, it.App, it.Env); err != nil {
				return err
			}
		}
		commitMsg := fmt.Sprintf("feat(apps): batch env publish (%d app(s))\n\nCreated by suparship.", len(items))
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// AppPublishBundle is one app's inputs for the batched PublishApps: its full
// stable-env set (PublishApp semantics — infra + first-env values + Kargo CRs)
// plus optional FocusEnvs to force-write (e.g. a pinned/suspended prod that
// isn't in the pipeline's first-deploy env set).
type AppPublishBundle struct {
	App        *domain.App
	Envs       []AppPublishEnv
	AppSetEnvs []AppSetEnv
	FocusEnvs  []AppPublishEnv
}

// PublishApps publishes many apps in a SINGLE clone/commit/push — the batched
// equivalent of republishApp + PublishAppEnv(focus) per app. For each bundle it
// writes the per-project infra (once), the app tree (values + charts + Kargo
// CRs), and any focus envs, then commits once. This collapses a stack pin/unpin
// fan-out's N×(≈3 clone/commit/push) into one git round-trip (the 504 fix).
func (p *Publisher) PublishApps(ctx context.Context, bundles []AppPublishBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	return p.withClonedRepo(ctx, func(repoDir string) error {
		infraDone := map[string]bool{}
		for _, b := range bundles {
			// Infra is per-project and idempotent — write it once per project.
			if !infraDone[b.App.ProjectName] {
				if err := p.writeEnvInfra(repoDir, b.App.ProjectName, b.AppSetEnvs); err != nil {
					return err
				}
				infraDone[b.App.ProjectName] = true
			}
			if err := p.writeAppTree(ctx, repoDir, b.App, b.Envs); err != nil {
				return err
			}
			for i := range b.FocusEnvs {
				if err := p.publishEnvFiles(ctx, repoDir, b.App, b.FocusEnvs[i]); err != nil {
					return err
				}
			}
		}
		commitMsg := fmt.Sprintf("feat(apps): batch publish (%d app(s))\n\nCreated by suparship.", len(bundles))
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

		// Env-scoped template pins: app.yaml's ChartPath below must point at THIS
		// env's effective template version.
		app := domain.AppForEnvTemplateVersions(app, env.EnvName)

		// Resolve the namespace: use the pre-computed value from the caller
		// when set; fall back to the legacy "{app}-{env}" default.
		ns := env.Namespace
		if ns == "" {
			ns = app.Name + "-" + env.EnvName
		}

		// Base app.yaml fields shared across the app's target clusters. The
		// per-(app,cluster) fields (AppName/ClusterName/ClusterServer/ValuesPath)
		// are filled per target inside the write loop below.
		baseMeta := AppMetadata{
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
			baseMeta.ChartType = ChartTypeExternal
			baseMeta.ChartRepoURL = chartRef.Repository
			baseMeta.ChartName = chartRef.Name
			baseMeta.ChartVersion = chartRef.Version
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
		// When the app delegates image-tag ownership to an external CD
		// controller (Kargo), preserve the tag already committed in the target
		// values.yaml instead of re-rendering the create-time seed — otherwise a
		// republish rolls the deployed image back. Preview envs always deploy
		// their own pipeline's tag, so they are never preserved.
		preserveTag := app.Spec.CD.Managed && env.EnvType != domain.AppEnvPreview
		// pinnedTag freezes a stable env to a specific image (e.g. a PR preview's
		// tag promoted without merging). When set, it's written into every tag key
		// on each republish — overriding both the create-time seed and any
		// CD-committed tag — and Kargo auto-promotion for the stage is off (see
		// publishKargoCRs), so the pinned image holds until the env is unpinned.
		var pinnedTag string
		if env.EnvType != domain.AppEnvPreview {
			pinnedTag = app.Spec.EnvironmentDefaults[env.EnvName].PinnedImageTag
		}
		// suspended writes the chart's suspend toggle (SuspendKey) to true so the
		// workload scales down while the env stays published (no data loss, unlike
		// undeploy). Resume clears the override, so nothing is written and the
		// chart default (running) applies on the next republish.
		suspended := false
		if ov, ok := app.Spec.EnvironmentDefaults[env.EnvName]; ok && ov.Suspend != nil {
			suspended = *ov.Suspend
		}
		// The tag keys Kargo owns for this app — one per image source. Preserve
		// each on republish so we never roll a CD-managed deployment back to the
		// create-time seed. Falls back to the canonical single key when the
		// template declares no Images mapping.
		var tagKeys []string
		for _, img := range resolveKargoImages(app, env.TemplateImages) {
			tagKeys = append(tagKeys, img.TagKey)
		}

		// A single-component opt-out app may curate a secret subset into
		// <app>-<component>-secrets. Build it once (per env, not per cluster) so
		// marshalValues can point platform.secretName at it and the platform-
		// resources write below emits the ExternalSecret. nil when the component
		// inherits app secrets or no requested key resolves in this env's scope.
		var componentSecretName string
		var componentSecretCfg *ESOExternalSecretConfig
		if len(app.Spec.Components) == 1 {
			if renames := componentSecretRenames(app.Spec.Components[0]); renames != nil {
				if esCfg := p.buildComponentExternalSecret(env, app, app.Spec.Components[0].Name, ns, renames); esCfg != nil {
					componentSecretCfg = esCfg
					componentSecretName = esCfg.Name
				}
			}
		}

		// outPath is the values.yaml the bytes will be written to; it doubles as
		// the source to read back a CD-committed tag for preservation.
		marshalValues := func(c ClusterTarget, outPath string) ([]byte, error) {
			baseDomain := env.BaseDomain
			if c.BaseDomain != "" {
				baseDomain = c.BaseDomain
			}
			hv := helmvalues.MapToHelmValuesForEnv(app, env.EnvName, env.EnvType, baseDomain, env.Namespace, c.Name, orgName, p.cfg.RoutingProfiles, env.RoutingProfiles, c.RoutingProfiles)
			// Unified model: a single-component app's component name is user-chosen
			// (e.g. "api"), but its chart reads a fixed values key (web-service →
			// components.web). Remap the one component's values onto the chart's
			// canonical key so renaming the component never breaks rendering — the
			// same projection the composed path does.
			if len(app.Spec.Components) == 1 {
				name := app.Spec.Components[0].Name
				key := p.resolveComponentKey(context.Background(), app.Spec.Template.Name, name)
				if key != name {
					if cv, ok := hv.Components[name]; ok {
						delete(hv.Components, name)
						hv.Components[key] = cv
					}
					if hv.Routing.Component == name {
						hv.Routing.Component = key
					}
				}
				// Per-component env scoping: a single-component app that opts out of
				// the app vars points platform.configMapName at its curated projection
				// (written below) and gets no app secrets — same as the composed path.
				if projName, _ := componentConfigProjection(app.Name, app.Spec.Components[0], nil); projName != "" {
					hv.Platform.ConfigMapName = projName
					// Opt-out: no app-wide secrets. If the component curates a
					// secret subset, point platform.secretName at its projection
					// (written with the platform resources); else "" (no secrets).
					hv.Platform.SecretName = componentSecretName
				}
			}
			overlay := envOverlay(app, env, c.Name)
			// Unified model: a single-component app renders single-source but still
			// carries its component's own Values overlay (the value-based per-
			// component config) — apply it on top of the app/env overlay. Empty for a
			// plain single-template app, so no change there.
			if len(app.Spec.Components) == 1 && len(app.Spec.Components[0].Values) > 0 {
				overlay = deepMerge(overlay, deepCopyMap(app.Spec.Components[0].Values))
			}
			if preserveTag {
				for _, tagKey := range tagKeys {
					if tag := existingImageTag(outPath, tagKey); tag != "" {
						setStringAtPath(overlay, tagKey, tag)
					}
				}
			}
			// A pin wins over both the seed and any CD-preserved tag.
			if pinnedTag != "" {
				for _, tagKey := range tagKeys {
					setStringAtPath(overlay, tagKey, pinnedTag)
				}
			}
			// Suspend: toggle the chart's suspend key on. Only written when
			// suspended, so resume drops back to the chart default.
			if suspended && env.SuspendKey != "" {
				setValueAtPath(overlay, env.SuspendKey, true)
			}
			if env.SkipCanonicalBase {
				// BYO/passthrough: emit only the overlay; hv.Platform still drives
				// token resolution. The chart's own values.yaml is the base (Helm).
				return marshalPassthroughValues(hv.Platform, overlay, env.EnvVars)
			}
			return marshalValuesWithOverlay(hv, overlay, env.EnvVars)
		}
		if env.EnvType == domain.AppEnvPreview {
			// Previews keep the flat single app.yaml + values.yaml layout: a
			// preview always targets its base env's one cluster, and its own
			// ApplicationSet globs the preview tree — per-app cluster targeting
			// (below) is a stable-env feature only.
			target := ClusterTarget{Name: env.ClusterRef}
			if len(env.Clusters) == 1 {
				target = env.Clusters[0]
			}
			valuesPath := p.envAppDir(chartMode, repoDir, env, app.ProjectName, app.Name, "values.yaml")
			hvBytes, err := marshalValues(target, valuesPath)
			if err != nil {
				return fmt.Errorf("marshal values.yaml for env %s: %w", env.EnvName, err)
			}
			if err := p.writeFile(valuesPath, hvBytes); err != nil {
				return err
			}
			appMetaBytes, err := yaml.Marshal(baseMeta)
			if err != nil {
				return fmt.Errorf("marshal app.yaml for env %s: %w", env.EnvName, err)
			}
			if err := p.writeFile(p.envAppDir(chartMode, repoDir, env, app.ProjectName, app.Name, "app.yaml"), appMetaBytes); err != nil {
				return err
			}
		} else {
			// Stable env: write one values.yaml + one app.yaml per cluster the app
			// targets (env.Clusters is this app's resolved selection). A single
			// target uses the shared env values.yaml (unchanged layout); >1 fans
			// out to _clusters/<cluster>/ values. Each app.yaml is a plain git-file
			// the ApplicationSet turns into exactly one Application — no env-wide
			// cluster matrix — so sibling apps in the same env can target different
			// clusters. Prune stale _targets (de-selected clusters) and the legacy
			// flat app.yaml first so nothing generates a phantom Application.
			if err := os.RemoveAll(p.envAppDir(chartMode, repoDir, env, app.ProjectName, app.Name, "_targets")); err != nil {
				return fmt.Errorf("prune _targets for env %s: %w", env.EnvName, err)
			}
			_ = os.Remove(p.envAppDir(chartMode, repoDir, env, app.ProjectName, app.Name, "app.yaml"))

			targets := env.Clusters
			if len(targets) == 0 {
				// Unbound / no resolvable cluster: keep the app publishable on the
				// active ClusterRef (may be "") — falls back to the in-cluster server.
				targets = []ClusterTarget{{Name: env.ClusterRef}}
			}
			fanOut := len(targets) > 1
			namePattern := p.cfg.ResourceNaming.EffectiveArgoAppName()
			for _, c := range targets {
				targetName := c.Name
				if targetName == "" {
					targetName = fallbackClusterName
				}
				server := c.Server
				if server == "" {
					server = defaultDestination
				}
				// Shared env values file for a single target; per-cluster file when
				// fanning out. valueParts is relative to envs/{env} (or
				// envs-external/{env} for external-mode apps).
				valueParts := []string{app.ProjectName, app.Name, "values.yaml"}
				if fanOut {
					valueParts = []string{"_clusters", c.Name, app.ProjectName, app.Name, "values.yaml"}
				}
				valuesAbs := p.envAppDir(chartMode, repoDir, env, valueParts...)
				hvBytes, err := marshalValues(c, valuesAbs)
				if err != nil {
					return fmt.Errorf("marshal values.yaml for env %s cluster %s: %w", env.EnvName, targetName, err)
				}
				if err := p.writeFile(valuesAbs, hvBytes); err != nil {
					return err
				}

				// app.yaml for this (app, cluster): fully-rendered Application name
				// so existing names are unchanged, plus destination + values path.
				meta := baseMeta
				meta.AppName = RenderArgoAppName(namePattern, app.ProjectName, app.Name, env.EnvName, targetName)
				meta.ClusterName = targetName
				meta.ClusterServer = server
				meta.ValuesPath = p.envAppRelPath(chartMode, env, valueParts...)
				metaBytes, err := yaml.Marshal(meta)
				if err != nil {
					return fmt.Errorf("marshal app.yaml for env %s cluster %s: %w", env.EnvName, targetName, err)
				}
				metaPath := p.envAppDir(chartMode, repoDir, env, app.ProjectName, app.Name, "_targets", targetName, "app.yaml")
				if err := p.writeFile(metaPath, metaBytes); err != nil {
					return err
				}
			}
		}

		// Platform-managed per-app resources (ConfigMap + ExternalSecret) are
		// written to the platform-owned _app-resources/ tree and shipped by the
		// platform ApplicationSet — NOT into the app's chart Application.
		// Env-var values may reference ((platform.*))/((vars.*)); resolve them against
		// the env's ACTIVE cluster (MVP — the ConfigMap is a single per-env write).
		envVars := env.EnvVars
		if hasInterpToken(envVars) {
			ctx := p.platformVarsContext(app, env, orgName)
			envVars = ctx.InterpolateMap(envVars)
		}
		appDir := p.envAppDir(chartMode, repoDir, env, app.ProjectName, app.Name)
		if err := p.writeAppPlatformResources(repoDir, appDir, app, ns, env, envVars); err != nil {
			return fmt.Errorf("writing platform resources for env %s: %w", env.EnvName, err)
		}

		// Drop stale per-component projections before (re)writing the current
		// opt-out set, so reverting the component to inherit (or renaming it)
		// leaves no orphaned ConfigMap/ExternalSecret in _app-resources.
		if err := p.pruneComponentProjections(repoDir, env, app); err != nil {
			return fmt.Errorf("prune component projections for env %s: %w", env.EnvName, err)
		}

		// Single-component opt-out: write its curated <app>-<component>-config
		// projection (resolved from the interpolated env vars).
		if len(app.Spec.Components) == 1 {
			c0 := app.Spec.Components[0]
			if projName, projVars := componentConfigProjection(app.Name, c0, envVars); projName != "" {
				if err := p.writeComponentConfigMap(repoDir, env, app, c0.Name, projName, ns, projVars); err != nil {
					return fmt.Errorf("writing component config for %s env %s: %w", c0.Name, env.EnvName, err)
				}
				// Curated secret subset (built up-front so values.yaml could point
				// platform.secretName at it) — emit the ExternalSecret projection.
				if componentSecretCfg != nil {
					if err := p.writeComponentExternalSecret(repoDir, env, app, c0.Name, componentSecretCfg); err != nil {
						return fmt.Errorf("writing component secret for %s env %s: %w", c0.Name, env.EnvName, err)
					}
				}
			}
		}

		slog.Debug("gitops: wrote app files", "env", env.EnvName, "app", app.Name)
	}
	return nil
}

// envOverlay builds the full values overlay applied on top of the chart/base
// values for an environment + target cluster, layered low→high (later wins):
//  1. template/org PlatformDefaultValues (PE, all envs)
//  2. template/org PlatformEnvValues     (PE, this env)
//  3. org PlatformClusterValues[cluster] (PE, this cluster — env-agnostic)
//  4. stack RawValues + StackEnvRawValues (shared by the app's stack)
//  5. app + env developer RawValues      (rawValuesOverlay)
//
// cluster is the target cluster ref for the values.yaml being written (the active
// cluster in active mode, or one fan-out member); "" applies no cluster layer.
// Everything is deep-copied so neither the template nor the app spec is mutated.
func envOverlay(app *domain.App, env AppPublishEnv, cluster string) map[string]any {
	overlay := deepMerge(deepCopyMap(env.PlatformDefaultValues), deepCopyMap(env.PlatformEnvValues))
	if cluster != "" && env.PlatformClusterValues != nil {
		overlay = deepMerge(overlay, deepCopyMap(env.PlatformClusterValues[cluster]))
	}
	// Stack layer: shared overlay for the app's stack (all envs, then this env),
	// below the developer's app/app-env RawValues.
	overlay = deepMerge(overlay, deepCopyMap(env.StackRawValues))
	overlay = deepMerge(overlay, deepCopyMap(env.StackEnvRawValues))
	return deepMerge(overlay, rawValuesOverlay(app, env.EnvName))
}

// rawValuesOverlay returns the freeform Helm values overlay for an env: the
// app-level RawValues deep-merged with the env-level RawValues (env wins). Both
// are deep-copied so the stored app spec is never mutated. Returns nil when
// neither is set.
func rawValuesOverlay(app *domain.App, envName string) map[string]any {
	base := deepCopyMap(app.Spec.RawValues)
	if ov, ok := app.Spec.EnvironmentDefaults[envName]; ok && len(ov.RawValues) > 0 {
		base = deepMerge(base, deepCopyMap(ov.RawValues))
	}
	return base
}

// imageTagValuesKey is the AppSpec.Values key the canonical mapper reads the
// image tag from (mirrors helmvalues' internal imageTagKey). Overriding it for a
// preview re-tags every component image.
const imageTagValuesKey = "image_tag"

// previewRawValuesOverlay returns the freeform Helm values overlay for a
// preview: the base env's overlay (app + base-env RawValues) with the reserved
// "preview" band's RawValues merged on top (preview wins). The per-preview name
// is never an EnvironmentDefaults key — previews share one band.
func previewRawValuesOverlay(app *domain.App, preview PreviewPublishSpec) map[string]any {
	// Mirror envOverlay (the stable-env composition) for the BASE env so the
	// preview inherits the base env's template/org → cluster → stack → app value
	// overrides, then layer the reserved preview band on top. Without this the
	// preview would render only the preview band over the chart defaults, losing
	// the base env's overrides (e.g. envConfigMapName → ((platform.configMapName))).
	overlay := deepMerge(deepCopyMap(preview.PlatformDefaultValues), deepCopyMap(preview.PlatformEnvValues))
	if preview.Cluster != "" && preview.PlatformClusterValues != nil {
		overlay = deepMerge(overlay, deepCopyMap(preview.PlatformClusterValues[preview.Cluster]))
	}
	overlay = deepMerge(overlay, deepCopyMap(preview.StackRawValues))
	overlay = deepMerge(overlay, deepCopyMap(preview.StackEnvRawValues))
	overlay = deepMerge(overlay, rawValuesOverlay(app, preview.BaseEnv))
	// Template-level preview defaults: the bottom of the "preview band" — applied
	// to every preview of this template's apps, above the base-env composition and
	// below the app's own preview override (so apps can modify/extend).
	overlay = deepMerge(overlay, deepCopyMap(preview.TemplatePreviewValues))
	if ov, ok := app.Spec.EnvironmentDefaults[domain.PreviewOverrideKey]; ok && len(ov.RawValues) > 0 {
		overlay = deepMerge(overlay, deepCopyMap(ov.RawValues))
	}
	return overlay
}

// activeTarget returns the ClusterTarget matching the env's active ClusterRef,
// falling back to the sole cluster or a bare-name target.
func activeTarget(env AppPublishEnv) ClusterTarget {
	for _, c := range env.Clusters {
		if c.Name == env.ClusterRef {
			return c
		}
	}
	if len(env.Clusters) == 1 {
		return env.Clusters[0]
	}
	return ClusterTarget{Name: env.ClusterRef}
}

// platformVarsContext builds the interpolation context for env-var values using
// the env's ACTIVE cluster (MVP: env-var token resolution is not fanned out
// per-cluster — the <app>-config ConfigMap is a single per-env write). Vars is
// the merged non-secret env-var map.
func (p *Publisher) platformVarsContext(app *domain.App, env AppPublishEnv, orgName string) platform.Context {
	target := activeTarget(env)
	baseDomain := env.BaseDomain
	if target.BaseDomain != "" {
		baseDomain = target.BaseDomain
	}
	hv := helmvalues.MapToHelmValuesForEnv(app, env.EnvName, env.EnvType, baseDomain, env.Namespace, target.Name, orgName, p.cfg.RoutingProfiles, env.RoutingProfiles, target.RoutingProfiles)
	return platform.Context{Platform: hv.Platform, Vars: env.EnvVars}
}

// hasInterpToken reports whether any value in m contains an interpolation token,
// so the publisher can skip the work (and avoid churn) when none do.
func hasInterpToken(m map[string]string) bool {
	for _, v := range m {
		if platform.HasToken(v) {
			return true
		}
	}
	return false
}

// marshalValuesWithOverlay serializes hv to YAML, applying platform/((vars.*))
// interpolation and the raw-values overlay only when needed. When there is no
// overlay and no interpolation token anywhere in the values, it returns the
// struct-marshalled bytes unchanged (stable, declaration-order keys) so existing
// apps see no churn. Otherwise it round-trips hv to a map, interpolates every
// string leaf against the platform context, deep-merges the (interpolated)
// overlay on top, and marshals the result.
func marshalValuesWithOverlay(hv helmvalues.HelmValues, overlay map[string]any, vars map[string]string) ([]byte, error) {
	raw, err := yaml.Marshal(hv)
	if err != nil {
		return nil, err
	}
	needsInterp := len(overlay) > 0 || platform.HasToken(string(raw))
	if !needsInterp {
		return raw, nil
	}

	ctx := platform.Context{Platform: hv.Platform, Vars: vars}
	var base map[string]any
	if err := yaml.Unmarshal(raw, &base); err != nil {
		return nil, err
	}
	base, _ = ctx.InterpolateTree(base).(map[string]any)
	if len(overlay) > 0 {
		ov, _ := ctx.InterpolateTree(overlay).(map[string]any)
		base = deepMerge(base, ov)
	}
	return yaml.Marshal(base)
}

// marshalPassthroughValues is the BYO/passthrough counterpart: it emits ONLY the
// (interpolated) overlay — no canonical suparship-common base — so the chart's own
// values.yaml (applied by Helm underneath) is the foundation. The platform values
// are used solely for ((platform.*))/((vars.*)) token resolution, not injected as a
// values block. Returns "{}" when the overlay is empty.
func marshalPassthroughValues(pv helmvalues.PlatformValues, overlay map[string]any, vars map[string]string) ([]byte, error) {
	if len(overlay) == 0 {
		return []byte("{}\n"), nil
	}
	ctx := platform.Context{Platform: pv, Vars: vars}
	out, _ := ctx.InterpolateTree(deepCopyMap(overlay)).(map[string]any)
	return yaml.Marshal(out)
}

// deepMerge / deepCopyMap delegate to helmvalues so publish-time layering and the
// API's effective-values preview share one implementation. Kept as local wrappers
// to leave the existing call sites unchanged.
func deepMerge(base, overlay map[string]any) map[string]any {
	return helmvalues.DeepMerge(base, overlay)
}

func deepCopyMap(m map[string]any) map[string]any {
	return helmvalues.DeepCopyMap(m)
}

// existingImageTag reads an already-committed values.yaml at path and returns
// the non-empty string value at the dotted key (e.g. "image.tag"). It returns
// "" when the file is absent (first publish), unreadable, unparseable, or the
// key is missing / not a non-empty string — in all of which cases the caller
// falls back to the freshly rendered tag.
//
// This is the mechanism that lets an external CD controller (Kargo) own the
// image tag: Kargo commits the promoted tag into this file, and on the next
// republish the publisher reads it back here and re-applies it instead of the
// create-time seed from the app spec. The committed file carries a leading
// "# Generated by …" comment header; YAML parsing ignores comments.
func existingImageTag(path, dottedKey string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return ""
	}
	return stringAtPath(m, dottedKey)
}

// stringAtPath returns the non-empty string value at a dotted key path within a
// nested map (e.g. "components.web.image.tag"), or "" if any segment is missing
// or the leaf is not a non-empty string.
func stringAtPath(m map[string]any, dotted string) string {
	parts := strings.Split(dotted, ".")
	cur := m
	for i, key := range parts {
		v, ok := cur[key]
		if !ok {
			return ""
		}
		if i == len(parts)-1 {
			s, _ := v.(string)
			return s
		}
		next, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}

// setStringAtPath sets val at a dotted key path within a nested map, creating
// intermediate maps as needed. A non-map value encountered along the path is
// replaced with a map so the leaf can be written.
func setStringAtPath(m map[string]any, dotted, val string) {
	setValueAtPath(m, dotted, val)
}

// setValueAtPath sets an arbitrary-typed val at a dotted key path within a
// nested map, creating intermediate maps as needed. A non-map value encountered
// along the path is replaced with a map so the leaf can be written.
func setValueAtPath(m map[string]any, dotted string, val any) {
	parts := strings.Split(dotted, ".")
	cur := m
	for i, key := range parts {
		if i == len(parts)-1 {
			cur[key] = val
			return
		}
		next, ok := cur[key].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[key] = next
		}
		cur = next
	}
}

// writeAppPlatformResources writes the platform-managed ConfigMap + ExternalSecret
// (and a meta.yaml for the platform ApplicationSet generator) into the
// platform-owned tree at _app-resources/{env}/{project}/{app}/, then prunes any
// stale copies left in the app's own directory by older versions (which used to
// ship these alongside the chart). oldAppDir is the per-app chart directory.
//
// envVars is the merged global → env → cluster scope map (already platform-token
// interpolated by the caller), written verbatim as the audit-trail for what the
// pod sees. The ExternalSecret merges all present scopes into one <app>-secrets
// Secret; nil (skipped) when no scope has keys.
func (p *Publisher) writeAppPlatformResources(
	repoDir, oldAppDir string,
	app *domain.App,
	namespace string,
	env AppPublishEnv,
	envVars map[string]string,
) error {
	resDir := p.outputDir(repoDir, "_app-resources", env.EnvName, app.ProjectName, app.Name)
	esCfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:             app.Name,
		Namespace:       namespace,
		Env:             env.EnvName,
		Project:         app.ProjectName,
		Stack:           app.Spec.Stack,
		Cluster:         env.ClusterRef,
		Presence:        env.ScopeKeys,
		Backend:         p.effectiveBackend(),
		Branding:        p.cfg.Branding,
		RefreshInterval: p.externalSecretRefreshInterval(),
	})
	meta := PlatformAppMeta{Name: app.Name, Project: app.ProjectName, Namespace: namespace}
	if err := p.writePlatformDir(resDir, secrets.AppConfigMapName(app.Name), namespace, envVars, esCfg, meta); err != nil {
		return err
	}
	// Migration: remove platform manifests that older publishers wrote into the
	// app's own (chart) directory.
	return p.pruneLegacyPlatformFiles(oldAppDir)
}

// writePlatformDir writes meta.yaml + the <app>-config ConfigMap + the
// <app>-secrets ExternalSecret (esCfg may be nil → pruned) into resDir.
func (p *Publisher) writePlatformDir(resDir, configMapName, namespace string, envVars map[string]string, esCfg *ESOExternalSecretConfig, meta PlatformAppMeta) error {
	metaBytes, err := yaml.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal platform meta: %w", err)
	}
	if err := p.writeFile(filepath.Join(resDir, "meta.yaml"), metaBytes); err != nil {
		return err
	}
	if err := p.WriteAppConfigMap(resDir, configMapName, namespace, envVars); err != nil {
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

	// A versioned chart is immutable, so if it's already synced in the (freshly
	// cloned) repo, skip the fetch + extract. This is a large win for fan-outs
	// that republish many apps of the same template in one commit (e.g. a stack
	// pin re-publishing 6 members): the charts are already committed, so
	// re-extracting them per member is pure waste. Only the first publish of a
	// new template@version fetches. (Dev-mode disk templates above always copy,
	// so local chart edits still take effect.)
	if dirNonEmpty(dstDir) {
		return nil
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

// dirNonEmpty reports whether path is a directory that contains at least one
// entry. Used to skip re-syncing an already-present (immutable) chart version.
func dirNonEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
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
// resolveKargoImages returns the image sources the Kargo CRs should target.
//
// images is the app's CD-selected image set, already discovered from the app's
// effective Helm values and resolved to concrete Repository + TagKey by the
// publish adapter (one per image the user chose to manage; sidecars and other
// unselected images are absent). When non-empty it is used verbatim.
//
// When the app has selected no images, it falls back to a single legacy image
// derived from the app's image_repository value (canonical tag key), preserving
// behaviour for suparship-common charts that predate explicit selection.
func resolveKargoImages(app *domain.App, images []KargoImage) []KargoImage {
	if len(images) > 0 {
		return images
	}
	if repo, ok := app.Spec.Values["image_repository"].(string); ok {
		if repo = strings.TrimSpace(repo); repo != "" {
			return []KargoImage{{Repository: repo, TagKey: DefaultImageTagKey, TagPattern: ".*"}}
		}
	}
	// No selection and no image set — caller decides whether to warn.
	return nil
}

// collectComponentImages returns the composed Warehouse's image sources: the
// per-component RESOLVED images the publish adapter discovered from each
// component's own values (env.ComponentTemplateImages), in component spec order.
// Each KargoImage.Name is the owning component so the promotion targets that
// component's values file. A component whose stored ComponentSpec.Images selection
// carries an explicit legacy Repository that discovery didn't resolve falls back
// to watching that repository directly, so pre-discovery configs keep working.
func collectComponentImages(app *domain.App, resolved map[string][]KargoImage) []KargoImage {
	var out []KargoImage
	for _, c := range app.Spec.Components {
		byKey := make(map[string]bool, len(resolved[c.Name]))
		for _, img := range resolved[c.Name] {
			byKey[img.TagKey] = true
			out = append(out, img)
		}
		// Legacy fallback: an explicit-repository selection discovery couldn't match.
		for _, img := range c.Images {
			if img.Repository == "" || byKey[img.TagKey] {
				continue
			}
			tagPattern := img.TagPattern
			if tagPattern == "" {
				tagPattern = DefaultImageTagPattern
			}
			strategy := img.SelectionStrategy
			if strategy == "" {
				strategy = DefaultImageSelectionStrategy
			}
			out = append(out, KargoImage{
				Name:              c.Name,
				Repository:        img.Repository,
				TagKey:            img.TagKey,
				TagPattern:        tagPattern,
				SelectionStrategy: strategy,
			})
		}
	}
	return out
}

// SelectKargoImages resolves an app's CD image selection against the images
// discovered in its effective Helm values. For each selected image (matched by
// TagKey) it emits a KargoImage carrying the discovered Repository + TagKey, with
// the selection's TagPattern/SelectionStrategy overriding the discovered defaults
// when set. A selection whose image is no longer present in the values is skipped
// with a warning (it would otherwise produce a Warehouse subscription that never
// resolves). An empty selection yields nil — the caller falls back to legacy.
func SelectKargoImages(discovered []tpl.TemplateImage, selection []domain.AppImageBinding) []KargoImage {
	if len(selection) == 0 {
		return nil
	}
	byKey := make(map[string]tpl.TemplateImage, len(discovered))
	for _, d := range discovered {
		byKey[d.TagKey] = d
	}
	out := make([]KargoImage, 0, len(selection))
	for _, sel := range selection {
		d, ok := byKey[sel.TagKey]
		if !ok {
			slog.Warn("gitops: CD-selected image not found in values; skipping",
				"name", sel.Name, "tagKey", sel.TagKey)
			continue
		}
		img := KargoImage{
			Name:              d.Name,
			Repository:        d.Repository,
			TagKey:            d.TagKey,
			TagPattern:        d.TagPattern,
			SelectionStrategy: d.SelectionStrategy,
		}
		if sel.TagPattern != "" {
			img.TagPattern = sel.TagPattern
		}
		if sel.SelectionStrategy != "" {
			img.SelectionStrategy = sel.SelectionStrategy
		}
		// Fall back to the platform defaults (7-char commit SHA / newest build) when
		// the selection and the discovered image leave them unset, so a CD-managed
		// image rolls forward on every merge without per-image configuration.
		if img.TagPattern == "" {
			img.TagPattern = DefaultImageTagPattern
		}
		if img.SelectionStrategy == "" {
			img.SelectionStrategy = DefaultImageSelectionStrategy
		}
		out = append(out, img)
	}
	return out
}

// SelectDeclaredKargoImages builds the CD image set from the DECLARED discovered
// images (those the template declares a pull rule for — Declared=true), used as the
// default when the user has bound no images. So a template that declares its image
// pull config yields a healthy Warehouse with zero config ("inherit from template").
// Undeclared discovered images (sidecars) are ignored. Rules fall back to the
// platform defaults (7-char SHA / NewestBuild) when the slot leaves them unset.
func SelectDeclaredKargoImages(discovered []tpl.TemplateImage) []KargoImage {
	var out []KargoImage
	for _, d := range discovered {
		if !d.Declared || d.Repository == "" {
			continue
		}
		img := KargoImage{
			Name:              d.Name,
			Repository:        d.Repository,
			TagKey:            d.TagKey,
			TagPattern:        d.TagPattern,
			SelectionStrategy: d.SelectionStrategy,
		}
		if img.TagPattern == "" {
			img.TagPattern = DefaultImageTagPattern
		}
		if img.SelectionStrategy == "" {
			img.SelectionStrategy = DefaultImageSelectionStrategy
		}
		out = append(out, img)
	}
	return out
}

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
		// A decommissioned env (Deploy explicitly false) leaves the app's
		// pipeline: it gets no stage and no promotion policy, and the chain
		// re-links the surviving neighbours below (the previous env becomes
		// terminal). Its orphaned stage file is removed by removeAppEnvFiles.
		if ov, ok := app.Spec.EnvironmentDefaults[env.EnvName]; ok && ov.Deploy != nil && !*ov.Deploy {
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

	// ── Per-project Project + ProjectConfig CRs (Kargo v1.x) ───────────────────
	// Each suparship project owns one Kargo Project in its own kargo-{project}
	// namespace (Kargo binds a Project 1:1 to a namespace). The Project CR (no spec
	// in v1.x) marks that namespace as a Kargo tenancy — it is static per project,
	// so every app in the project writes identical bytes (idempotent). The
	// ProjectConfig CR is a per-namespace singleton holding the PromotionPolicies
	// (auto-promote staging, gate prod) for ALL the project's apps, so this app
	// MERGES its policies into the existing file instead of overwriting it.
	proj := BuildKargoProject(app.ProjectName, p.cfg.Branding)
	projBytes, err := yaml.Marshal(proj)
	if err != nil {
		return fmt.Errorf("marshal kargo project: %w", err)
	}
	if err := p.writeFile(filepath.Join(kargoDir, projectNS+"-project.yaml"), projBytes); err != nil {
		return err
	}
	slog.Debug("gitops: wrote kargo project", "namespace", projectNS)

	var projectEnvs []KargoProjectEnv
	for i, env := range stableEnvs {
		projectEnvs = append(projectEnvs, KargoProjectEnv{
			AppName:      app.Name,
			EnvName:      env.EnvName,
			IsFirstStage: i == 0,
			AutoPromote:  env.AutoPromote,
			// Only a real, user-facing pin (PinnedFrom set) pauses Kargo. A
			// transient unpin-restore writes PinnedImageTag with PinnedFrom empty
			// and must leave auto-promotion on.
			Pinned: app.Spec.EnvironmentDefaults[env.EnvName].PinnedFrom != "",
		})
	}
	appPolicies := BuildKargoPromotionPolicies(projectEnvs)
	existingPolicies, err := p.readKargoPromotionPolicies(kargoDir, projectNS)
	if err != nil {
		return err
	}
	merged := MergeKargoPromotionPolicies(existingPolicies, app.Name, appPolicies)
	if err := p.writeKargoProjectConfig(kargoDir, app.ProjectName, merged); err != nil {
		return err
	}
	slog.Debug("gitops: wrote kargo projectconfig", "namespace", projectNS, "policies", len(merged))

	// ── Resolve the app's image sources ────────────────────────────────────────
	// Composed apps: one image per component from its declared bindings, each
	// carrying its owning component so the promotion writes into that component's
	// values.yaml. Single-source: the template's per-service Images mapping (via
	// TemplateImages) or the legacy image_repository fallback.
	composed := app.Spec.IsComposed()
	var images []KargoImage
	if composed {
		var resolved map[string][]KargoImage
		if len(stableEnvs) > 0 {
			resolved = stableEnvs[0].ComponentTemplateImages
		}
		images = collectComponentImages(app, resolved)
	} else {
		var tmplImages []KargoImage
		if len(stableEnvs) > 0 {
			tmplImages = stableEnvs[0].TemplateImages
		}
		images = resolveKargoImages(app, tmplImages)
	}
	// ── Warehouse ──────────────────────────────────────────────────────────────
	whPath := filepath.Join(kargoDir, projectNS+"-"+app.Name+"-warehouse.yaml")
	if len(images) == 0 {
		// No image source — no user bindings, no template-declared images, and no
		// image_repository. Do NOT write a placeholder Warehouse: an unreachable
		// ghcr.io/{project}/{app} subscription just thrashes Kargo in a failing
		// refresh loop. Prune any stale Warehouse and skip; the Stages still publish,
		// and the Warehouse materializes healthy once an image is detected/bound
		// (auto-inherited from the template, or set via the image editor).
		if err := os.Remove(whPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("prune stale kargo warehouse for %s: %w", app.Name, err)
		}
		slog.Warn("gitops: no image source for CD — skipping Kargo Warehouse (promotions paused until an image is set)",
			"project", app.ProjectName, "app", app.Name, "composed", composed)
	} else {
		whOpts := KargoBuildOptions{
			Images:                images,
			InsecureSkipTLSVerify: p.cfg.InsecureRegistry,
			Branding:              p.cfg.Branding,
			SubPath:               p.cfg.SubPath,
		}
		warehouse := BuildKargoWarehouse(app, whOpts)
		whBytes, err := yaml.Marshal(warehouse)
		if err != nil {
			return fmt.Errorf("marshal kargo warehouse for %s: %w", app.Name, err)
		}
		if err := p.writeFile(whPath, whBytes); err != nil {
			return err
		}
		slog.Debug("gitops: wrote kargo warehouse", "app", app.Name)
	}

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
			Images:                images,
			InsecureSkipTLSVerify: p.cfg.InsecureRegistry,
			GitOpsRepoURL:         p.kargoGitRepoURL(),
			GitOpsRepoInsecure:    p.cfg.InsecureRegistry,
			Branding:              p.cfg.Branding,
			SubPath:               p.cfg.SubPath,
			ArgoAppNamePattern:    p.cfg.ResourceNaming.EffectiveArgoAppName(),
			Clusters:              env.Clusters,
			Composed:              composed,
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

// kargoProjectConfigPath is the gitops-repo path of a project's Kargo
// ProjectConfig manifest (one per kargo-{project} namespace).
func (p *Publisher) kargoProjectConfigPath(kargoDir, projectNS string) string {
	return filepath.Join(kargoDir, projectNS+"-projectconfig.yaml")
}

// cleanupKargoCRs removes an app's Kargo Warehouse + Stage files and drops its
// promotion policies from the shared per-project ProjectConfig. It is called for
// direct-delivery apps so an app switched from pipeline→direct doesn't leave
// stale Kargo CRs behind in Git (they'd otherwise keep watching images and
// gating a promotion that no longer exists). Idempotent: a no-op when the app
// has no Kargo files.
func (p *Publisher) cleanupKargoCRs(repoDir string, app *domain.App) error {
	kargoDir := p.outputDir(repoDir, "_infra", "kargo")
	projectNS := KargoNamespaceForProject(app.ProjectName)

	for _, pattern := range []string{
		filepath.Join(kargoDir, projectNS+"-"+app.Name+"-warehouse.yaml"),
		filepath.Join(kargoDir, projectNS+"-"+app.Name+"-*-stage.yaml"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("glob kargo files %s: %w", pattern, err)
		}
		for _, m := range matches {
			if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove kargo file %s: %w", m, err)
			}
		}
	}

	// Drop this app's promotion policies from the project's shared ProjectConfig,
	// preserving any sibling pipeline apps' policies. Only rewrite when something
	// changed, so a project with no Kargo at all doesn't get a spurious file.
	existing, err := p.readKargoPromotionPolicies(kargoDir, projectNS)
	if err != nil {
		return err
	}
	merged := MergeKargoPromotionPolicies(existing, app.Name, nil)
	if len(merged) != len(existing) {
		if err := p.writeKargoProjectConfig(kargoDir, app.ProjectName, merged); err != nil {
			return err
		}
	}
	return nil
}

// readKargoPromotionPolicies returns the promotion policies currently recorded
// in the project's ProjectConfig manifest, or nil when it does not yet exist.
// Callers merge their app's policies into the result before re-writing (multiple
// apps in a project share one ProjectConfig).
func (p *Publisher) readKargoPromotionPolicies(kargoDir, projectNS string) ([]KargoPromotionPolicy, error) {
	data, err := os.ReadFile(p.kargoProjectConfigPath(kargoDir, projectNS))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read kargo projectconfig: %w", err)
	}
	var cfg KargoProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse kargo projectconfig: %w", err)
	}
	return cfg.Spec.PromotionPolicies, nil
}

// writeKargoProjectConfig (re)writes the project's ProjectConfig manifest with
// the given (already-merged) promotion policies.
func (p *Publisher) writeKargoProjectConfig(kargoDir, projectName string, policies []KargoPromotionPolicy) error {
	cfg := BuildKargoProjectConfig(projectName, policies, p.cfg.Branding)
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal kargo projectconfig: %w", err)
	}
	return p.writeFile(p.kargoProjectConfigPath(kargoDir, KargoNamespaceForProject(projectName)), b)
}

// ComponentPlatformValues holds one composed component's template value overlays —
// the template's DefaultValues/EnvValues plus the org-level TemplateOverride
// (Default all-envs, Env this-env, Cluster per-cluster). Merged beneath the
// component's own ComponentSpec.Values in the composed render. Mirrors the
// single-source AppPublishEnv.Platform{Default,Env,Cluster}Values but per component.
type ComponentPlatformValues struct {
	Default map[string]any
	Env     map[string]any
	Cluster map[string]map[string]any
	// Preview is the component template's merged PreviewDefaultValues (TemplateSpec
	// + org TemplateOverride), applied to every preview of an app using this
	// component's template — the composed analog of PreviewPublishSpec.
	// TemplatePreviewValues. Only used on the composed preview path.
	Preview map[string]any
	// SuspendKey is the dotted Helm values key that toggles suspend for THIS
	// component's chart (the component template's declared key, or the "suspend"
	// convention default). When the env override sets Suspend=true, the composed
	// publisher writes true here in the component's values.yaml so the workload
	// scales down. The composed analog of AppPublishEnv.SuspendKey.
	SuspendKey string
}

// AppPublishEnv carries per-environment publish context for PublishApp.
type AppPublishEnv struct {
	// AutoPromote opts this app's pipeline into auto-promotion to prod (the
	// effective value of CDConfig.AutoPromote ORed with its stack's setting).
	// App-wide, carried on every env; the Kargo policy builder enables auto
	// promotion for the downstream (prod) stages when true.
	AutoPromote bool
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
	// ScopeSecretKeys carries the actual secret key NAMES per (scope, tier) item —
	// populated by the adapter only when an app has a component that curates
	// secrets, so the per-component data[] projection can resolve each key to its
	// item. Empty otherwise (the app-wide secret uses dataFrom, needing no names).
	ScopeSecretKeys ScopeSecretKeys
	// RoutingProfiles is a sparse override map for this env. Entries here
	// replace the org-level profile (PublisherConfig.RoutingProfiles) of the
	// same name; absent names inherit the org default. Populated by the
	// publish adapter from rbac.OrgEnvironment.RoutingProfiles when present.
	RoutingProfiles domain.RoutingProfiles
	// Clusters is the env's fan-out target set (deployMode "all"). When it has
	// more than one entry the publisher writes a per-cluster values.yaml under
	// envs/{env}/_clusters/{cluster}/... (each merged with that cluster's
	// overrides) instead of a single shared env values.yaml. Empty / single =
	// the legacy single values.yaml.
	Clusters []ClusterTarget
	// PlatformDefaultValues / PlatformEnvValues are the Platform-Engineer-authored
	// Helm values overlays from the app's template: DefaultValues (all envs) and
	// EnvValues[thisEnv]. Layered on top of the chart/base values and below the
	// developer RawValues, then ((platform.*))/((vars.*)) interpolated. Populated by
	// the publish adapter from the resolved template.
	PlatformDefaultValues map[string]any
	PlatformEnvValues     map[string]any
	// PlatformClusterValues are the org-level per-cluster overlays keyed by
	// cluster ref (env-agnostic), layered after PlatformEnvValues and below the
	// developer RawValues. Only the block for the target cluster of each written
	// values.yaml is applied (see envOverlay). Populated by the publish adapter
	// from the org template override.
	PlatformClusterValues map[string]map[string]any
	// ComponentPlatformValues holds the PE-authored value overlays for EACH composed
	// component's OWN template (keyed by component name) — the single-source
	// Platform{Default,Env,Cluster}Values equivalent, but per component. Populated by
	// the publish adapter only for composed apps (each component has its own template
	// + org override). Merged beneath the component's own Values in the composed
	// render, so the platform-engineer's template overrides reach each component.
	ComponentPlatformValues map[string]ComponentPlatformValues
	// ComponentTemplateImages holds each composed component's RESOLVED Kargo image
	// sources (keyed by component name), the per-component analog of TemplateImages.
	// The publish adapter discovers each component's images from its own effective
	// values (chart defaults ⊕ its Values overlay) and matches them against the
	// component's ComponentSpec.Images selection — so a component image is auto-
	// identified from values, not hand-typed. Each entry's KargoImage.Name is the
	// OWNING component (so the promotion targets that component's values file).
	ComponentTemplateImages map[string][]KargoImage
	// StackRawValues / StackEnvRawValues are the app's stack's shared Helm values
	// overlay (all envs / this env), layered above the platform values and below
	// the developer RawValues. Empty when the app isn't in a stack. Populated by
	// the publish adapter from the stack record.
	StackRawValues    map[string]any
	StackEnvRawValues map[string]any
	// SkipCanonicalBase, when true, omits the canonical suparship-common values
	// base (app/platform/components/suparship/routing) from the published
	// values.yaml — for BYO/passthrough templates (Spec.CanonicalValues()==false).
	// The platform context is still built so ((platform.*))/((vars.*)) tokens in the
	// overlay resolve; only the injected schema is dropped.
	SkipCanonicalBase bool
	// TemplateImages are the app's resolved image sources (one per service),
	// derived from the template's Images mapping by the publish adapter. They
	// drive the Kargo Warehouse subscriptions + Stage image updates, and the
	// set of tag keys the publisher preserves for CD-managed apps. App-level
	// (identical across envs); empty means the template declares no mapping and
	// the publisher falls back to a single legacy image.
	TemplateImages []KargoImage
	// SuspendKey is the dotted Helm values key that toggles suspend for this
	// app's chart (the template's declared key, or the "suspend" convention
	// default). When the env override sets Suspend=true, the publisher writes
	// `true` here so the chart scales the workload down; resume writes nothing
	// (the overlay is rebuilt each publish, so the flag simply disappears).
	SuspendKey string
}

// PublishPreview writes a preview app.yaml and values.yaml so ArgoCD
// deploys the preview via the previews ApplicationSet.
//
// Written files (env-first + app-scoped, mirroring the stable envs/ tree, so the
// base env is evident and apps sharing a preview name in one PR don't collide):
//   - gitops-output/previews/{baseEnv}/{project}/{previewName}/{app}/app.yaml
//   - gitops-output/previews/{baseEnv}/{project}/{previewName}/{app}/values.yaml
//   - gitops-output/_app-resources/previews/{baseEnv}/{project}/{previewName}/{app}/env-configmap.yaml
//   - gitops-output/_app-resources/previews/{baseEnv}/{project}/{previewName}/{app}/external-secret.yaml (when StoreName is set)
//
// PublishPreview is idempotent; it only creates a commit when content changes.
func (p *Publisher) PublishPreview(ctx context.Context, app *domain.App, preview PreviewPublishSpec) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		if err := p.publishOnePreview(ctx, repoDir, app, preview); err != nil {
			return err
		}
		commitMsg := fmt.Sprintf("feat(previews): create preview %s/%s\n\nCreated by suparship.", app.ProjectName, preview.PreviewName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// publishOnePreview dispatches a single preview to the composed or single-source
// renderer. A composed app (≥2 components) renders each preview-enabled component
// as its own chart source; a single-source app keeps the mature single-chart
// preview path.
func (p *Publisher) publishOnePreview(ctx context.Context, repoDir string, app *domain.App, preview PreviewPublishSpec) error {
	if app.Spec.IsComposed() {
		return p.publishComposedPreviewFiles(ctx, repoDir, app, preview)
	}
	return p.publishPreviewFiles(repoDir, app, preview)
}

// PreviewPublishBundle is one member of a batched preview publish: an app plus
// its resolved preview spec.
type PreviewPublishBundle struct {
	App     *domain.App
	Preview PreviewPublishSpec
}

// PublishPreviews writes many previews' files in ONE clone/commit/push — the
// batched form of PublishPreview per app. It collapses a stack preview fan-out's
// N×(clone/commit/push) into one git round-trip (the preview 504 fix, mirroring
// PublishApps for pin). Every preview targets the same repo, so N un-batched
// publishes would serialize on the repo lock anyway; only batching removes the
// per-member git cost.
func (p *Publisher) PublishPreviews(ctx context.Context, bundles []PreviewPublishBundle) error {
	if len(bundles) == 0 {
		return nil
	}
	return p.withClonedRepo(ctx, func(repoDir string) error {
		for _, b := range bundles {
			if err := p.publishOnePreview(ctx, repoDir, b.App, b.Preview); err != nil {
				return err
			}
		}
		commitMsg := fmt.Sprintf("feat(previews): batch publish (%d preview(s))\n\nCreated by suparship.", len(bundles))
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// publishPreviewFiles writes a preview's app.yaml, values.yaml and platform
// resources into repoDir (no git commit). Extracted from PublishPreview so the
// file-generation logic — notably image-tag resolution — is testable without a
// repo clone.
func (p *Publisher) publishPreviewFiles(repoDir string, app *domain.App, preview PreviewPublishSpec) error {
	// Previews clone their base env, including its env-scoped template pins —
	// a preview of an upgraded staging must run staging's chart version.
	app = domain.AppForEnvTemplateVersions(app, preview.BaseEnv)
	previewMeta := PreviewAppMetadata{
		AppName:       app.Name,
		PreviewName:   preview.PreviewName,
		Project:       app.ProjectName,
		BaseEnv:       preview.BaseEnv,
		Template:      app.Spec.Template.Name,
		ChartPath:     chartPathFor(app.Spec.Template.Name, app.Spec.Template.Version),
		ClusterServer: preview.ClusterServer,
		Namespace:     preview.Namespace,
	}
	metaBytes, err := yaml.Marshal(previewMeta)
	if err != nil {
		return fmt.Errorf("marshal preview app.yaml: %w", err)
	}
	metaPath := p.outputDir(repoDir, "previews", preview.BaseEnv, app.ProjectName, preview.PreviewName, app.Name, "app.yaml")
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
	//
	// A per-preview image tag is surfaced two ways, so any chart shape works:
	//   - ((platform.imageTag)): set on hv.Platform below, so an override like
	//     `image.tag: "((platform.imageTag))"` resolves to the PR build at publish
	//     (overlay tokens interpolate against hv.Platform). Chart-agnostic — the
	//     recommended way for BYO/passthrough charts whose image key varies.
	//   - canonical fold: for canonical templates the tag is also folded into the
	//     app's image_tag so the mapper bakes it into each component's image.tag.
	mapApp := app
	overlay := previewRawValuesOverlay(app, preview)
	if preview.ImageTag != "" && !preview.SkipCanonicalBase {
		clone := *app
		vals := make(map[string]any, len(app.Spec.Values)+1)
		for k, v := range app.Spec.Values {
			vals[k] = v
		}
		vals[imageTagValuesKey] = preview.ImageTag
		clone.Spec.Values = vals
		mapApp = &clone
	}
	hv := helmvalues.MapToHelmValuesForEnv(mapApp, preview.PreviewName, domain.AppEnvPreview, preview.BaseDomain, preview.Namespace, "", previewOrgName, p.cfg.RoutingProfiles, nil, nil)
	// Expose the per-PR tag as ((platform.imageTag)) for overlay/raw-values token
	// interpolation, independent of the chart's image-mapping shape.
	if preview.ImageTag != "" {
		hv.Platform.ImageTag = preview.ImageTag
	}
	// Shared-namespace previews put every preview of the project into one
	// namespace (the namespace pattern omits {name}), so the resolved namespace
	// doesn't carry the preview name. Suffix the platform-managed resource names
	// (env ConfigMap + ExternalSecret/target Secret) with the preview name so
	// previews of the same app don't collide; the ((platform.configMapName)) /
	// ((platform.secretName)) tokens follow suit so the chart's
	// envConfigMapName/envSecretName references resolve to the suffixed objects.
	// Workload resource names are the chart's responsibility (via the
	// ((platform.previewName)) token in fullnameOverride).
	resBase := app.Name
	if !strings.Contains(preview.Namespace, preview.PreviewName) {
		resBase = app.Name + "-" + preview.PreviewName
	}
	hv.Platform.PreviewName = preview.PreviewName
	hv.Platform.ConfigMapName = secrets.AppConfigMapName(resBase)
	hv.Platform.SecretName = secrets.AppSecretName(resBase)
	var hvBytes []byte
	if preview.SkipCanonicalBase {
		hvBytes, err = marshalPassthroughValues(hv.Platform, overlay, preview.EnvVars)
	} else {
		hvBytes, err = marshalValuesWithOverlay(hv, overlay, preview.EnvVars)
	}
	if err != nil {
		return fmt.Errorf("marshal preview values.yaml: %w", err)
	}
	valuesPath := p.outputDir(repoDir, "previews", preview.BaseEnv, app.ProjectName, preview.PreviewName, app.Name, "values.yaml")
	if err := p.writeFile(valuesPath, hvBytes); err != nil {
		return err
	}
	// Interpolate preview env-var values against the preview's platform context.
	previewEnvVars := preview.EnvVars
	if hasInterpToken(previewEnvVars) {
		previewEnvVars = platform.Context{Platform: hv.Platform, Vars: preview.EnvVars}.InterpolateMap(previewEnvVars)
	}

	// Platform-managed ConfigMap + ExternalSecret go to the platform-owned
	// _app-resources/previews/ tree (shipped by the preview platform
	// ApplicationSet), not the preview's chart directory.
	previewDir := p.outputDir(repoDir, "previews", preview.BaseEnv, app.ProjectName, preview.PreviewName, app.Name)
	resDir := p.outputDir(repoDir, "_app-resources", "previews", preview.BaseEnv, app.ProjectName, preview.PreviewName, app.Name)
	esCfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:             app.Name,
		SecretName:      secrets.AppSecretName(resBase),
		Namespace:       preview.Namespace,
		Env:             preview.BaseEnv,
		Project:         app.ProjectName,
		Stack:           app.Spec.Stack,
		Presence:        preview.ScopeKeys,
		IsPreview:       true,
		PreviewName:     preview.PreviewName,
		Backend:         p.effectiveBackend(),
		Branding:        p.cfg.Branding,
		RefreshInterval: p.externalSecretRefreshInterval(),
	})
	meta := PlatformAppMeta{
		Name:          preview.PreviewName,
		AppName:       app.Name,
		BaseEnv:       preview.BaseEnv,
		Project:       app.ProjectName,
		Namespace:     preview.Namespace,
		ClusterServer: preview.ClusterServer,
	}
	if err := p.writePlatformDir(resDir, secrets.AppConfigMapName(resBase), preview.Namespace, previewEnvVars, esCfg, meta); err != nil {
		return fmt.Errorf("writing preview platform resources: %w", err)
	}
	if err := p.pruneLegacyPlatformFiles(previewDir); err != nil {
		return err
	}
	return nil
}

// publishComposedPreviewFiles renders a preview for a COMPOSED app: it writes
// per-component preview values for only the components with preview enabled
// (ComponentSpec.EnabledInPreview — web/worker by default; stateful DBs and
// one-shot job/cron off unless explicitly on), one multi-source Application
// scoped to the ephemeral preview namespace (pinned image tag, NO Kargo), each
// enabled stateful component as its own prune-disabled Application, plus the
// app-wide preview ConfigMap/ExternalSecret (reused from the single-source
// preview platform tree). Discovery is the static previews-composed root app.
func (p *Publisher) publishComposedPreviewFiles(ctx context.Context, repoDir string, app *domain.App, preview PreviewPublishSpec) error {
	// Previews clone their base env, including its env-scoped template pins.
	app = domain.AppForEnvTemplateVersions(app, preview.BaseEnv)
	previewOrgName := p.cfg.OrgName
	if previewOrgName == "" {
		previewOrgName = "default"
	}
	ns := preview.Namespace

	// App-wide preview config/secret names — suffixed for shared-namespace previews
	// (namespace omits the preview name) so previews of the same app don't collide.
	resBase := app.Name
	if !strings.Contains(preview.Namespace, preview.PreviewName) {
		resBase = app.Name + "-" + preview.PreviewName
	}
	configMapName := secrets.AppConfigMapName(resBase)
	secretName := secrets.AppSecretName(resBase)

	// Prune this app's existing preview trees first so a component that was
	// disabled-in-preview (or removed) leaves no orphan values/manifest.
	compValuesRoot := p.outputDir(repoDir, "previews", preview.BaseEnv, app.ProjectName, preview.PreviewName, app.Name, "components")
	if err := os.RemoveAll(compValuesRoot); err != nil {
		return fmt.Errorf("prune composed preview values: %w", err)
	}
	manifestDir := p.outputDir(repoDir, composedAppsDir, composedPreviewsSubdir, preview.BaseEnv, app.ProjectName, preview.PreviewName, app.Name)
	if err := os.RemoveAll(manifestDir); err != nil {
		return fmt.Errorf("prune composed preview manifests: %w", err)
	}

	// Only the components opted into previews.
	var included []domain.ComponentSpec
	for _, c := range app.Spec.ComposedComponents() {
		if c.Template != nil && c.EnabledInPreview() {
			included = append(included, c)
		}
	}
	previewBand := app.Spec.EnvironmentDefaults[domain.PreviewOverrideKey]

	componentValues := make(map[string]string, len(included))
	var appPlatform helmvalues.PlatformValues
	for i, c := range included {
		key := p.resolveComponentKey(ctx, c.Template.Name, c.Name)
		canonical := p.resolveComponentCanonical(ctx, c.Template.Name)
		hv := helmvalues.MapComponentHelmValuesForEnv(app, c, key, preview.PreviewName, domain.AppEnvPreview, preview.BaseDomain, ns, "", previewOrgName,
			p.cfg.RoutingProfiles, nil, nil)
		hv.Platform.PreviewName = preview.PreviewName
		hv.Platform.ConfigMapName = configMapName
		hv.Platform.SecretName = secretName
		if preview.ImageTag != "" {
			hv.Platform.ImageTag = preview.ImageTag
		}
		if i == 0 {
			appPlatform = hv.Platform // app-wide identity for env-var interpolation
		}

		// Overlay, low→high: PE component-template base-env overlays (Default+Env; no
		// cluster for previews) ⊕ the component's own Values ⊕ the component
		// template's PREVIEW defaults ⊕ the app's per-component preview band. Mirrors
		// the single-source order (previewRawValuesOverlay): template preview defaults
		// sit above the developer base overlay and below the app's own preview band.
		pv := preview.ComponentPlatformValues[c.Name]
		overlay := deepMerge(deepCopyMap(pv.Default), deepCopyMap(pv.Env))
		overlay = deepMerge(overlay, deepCopyMap(c.Values))
		overlay = deepMerge(overlay, deepCopyMap(pv.Preview))
		if len(previewBand.ComponentValues[c.Name]) > 0 {
			overlay = deepMerge(overlay, deepCopyMap(previewBand.ComponentValues[c.Name]))
		}
		// Pin the per-PR image tag: a canonical component folds it into
		// components.<key>.image.tag; a passthrough component relies on the
		// ((platform.imageTag)) token in its own overlay.
		if preview.ImageTag != "" && canonical {
			o := deepCopyMap(overlay)
			if o == nil {
				o = map[string]any{}
			}
			setStringAtPath(o, "components."+key+".image.tag", preview.ImageTag)
			overlay = o
		}

		var hvBytes []byte
		var err error
		if canonical {
			hvBytes, err = marshalValuesWithOverlay(hv, overlay, preview.EnvVars)
		} else {
			hvBytes, err = marshalPassthroughValues(hv.Platform, overlay, preview.EnvVars)
		}
		if err != nil {
			return fmt.Errorf("marshal preview values for component %s: %w", c.Name, err)
		}
		valuesAbs := p.outputDir(repoDir, "previews", preview.BaseEnv, app.ProjectName, preview.PreviewName, app.Name, "components", c.Name, "values.yaml")
		if err := p.writeFile(valuesAbs, hvBytes); err != nil {
			return err
		}
		componentValues[c.Name] = p.relativeOutputPath("previews", preview.BaseEnv, app.ProjectName, preview.PreviewName, app.Name, "components", c.Name, "values.yaml")
	}

	// Rendered Application(s), scoped to the preview namespace — a clone carrying
	// only the included components so BuildComposedApplication references exactly
	// the values we wrote. No KargoStage: previews are pinned, never promoted.
	if len(included) > 0 {
		previewApp := *app
		previewApp.Spec.Components = included
		composedOpts := ComposedBuildOptions{
			RepoURL:         p.cfg.ArgoCDRepoURL,
			SubPath:         p.cfg.SubPath,
			AppName:         app.Name + "-" + preview.PreviewName,
			EnvName:         preview.PreviewName,
			ClusterServer:   preview.ClusterServer,
			Namespace:       ns,
			SyncAutomated:   p.cfg.SyncAutomated,
			ComponentValues: componentValues,
		}
		if manifest := BuildComposedApplication(&previewApp, composedOpts); len(manifest.Spec.Sources) > 1 {
			manifestBytes, err := yaml.Marshal(manifest)
			if err != nil {
				return fmt.Errorf("marshal composed preview Application: %w", err)
			}
			if err := p.writeFile(filepath.Join(manifestDir, "application.yaml"), manifestBytes); err != nil {
				return err
			}
		}
		for _, c := range previewApp.Spec.StatefulComponents() {
			compManifest := BuildComponentApplication(&previewApp, c, composedOpts)
			compBytes, err := yaml.Marshal(compManifest)
			if err != nil {
				return fmt.Errorf("marshal stateful preview component %s: %w", c.Name, err)
			}
			if err := p.writeFile(filepath.Join(manifestDir, c.Name+"-application.yaml"), compBytes); err != nil {
				return err
			}
		}
	}

	// App-wide preview platform resources (ConfigMap + ExternalSecret) — identical
	// to the single-source preview path; one set per app, shared by all components.
	previewEnvVars := preview.EnvVars
	if hasInterpToken(previewEnvVars) {
		previewEnvVars = platform.Context{Platform: appPlatform, Vars: preview.EnvVars}.InterpolateMap(previewEnvVars)
	}
	resDir := p.outputDir(repoDir, "_app-resources", "previews", preview.BaseEnv, app.ProjectName, preview.PreviewName, app.Name)
	esCfg := BuildAppExternalSecret(WorkloadExternalSecretParams{
		App:             app.Name,
		SecretName:      secretName,
		Namespace:       ns,
		Env:             preview.BaseEnv,
		Project:         app.ProjectName,
		Stack:           app.Spec.Stack,
		Presence:        preview.ScopeKeys,
		IsPreview:       true,
		PreviewName:     preview.PreviewName,
		Backend:         p.effectiveBackend(),
		Branding:        p.cfg.Branding,
		RefreshInterval: p.externalSecretRefreshInterval(),
	})
	meta := PlatformAppMeta{
		Name:          preview.PreviewName,
		AppName:       app.Name,
		BaseEnv:       preview.BaseEnv,
		Project:       app.ProjectName,
		Namespace:     ns,
		ClusterServer: preview.ClusterServer,
	}
	if err := p.writePlatformDir(resDir, configMapName, ns, previewEnvVars, esCfg, meta); err != nil {
		return fmt.Errorf("writing composed preview platform resources: %w", err)
	}

	// Static root app that discovers every composed preview (idempotent).
	rootApp := BuildComposedPreviewRootApp(p.cfg.ArgoCDRepoURL, AppSetOptions{
		SyncAutomated: p.cfg.SyncAutomated,
		SubPath:       p.cfg.SubPath,
	})
	rootBytes, err := yaml.Marshal(rootApp)
	if err != nil {
		return fmt.Errorf("marshal composed previews root app: %w", err)
	}
	if err := p.writeFile(p.outputDir(repoDir, "_infra", "previews-composed-appset.yaml"), rootBytes); err != nil {
		return err
	}
	// The root app's source path must exist from birth — see
	// ensureComposedPreviewsRoot.
	return p.ensureComposedPreviewsRoot(repoDir)
}

// ensureComposedPreviewsRoot keeps the previews-composed root Application's
// source directory present in git even when no preview exists. Git cannot
// track an empty directory, so removing the last preview's files (or
// publishing the root app before the first preview) leaves the Application
// pointing at a missing path — ArgoCD strands it in Unknown, it can never
// sync, and with prune-on-sync never running the last preview's workloads
// leak as zombies. A .gitkeep renders zero manifests (directory sources only
// read *.yaml/json), so an "empty" sync correctly prunes everything.
func (p *Publisher) ensureComposedPreviewsRoot(repoDir string) error {
	return p.writeFile(p.outputDir(repoDir, composedAppsDir, composedPreviewsSubdir, ".gitkeep"), nil)
}

// PreviewPublishSpec carries the parameters for publishing a preview environment.
type PreviewPublishSpec struct {
	// PreviewName is the sanitized preview identifier (e.g. "pr-42").
	PreviewName string
	// BaseEnv is the stable env the preview clones (default the first stable env
	// by promotion order, conventionally "staging"). The preview reuses this
	// env's vault, ClusterSecretStore, cluster and per-env config — preview band
	// items live inside the base env vault, so no per-preview vault is created.
	BaseEnv string
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
	// ImageTag, when non-empty, overrides the image tag in the preview's values:
	// it is exposed as ((platform.imageTag)) (for `image.tag: "((platform.imageTag))"`
	// style overrides) and, for canonical templates, folded into each component's
	// image.tag. Empty inherits the base env's image tag.
	ImageTag string
	// ScopeKeys reports which (scope, tier) items have keys, so PublishPreview
	// emits only the ExternalSecrets that resolve.
	ScopeKeys ScopePresence
	// SkipCanonicalBase mirrors AppPublishEnv.SkipCanonicalBase for previews of
	// BYO/passthrough templates.
	SkipCanonicalBase bool

	// The fields below are the BASE env's resolved value overlays, so the preview
	// inherits exactly what the base env deploys (the preview band layers on top).
	// They mirror the same-named AppPublishEnv fields; the publish adapter fills
	// them via setPlatformOverlays/setStackOverlays for the base env.
	//
	// Cluster is the base env's active cluster ref, selecting which
	// PlatformClusterValues block applies.
	Cluster string
	// PlatformDefaultValues / PlatformEnvValues are the PE-authored template/org
	// value overrides (all envs, then the base env).
	PlatformDefaultValues map[string]any
	PlatformEnvValues     map[string]any
	// PlatformClusterValues are the org-level per-cluster overlays keyed by
	// cluster ref; only the Cluster block is applied.
	PlatformClusterValues map[string]map[string]any
	// StackRawValues / StackEnvRawValues are the app's stack shared overlays.
	StackRawValues    map[string]any
	StackEnvRawValues map[string]any
	// TemplatePreviewValues is the template's default preview overlay (TemplateSpec
	// + TemplateOverride PreviewDefaultValues, merged), applied to every preview of
	// the template's apps below the app's own preview band.
	TemplatePreviewValues map[string]any
	// ComponentPlatformValues holds the PE-authored value overlays for EACH composed
	// component's own template (keyed by component name), the composed analog of
	// Platform{Default,Env}Values. Populated by the publish adapter only for
	// composed apps; merged beneath each component's Values in a composed preview.
	// nil for single-source previews. Cluster overlays don't apply to previews.
	ComponentPlatformValues map[string]ComponentPlatformValues
}

// DeletePreview removes one app's preview GitOps files and commits. It deletes
// BOTH the preview's chart tree (previews/{baseEnv}/{project}/{name}/{app}) —
// pruned by the previews ApplicationSet — AND its platform-resources tree
// (_app-resources/previews/{baseEnv}/{project}/{name}/{app}) — pruned by the
// previews-platform ApplicationSet. Removing only the former leaves the
// preview-platform Application dangling. Other apps sharing the preview name are
// untouched. No-op (without error) when neither exists.
func (p *Publisher) DeletePreview(ctx context.Context, projectName, previewName, appName, baseEnv string) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		removed, err := p.deletePreviewFiles(repoDir, projectName, previewName, appName, baseEnv)
		if err != nil {
			return err
		}
		if !removed {
			slog.Debug("gitops: preview directories not found, nothing to delete", "preview", previewName, "app", appName)
			return nil
		}
		commitMsg := fmt.Sprintf("feat(previews): delete preview %s/%s/%s/%s\n\nDeleted by suparship.", baseEnv, projectName, previewName, appName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// deletePreviewFiles removes one app's preview chart tree and its platform-resources
// tree from repoDir (no git commit), reporting whether anything was removed.
func (p *Publisher) deletePreviewFiles(repoDir, projectName, previewName, appName, baseEnv string) (bool, error) {
	u := &unpublishHelper{}
	if err := u.rm(p.outputDir(repoDir, "previews", baseEnv, projectName, previewName, appName)); err != nil {
		return false, err
	}
	if err := u.rm(p.outputDir(repoDir, "_app-resources", "previews", baseEnv, projectName, previewName, appName)); err != nil {
		return false, err
	}
	// Composed apps also render a preview Application manifest tree, pruned by the
	// previews-composed root app once removed.
	if err := u.rm(p.outputDir(repoDir, composedAppsDir, composedPreviewsSubdir, baseEnv, projectName, previewName, appName)); err != nil {
		return false, err
	}
	if u.removed {
		// Keep the root app's source path alive so the prune actually runs —
		// see ensureComposedPreviewsRoot.
		if err := p.ensureComposedPreviewsRoot(repoDir); err != nil {
			return u.removed, err
		}
	}
	return u.removed, nil
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
//   - previews/*/{project}/*/{app}/          — open preview chart trees (all PRs)
//   - _app-resources/previews/*/{project}/*/{app}/     — preview platform resources
//   - _composed-apps/_previews/*/{project}/*/{app}/    — composed preview manifests
//   - {env}/{project}/{app}/                 — legacy pre-envs/ layout
//
// No-op if nothing found.
func (p *Publisher) UnpublishApp(ctx context.Context, projectName, appName string) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		removed, err := p.unpublishAppFiles(repoDir, projectName, appName)
		if err != nil {
			return err
		}
		if !removed {
			slog.Debug("gitops: no app files found — nothing to delete",
				"project", projectName, "app", appName)
			return nil
		}
		commitMsg := fmt.Sprintf("feat(apps): delete app %s/%s\n\nDeleted by suparship.", projectName, appName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// unpublishAppFiles removes all of an app's GitOps files from an already-cloned repo
// (no git), reporting whether anything was removed. Covers the same layouts listed
// on UnpublishApp, including preview trees across all open preview names. Extracted
// for white-box testing without git.
func (p *Publisher) unpublishAppFiles(repoDir, projectName, appName string) (bool, error) {
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
				return u.removed, err
			}
			// Per-cluster fan-out values: envs/{env}/_clusters/{cluster}/{project}/{app}.
			clustersDir := filepath.Join(baseDir, e.Name(), "_clusters")
			if cents, cerr := os.ReadDir(clustersDir); cerr == nil {
				for _, c := range cents {
					if !c.IsDir() {
						continue
					}
					if err := u.rm(filepath.Join(clustersDir, c.Name(), projectName, appName)); err != nil {
						return u.removed, err
					}
				}
			}
		}
	}

	// Kargo Warehouse + Stage CRs for this app. Match on BOTH the stamped
	// suparship.io/project and suparship.io/app labels rather than the filename
	// prefix: the flat _infra/kargo/ dir holds every project's CRs, so two
	// projects can own an app with the same name, and a sibling app whose name
	// extends this one (e.g. "web-admin" vs "web") shares the filename prefix —
	// either would be wrongly pruned by a name match alone. The project's
	// Project/ProjectConfig CRs carry no app label, so they are never matched here.
	kargoDir := p.outputDir(repoDir, "_infra", "kargo")
	if entries, err := os.ReadDir(kargoDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			path := filepath.Join(kargoDir, e.Name())
			if kargoManifestLabel(path, labelApp) != appName || kargoManifestLabel(path, labelProject) != projectName {
				continue
			}
			if err := u.rm(path); err != nil {
				return u.removed, err
			}
		}
	}

	// Drop this app's promotion policies from the project's ProjectConfig
	// (other apps in the project keep theirs).
	projectNS := KargoNamespaceForProject(projectName)
	if existing, perr := p.readKargoPromotionPolicies(kargoDir, projectNS); perr != nil {
		return u.removed, perr
	} else if merged := MergeKargoPromotionPolicies(existing, appName, nil); len(merged) != len(existing) {
		if werr := p.writeKargoProjectConfig(kargoDir, projectName, merged); werr != nil {
			return u.removed, werr
		}
		u.removed = true
	}

	// Preview trees for this app across ALL open preview names. Critical for
	// consolidation: when per-component apps (e.g. lk-sh-web, lk-sh-express-caller)
	// are merged into a composed app (voiceai-lk-sh), unpublishing each old app must
	// drop its single-source preview chart, platform, and composed-manifest trees —
	// otherwise they linger under every open PR and keep rendering as phantom preview
	// Applications. Layout: {root}/{baseEnv}/{project}/{preview}/{app}.
	for _, root := range [][]string{
		{"previews"},
		{"_app-resources", "previews"},
		{composedAppsDir, composedPreviewsSubdir},
	} {
		pattern := filepath.Join(p.outputDir(repoDir, root...), "*", projectName, "*", appName)
		matches, gerr := filepath.Glob(pattern)
		if gerr != nil {
			return u.removed, fmt.Errorf("glob preview trees %s: %w", pattern, gerr)
		}
		for _, m := range matches {
			if err := u.rm(m); err != nil {
				return u.removed, err
			}
		}
	}
	if u.removed {
		// Keep the previews-composed root app's source path alive so its
		// prune runs — see ensureComposedPreviewsRoot.
		if err := p.ensureComposedPreviewsRoot(repoDir); err != nil {
			return u.removed, err
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
				return u.removed, err
			}
		}
	}

	return u.removed, nil
}

// RemoveAppEnv removes a SINGLE environment's GitOps manifests for an app —
// envs/{env}/{project}/{app}/ (and its _clusters/* fan-out values) plus
// _app-resources/{env}/{project}/{app}/ — then commits + pushes. ArgoCD's
// automated prune then removes that env's workload. This is the explicit,
// deliberate "remove from cluster" action for a direct-delivery app; it is NOT a
// side effect of toggling an env off (that merely stops publishing). Other envs
// and other apps are untouched. No-op when the env has no files.
func (p *Publisher) RemoveAppEnv(ctx context.Context, projectName, appName, envName string) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		removed, err := p.removeAppEnvFiles(repoDir, projectName, appName, envName)
		if err != nil {
			return err
		}
		if !removed {
			slog.Debug("gitops: no env files found — nothing to remove",
				"project", projectName, "app", appName, "env", envName)
			return nil
		}
		commitMsg := fmt.Sprintf("feat(apps): remove %s/%s from %s\n\nRemoved by suparship.", projectName, appName, envName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// removeAppEnvFiles deletes one env's app + platform-resource trees (and any
// per-cluster fan-out values) for an app, returning whether anything was removed.
// No git ops — the caller commits. Other envs and other apps are untouched.
func (p *Publisher) removeAppEnvFiles(repoDir, projectName, appName, envName string) (bool, error) {
	var u unpublishHelper
	for _, base := range []string{"envs", "_app-resources"} {
		if err := u.rm(p.outputDir(repoDir, base, envName, projectName, appName)); err != nil {
			return false, err
		}
		// Per-cluster fan-out values: {base}/{env}/_clusters/{cluster}/{project}/{app}.
		clustersDir := p.outputDir(repoDir, base, envName, "_clusters")
		if cents, cerr := os.ReadDir(clustersDir); cerr == nil {
			for _, c := range cents {
				if !c.IsDir() {
					continue
				}
				if err := u.rm(filepath.Join(clustersDir, c.Name(), projectName, appName)); err != nil {
					return false, err
				}
			}
		}
	}
	// Remove this env's Kargo Stage file so a pipeline app's decommissioned env
	// leaves no orphaned stage (publishKargoCRs only writes stages, never deletes
	// a departed one). The chain re-links via the Deploy-false filter in
	// publishKargoCRs on the app's republish. Direct apps have no stage file — the
	// rm is a no-op then.
	projectNS := KargoNamespaceForProject(projectName)
	stagePath := filepath.Join(p.outputDir(repoDir, "_infra", "kargo"), projectNS+"-"+appName+"-"+envName+"-stage.yaml")
	if err := u.rm(stagePath); err != nil {
		return false, err
	}
	return u.removed, nil
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

		// The project's Kargo CRs — Project, ProjectConfig, and every app's
		// Warehouse/Stage — all live in the kargo-{project} namespace and carry the
		// stamped suparship.io/project label, so a single label match prunes the
		// whole project's Kargo tenancy (including the Project + ProjectConfig).
		// Matching the label rather than the filename prefix avoids collisions with
		// a hyphen-extended sibling project (e.g. "web" vs "web-admin").
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

		commitMsg := fmt.Sprintf("feat(projects): delete project %s apps (phase 1/2)\n\nDeleted by suparship.", projectName)
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
		commitMsg := fmt.Sprintf("feat(projects): delete project %s infra (phase 2/2)\n\nDeleted by suparship.", projectName)
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
	// Serialize on the shared cache dir: one publish at a time reuses the
	// persistent working clone. Held for the whole publish (refresh → write →
	// commit → push) so a concurrent publish never sees a half-written tree.
	lock := repoLockFor(p.repoCacheDir)
	waitStart := time.Now()
	lock.Lock()
	defer lock.Unlock()
	lockWait := time.Since(waitStart)

	syncStart := time.Now()
	repoDir, err := p.syncRepo(ctx)
	if err != nil {
		return err
	}
	syncDur := time.Since(syncStart)

	writeStart := time.Now()
	err = fn(repoDir)
	// One line per publish so a 504 can be attributed: lock contention vs repo
	// sync (fetch/clone) vs the write+commit+push done inside fn (commitAndPush
	// logs its own git-level breakdown).
	slog.Info("gitops: publish timing",
		"repo", p.cfg.RepoURL,
		"lock_wait", lockWait.Round(time.Millisecond).String(),
		"repo_sync", syncDur.Round(time.Millisecond).String(),
		"write_commit_push", time.Since(writeStart).Round(time.Millisecond).String(),
	)
	return err
}

// syncRepo brings the persistent cache clone up to date with the remote and
// returns its path. It refreshes an existing cache with fetch+reset (fast,
// incremental — the win over re-cloning the whole repo per publish) and falls
// back to a fresh clone when the cache is absent or a refresh fails (corrupt
// clone, force-push, rotated creds, etc.).
func (p *Publisher) syncRepo(ctx context.Context) (string, error) {
	repoDir := p.repoCacheDir
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		if err := p.refreshRepo(ctx, repoDir); err == nil {
			return repoDir, nil
		} else {
			slog.Warn("gitops: cache refresh failed; re-cloning", "repo", p.cfg.RepoURL, "err", err)
		}
	}
	if err := p.freshClone(ctx, repoDir); err != nil {
		return "", err
	}
	return repoDir, nil
}

// refreshRepo updates an existing cache clone to the remote branch head
// (shallow fetch + hard reset + clean), discarding any leftover state from a
// prior publish. Re-points origin first so rotated credentials are picked up.
func (p *Publisher) refreshRepo(ctx context.Context, repoDir string) error {
	cloneURL := p.embedCredentials(p.cfg.RepoURL)
	if err := p.git(ctx, repoDir, "remote", "set-url", "origin", cloneURL); err != nil {
		return err
	}
	slog.Debug("gitops: refreshing cache", "repo", p.cfg.RepoURL, "branch", p.cfg.Branch)
	fetchStart := time.Now()
	if err := p.git(ctx, repoDir, "fetch", "--depth=1", "origin", p.cfg.Branch); err != nil {
		return err
	}
	if err := p.git(ctx, repoDir, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return err
	}
	if err := p.git(ctx, repoDir, "clean", "-fd"); err != nil {
		return err
	}
	slog.Info("gitops: cache refresh (fetch+reset)", "repo", p.cfg.RepoURL, "dur", time.Since(fetchStart).Round(time.Millisecond).String())
	return p.configureRepo(ctx, repoDir)
}

// freshClone wipes any stale cache dir and does a shallow clone into repoDir.
func (p *Publisher) freshClone(ctx context.Context, repoDir string) error {
	if err := os.RemoveAll(repoDir); err != nil {
		return fmt.Errorf("reset gitops cache: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(repoDir), 0o755); err != nil {
		return fmt.Errorf("create gitops cache dir: %w", err)
	}
	cloneURL := p.embedCredentials(p.cfg.RepoURL)
	slog.Debug("gitops: cloning repo (cache miss)", "repo", p.cfg.RepoURL, "branch", p.cfg.Branch)
	cloneStart := time.Now()
	if err := p.git(ctx, filepath.Dir(repoDir), "clone", "--depth=1", "--branch="+p.cfg.Branch, cloneURL, repoDir); err != nil {
		return fmt.Errorf("clone gitops repo: %w", err)
	}
	slog.Info("gitops: fresh clone (cache miss)", "repo", p.cfg.RepoURL, "dur", time.Since(cloneStart).Round(time.Millisecond).String())
	return p.configureRepo(ctx, repoDir)
}

// configureRepo sets the commit identity and seeds the operator README. Cheap
// and idempotent, so it runs after both refresh and fresh clone.
func (p *Publisher) configureRepo(ctx context.Context, repoDir string) error {
	authorName := p.cfg.CommitAuthorName
	if authorName == "" {
		authorName = DefaultCommitAuthorName
	}
	authorEmail := p.cfg.CommitAuthorEmail
	if authorEmail == "" {
		authorEmail = DefaultCommitAuthorEmail
	}
	if err := p.git(ctx, repoDir, "config", "user.email", authorEmail); err != nil {
		return err
	}
	if err := p.git(ctx, repoDir, "config", "user.name", authorName); err != nil {
		return err
	}
	if err := p.ensureRepoREADME(repoDir); err != nil {
		// README is operator-facing documentation, not infrastructure —
		// log and continue rather than aborting a publish if the write
		// fails for some odd reason.
		slog.Warn("gitops: README seeding failed; continuing publish", "err", err)
	}
	return nil
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
// self-documenting (`envs/`, `previews/`, `_infra/`, `charts/`). The dedicated
// PublishPreview flow writes to "previews/{baseEnv}/{project}/{previewName}/{app}/"
// (env-first, mirroring envs/) so the locations differ on purpose.
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

// envAppRelPath is envAppDir's repo-root-relative twin: the slash-joined path
// (under SubPath) used inside YAML manifests — e.g. AppMetadata.ValuesPath that
// the ApplicationSet reads via $appvalues/{{valuesPath}}. Mirrors the
// inline/external routing of envAppDir/appEnvDir(External).
func (p *Publisher) envAppRelPath(mode AppMetadataChartType, env AppPublishEnv, parts ...string) string {
	prefix := "envs"
	if mode == AppMetadataChartTypeExternal && env.EnvType != domain.AppEnvPreview {
		prefix = "envs-external"
	}
	all := append([]string{prefix, env.EnvName}, parts...)
	return p.relativeOutputPath(all...)
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
├── previews/{baseEnv}/{project}/{previewName}/{app}/  # per-PR preview environments (env-first)
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
	addStart := time.Now()
	if err := p.git(ctx, repoDir, "add", "."); err != nil {
		return err
	}
	addDur := time.Since(addStart)

	statusStart := time.Now()
	empty, err := p.stagedIsEmpty(ctx, repoDir)
	if err != nil {
		return err
	}
	statusDur := time.Since(statusStart)
	if empty {
		slog.Debug("gitops: nothing to commit — already up to date")
		return nil
	}

	slog.Debug("gitops: committing", "msg", msg)
	commitStart := time.Now()
	if err := p.git(ctx, repoDir, "commit", "-m", msg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	commitDur := time.Since(commitStart)

	slog.Debug("gitops: pushing to origin", "branch", p.cfg.Branch)
	pushStart := time.Now()
	if err := p.git(ctx, repoDir, "push", "origin", p.cfg.Branch); err != nil {
		return fmt.Errorf("push to gitops repo: %w", err)
	}
	// Breakdown so a slow publish can be pinned to add/status (working-tree
	// scan size) vs push (network) rather than lumped into write_commit_push.
	slog.Info("gitops: commit+push timing",
		"repo", p.cfg.RepoURL,
		"git_add", addDur.Round(time.Millisecond).String(),
		"git_status", statusDur.Round(time.Millisecond).String(),
		"git_commit", commitDur.Round(time.Millisecond).String(),
		"git_push", time.Since(pushStart).Round(time.Millisecond).String(),
	)
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
