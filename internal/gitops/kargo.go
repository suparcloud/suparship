package gitops

import (
	"fmt"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/domain"
)

// Kargo API constants.
const (
	kargoAPIVersion    = "kargo.akuity.io/v1alpha1"
	kargoKindWarehouse = "Warehouse"
	kargoKindStage     = "Stage"
	kargoKindPromotion = "Promotion"

	// labelKargoProject marks resources as belonging to a Kargo project
	// (namespace). Kargo uses namespace-per-project isolation.
	labelKargoProject = "kargo.akuity.io/project"
)

// ── Warehouse ─────────────────────────────────────────────────────────────────

// KargoWarehouse is a minimal, serialisable representation of a Kargo Warehouse
// CR. A Warehouse watches an image repository and creates Freight objects when
// new images are published.
//
// Kargo docs: https://docs.kargo.io/concepts#warehouses
type KargoWarehouse struct {
	APIVersion string        `yaml:"apiVersion"`
	Kind       string        `yaml:"kind"`
	Metadata   ObjectMeta    `yaml:"metadata"`
	Spec       WarehouseSpec `yaml:"spec"`
}

// WarehouseSpec configures the artifact subscriptions for a Kargo Warehouse.
type WarehouseSpec struct {
	// FreightCreationPolicy controls how Freight objects are created:
	//   "Automatic" — one Freight per new image tag (default).
	FreightCreationPolicy string                  `yaml:"freightCreationPolicy,omitempty"`
	Subscriptions         []WarehouseSubscription `yaml:"subscriptions"`
}

// WarehouseSubscription is one artifact source that the Warehouse watches.
// For MVP only image subscriptions are used; Git subscriptions are future work.
type WarehouseSubscription struct {
	Image *ImageSubscription `yaml:"image,omitempty"`
}

// ImageSubscription configures image polling for a Warehouse.
type ImageSubscription struct {
	// RepoURL is the container image repository to watch (without tag).
	RepoURL string `yaml:"repoURL"`
	// AllowTags is a regular expression that limits which tags Kargo considers.
	// "^\d+\.\d+\.\d+$" limits to SemVer-style tags (e.g. 1.2.3).
	// Maps to the Kargo v1alpha1 "allowTags" field (renamed from "tagPattern" in older versions).
	AllowTags string `yaml:"allowTags,omitempty"`
	// SemverConstraint is an optional semver range (e.g. ">=1.0.0").
	// Use instead of AllowTags when the image follows semantic versioning.
	SemverConstraint string `yaml:"semverConstraint,omitempty"`
	// InsecureSkipTLSVerify skips TLS certificate verification when
	// connecting to the registry. Required for HTTP-only registries
	// like the local kind-registry in dev mode.
	InsecureSkipTLSVerify bool `yaml:"insecureSkipTLSVerify,omitempty"`
}

// ── Stage ─────────────────────────────────────────────────────────────────────

// KargoStage is a minimal, serialisable representation of a Kargo Stage CR.
// A Stage represents one deployment tier (e.g. staging, prod) in a promotion
// pipeline. suparShip generates one Stage per stable AppEnvironment per app.
//
// Kargo docs: https://docs.kargo.io/concepts#stages
type KargoStage struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   ObjectMeta `yaml:"metadata"`
	Spec       StageSpec  `yaml:"spec"`
}

// StageSpec describes the freight sources and promotion mechanisms for a Stage.
//
// Note: Kargo v0.9.0's admission webhook validates against the deprecated
// spec.promotionMechanisms field. The newer spec.promotionTemplate.spec.steps
// API is accepted by the CRD but the webhook blocks Promotion creation when
// only steps are present. We use promotionMechanisms for compatibility.
type StageSpec struct {
	RequestedFreight    []FreightRequest     `yaml:"requestedFreight"`
	PromotionMechanisms *PromotionMechanisms `yaml:"promotionMechanisms,omitempty"`
}

// PromotionMechanisms defines the actions Kargo takes when promoting Freight
// into a Stage. Deprecated in Kargo v0.8 in favour of promotionTemplate.spec.steps
// but required by the v0.9.0 Promotion admission webhook.
type PromotionMechanisms struct {
	// GitRepoUpdates writes updated image tags or chart versions into a Git
	// repo so the new state is committed before ArgoCD syncs.
	GitRepoUpdates []GitRepoUpdate `yaml:"gitRepoUpdates,omitempty"`
	// ArgoCDAppUpdates is the list of ArgoCD Applications Kargo should sync
	// as part of the promotion.
	ArgoCDAppUpdates []ArgoCDAppUpdate `yaml:"argoCDAppUpdates,omitempty"`
}

// GitRepoUpdate describes one Git repository to update during promotion.
type GitRepoUpdate struct {
	RepoURL               string          `yaml:"repoURL"`
	ReadBranch            string          `yaml:"readBranch,omitempty"`
	WriteBranch           string          `yaml:"writeBranch,omitempty"`
	InsecureSkipTLSVerify bool            `yaml:"insecureSkipTLSVerify,omitempty"`
	Helm                  *HelmPromUpdate `yaml:"helm,omitempty"`
}

// HelmPromUpdate describes how Helm values files should be updated.
type HelmPromUpdate struct {
	Images []HelmImageUpdate `yaml:"images,omitempty"`
}

// HelmImageUpdate maps a Freight image to a key in a Helm values file.
type HelmImageUpdate struct {
	Image          string `yaml:"image"`
	ValuesFilePath string `yaml:"valuesFilePath"`
	Key            string `yaml:"key"`
	Value          string `yaml:"value"`
}

// ArgoCDAppUpdate describes one ArgoCD Application to update during promotion.
type ArgoCDAppUpdate struct {
	// AppName is the name of the ArgoCD Application CR.
	AppName string `yaml:"appName"`
	// AppNamespace is the namespace where the Application lives (default: argocd).
	AppNamespace string `yaml:"appNamespace,omitempty"`
}

// FreightRequest declares which Warehouse the Stage sources from, and whether
// freight arrives directly or from an upstream Stage.
type FreightRequest struct {
	Origin  FreightOrigin  `yaml:"origin"`
	Sources FreightSources `yaml:"sources"`
}

// FreightOrigin identifies the Warehouse that produces the freight.
type FreightOrigin struct {
	// Kind is always "Warehouse" for MVP.
	Kind string `yaml:"kind"`
	// Name is the Warehouse name, which matches the app name by convention.
	Name string `yaml:"name"`
}

// FreightSources describes where freight is obtained for this Stage.
// Either Direct (from the Warehouse) or from upstream Stages.
type FreightSources struct {
	// Direct means the Stage polls the Warehouse directly.
	// Used for the first stage in the pipeline (e.g. staging).
	Direct bool `yaml:"direct,omitempty"`
	// Stages lists upstream Stage names. Freight must pass through these
	// stages before reaching this one. Used for prod (upstream: staging).
	Stages []string `yaml:"stages,omitempty"`
}

// ── Build options ─────────────────────────────────────────────────────────────

// KargoBuildOptions carries the Kargo-specific options used by the builders.
type KargoBuildOptions struct {
	// KargoNamespace is the Kubernetes namespace for Kargo CRs (= Kargo project).
	// Defaults to the suparship project name.
	KargoNamespace string

	// ImageRepoURL is the container image repository the Warehouse monitors.
	// When empty a placeholder is used; operators should override this to their
	// actual registry path.
	ImageRepoURL string

	// ImageTagPattern is a regex that filters which tags Kargo promotes.
	// Maps to the Kargo v1alpha1 "allowTags" field.
	// Defaults to semver tags (^\d+\.\d+\.\d+$) when empty.
	ImageTagPattern string

	// InsecureSkipTLSVerify disables TLS verification for image registry
	// access. Required for HTTP-only registries (e.g. local dev registries).
	InsecureSkipTLSVerify bool

	// GitOpsRepoURL is the Git URL of the gitops repo that Kargo should
	// update during promotion (to write new image tags into values.yaml).
	// When empty, gitRepoUpdates are omitted from the Stage.
	GitOpsRepoURL string

	// GitOpsRepoInsecure disables TLS verification for the gitops repo.
	GitOpsRepoInsecure bool

	// Branding stamps the platform identity onto every Kargo CR. Zero
	// value applies "suparship" / "suparship.io" defaults so existing
	// callers don't change behaviour.
	Branding branding.Config
}

// ── Builders ─────────────────────────────────────────────────────────────────

// BuildKargoWarehouse constructs a Kargo Warehouse CR for an app.
//
// One Warehouse is shared across all stable environments of the app. It is
// placed in opts.KargoNamespace (default: app.ProjectName) and named after the
// app so Stages can reference it by name.
//
// BuildKargoWarehouse is a pure function — identical inputs produce identical
// output.
func BuildKargoWarehouse(app *domain.App, opts KargoBuildOptions) *KargoWarehouse {
	opts = applyKargoDefaults(opts, app)

	repoURL := opts.ImageRepoURL
	tagPattern := opts.ImageTagPattern

	return &KargoWarehouse{
		APIVersion: kargoAPIVersion,
		Kind:       kargoKindWarehouse,
		Metadata: ObjectMeta{
			Name:      app.Name,
			Namespace: opts.KargoNamespace,
			Labels: branding.MergeLabels(
				opts.Branding.ManagedByLabels(),
				map[string]string{
					labelApp:          app.Name,
					labelProject:      app.ProjectName,
					labelKargoProject: opts.KargoNamespace,
				},
			),
		},
		Spec: WarehouseSpec{
			FreightCreationPolicy: "Automatic",
			Subscriptions: []WarehouseSubscription{
				{
					Image: &ImageSubscription{
						RepoURL:               repoURL,
						AllowTags:              tagPattern,
						InsecureSkipTLSVerify:  opts.InsecureSkipTLSVerify,
					},
				},
			},
		},
	}
}

// BuildKargoStage constructs a Kargo Stage CR for one app environment.
//
// upstreamStages lists the Stage names that must be healthy before freight can
// reach this Stage. Pass nil or an empty slice for the first stage in the
// pipeline (e.g. staging), which receives freight directly from the Warehouse.
//
// BuildKargoStage is a pure function — identical inputs produce identical
// output.
func BuildKargoStage(app *domain.App, env domain.AppEnvironment, upstreamStages []string, opts KargoBuildOptions) *KargoStage {
	opts = applyKargoDefaults(opts, app)

	stageName := KargoStageName(app.Name, env.EnvName)

	sources := FreightSources{}
	if len(upstreamStages) == 0 {
		sources.Direct = true
	} else {
		// Upstream references also use {app}-{env} naming.
		qualifiedUpstreams := make([]string, len(upstreamStages))
		for i, us := range upstreamStages {
			qualifiedUpstreams[i] = KargoStageName(app.Name, us)
		}
		sources.Stages = qualifiedUpstreams
	}

	// PromotionMechanisms triggers an ArgoCD sync for the app+env Application.
	// The Application name follows the suparship convention: "{app}-{env}".
	//
	// Note: We use the deprecated promotionMechanisms API because the Kargo v0.9.0
	// Promotion admission webhook validates against this field. The newer
	// promotionTemplate.spec.steps API is accepted by the CRD but the webhook
	// rejects Promotions for Stages that only declare steps.
	argoAppName := ApplicationName(app.Name, env.EnvName)

	// The values.yaml path within the gitops repo for this app+env.
	valuesFilePath := fmt.Sprintf("gitops-output/%s/%s/%s/values.yaml", env.EnvName, app.ProjectName, app.Name)

	pm := &PromotionMechanisms{
		ArgoCDAppUpdates: []ArgoCDAppUpdate{
			{
				AppName:      argoAppName,
				AppNamespace: defaultArgoCDNS,
			},
		},
	}

	// When the gitops repo URL is configured, add gitRepoUpdates so Kargo
	// commits new image tags to values.yaml before triggering the ArgoCD sync.
	if opts.GitOpsRepoURL != "" {
		pm.GitRepoUpdates = []GitRepoUpdate{
			{
				RepoURL:               opts.GitOpsRepoURL,
				ReadBranch:            "main",
				WriteBranch:           "main",
				InsecureSkipTLSVerify: opts.GitOpsRepoInsecure,
				Helm: &HelmPromUpdate{
					Images: []HelmImageUpdate{
						{
							Image:          opts.ImageRepoURL,
							ValuesFilePath: valuesFilePath,
							Key:            "components.web.image.tag",
							Value:          "Tag",
						},
					},
				},
			},
		}
	}

	return &KargoStage{
		APIVersion: kargoAPIVersion,
		Kind:       kargoKindStage,
		Metadata: ObjectMeta{
			Name:      stageName,
			Namespace: opts.KargoNamespace,
			Labels: branding.MergeLabels(
				opts.Branding.ManagedByLabels(),
				map[string]string{
					labelApp:          app.Name,
					labelProject:      app.ProjectName,
					labelEnv:          env.EnvName,
					labelEnvType:      string(env.EnvType),
					labelKargoProject: opts.KargoNamespace,
				},
			),
		},
		Spec: StageSpec{
			RequestedFreight: []FreightRequest{
				{
					Origin:  FreightOrigin{Kind: "Warehouse", Name: app.Name},
					Sources: sources,
				},
			},
			PromotionMechanisms: pm,
		},
	}
}

// ── Kargo Project CR ───────────────────────────────────────────────────────────

// KargoProject is a minimal, serialisable representation of a Kargo Project CR.
//
// In Kargo v0.9+, the Project CR replaces the Namespace-label approach used in
// older versions. Creating a Project CR causes Kargo to create (or adopt) the
// namespace with the correct labels and RBAC, and allows Kargo to manage
// Warehouse / Stage / Promotion CRs within it.
//
// The Project also holds PromotionPolicies that control which Stages allow
// manual and auto-promotion. In Kargo v0.9, without an explicit policy the
// admission webhook blocks all Promotion CR creation.
//
// One Project is created per suparship project. By suparship convention the
// Project name equals the project name (which also becomes the namespace name).
//
// Kargo docs: https://docs.kargo.io/concepts#projects
type KargoProject struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Metadata   ObjectMeta       `yaml:"metadata"`
	Spec       KargoProjectSpec `yaml:"spec,omitempty"`
}

// KargoProjectSpec holds the optional configuration for a Kargo Project.
type KargoProjectSpec struct {
	// PromotionPolicies governs which Stages allow promotion and whether
	// auto-promotion is enabled.
	PromotionPolicies []KargoPromotionPolicy `yaml:"promotionPolicies,omitempty"`
}

// KargoPromotionPolicy defines the promotion rules for one Stage within a Project.
type KargoPromotionPolicy struct {
	// Stage is the name of the Kargo Stage (= environment name).
	Stage string `yaml:"stage"`
	// AutoPromotionEnabled enables automatic promotion of new Freight into
	// this Stage. Set to true for the first stage in the pipeline (staging).
	AutoPromotionEnabled bool `yaml:"autoPromotionEnabled,omitempty"`
}

// BuildKargoProject returns a Kargo Project CR for the given project name and
// environments. It generates PromotionPolicies for all non-preview environments,
// enabling auto-promotion on the first (staging) environment.
//
// brand stamps the platform identity. Zero value applies "suparship" defaults.
// Compatible with Kargo v0.9+. BuildKargoProject is a pure function.
func BuildKargoProject(projectName string, envs []KargoProjectEnv, brand branding.Config) KargoProject {
	var policies []KargoPromotionPolicy
	for _, env := range envs {
		policies = append(policies, KargoPromotionPolicy{
			Stage:                KargoStageName(env.AppName, env.EnvName),
			AutoPromotionEnabled: env.IsFirstStage,
		})
	}

	return KargoProject{
		APIVersion: kargoAPIVersion,
		Kind:       "Project",
		Metadata: ObjectMeta{
			Name: projectName,
			Labels: branding.MergeLabels(
				brand.ManagedByLabels(),
				map[string]string{labelProject: projectName},
			),
		},
		Spec: KargoProjectSpec{
			PromotionPolicies: policies,
		},
	}
}

// KargoProjectEnv describes one environment for Kargo Project generation.
type KargoProjectEnv struct {
	// AppName is the application name. Used together with EnvName to form
	// the Stage name ("{app}-{env}") in PromotionPolicies.
	AppName string
	// EnvName is the environment name (e.g. "staging", "prod").
	EnvName string
	// IsFirstStage marks this as the first stage in the pipeline (receives
	// Freight directly from the Warehouse). Auto-promotion is enabled for it.
	IsFirstStage bool
}

// KubernetesNamespace is retained for backward compatibility with older Kargo
// deployments that use the Namespace-label approach (Kargo < v0.9).
//
// Deprecated: Use BuildKargoProject for Kargo v0.9+.
type KubernetesNamespace struct {
	APIVersion string                  `yaml:"apiVersion"`
	Kind       string                  `yaml:"kind"`
	Metadata   KubernetesNamespaceMeta `yaml:"metadata"`
}

// KubernetesNamespaceMeta holds the name and labels for the namespace.
type KubernetesNamespaceMeta struct {
	Name   string            `yaml:"name"`
	Labels map[string]string `yaml:"labels"`
}

// BuildKargoProjectNamespace returns a Namespace manifest with the
// kargo.akuity.io/project label.
//
// Deprecated: Use BuildKargoProject for Kargo v0.9+. Retained for callers
// that target older Kargo deployments.
func BuildKargoProjectNamespace(projectName string) KubernetesNamespace {
	return KubernetesNamespace{
		APIVersion: "v1",
		Kind:       "Namespace",
		Metadata: KubernetesNamespaceMeta{
			Name: projectName,
			Labels: map[string]string{
				labelKargoProject: "true",
			},
		},
	}
}

// KargoStageName returns the Kargo Stage name for an app environment.
// Uses "{app}-{env}" to avoid collisions when multiple apps share a project.
func KargoStageName(appName, envName string) string {
	return appName + "-" + envName
}

// KargoNamespaceForProject returns the Kargo namespace for a suparship project.
// By convention Kargo namespaces match the suparship project name.
func KargoNamespaceForProject(projectName string) string {
	return projectName
}

// DefaultImageRepoURL derives a placeholder container image repository URL
// for an app when no explicit override is provided in KargoBuildOptions.
// The returned URL is a template placeholder; operators must update it to
// their actual registry before Kargo can detect new images.
func DefaultImageRepoURL(projectName, appName string) string {
	return fmt.Sprintf("ghcr.io/%s/%s", projectName, appName)
}

// applyKargoDefaults fills zero-valued KargoBuildOptions with sensible defaults.
func applyKargoDefaults(opts KargoBuildOptions, app *domain.App) KargoBuildOptions {
	if opts.KargoNamespace == "" {
		opts.KargoNamespace = app.ProjectName
	}
	if opts.ImageRepoURL == "" {
		opts.ImageRepoURL = DefaultImageRepoURL(app.ProjectName, app.Name)
	}
	if opts.ImageTagPattern == "" {
		// Match SemVer tags: 1.0.0, 2.3.14, etc.
		opts.ImageTagPattern = `^\d+\.\d+\.\d+$`
	}
	return opts
}
