package gitops

import (
	"fmt"
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/domain"
)

// Kargo API constants.
const (
	kargoAPIVersion        = "kargo.akuity.io/v1alpha1"
	kargoKindWarehouse     = "Warehouse"
	kargoKindStage         = "Stage"
	kargoKindPromotion     = "Promotion"
	kargoKindProjectConfig = "ProjectConfig"
	kargoKindProject       = "Project"

	// labelKargoProject marks resources as belonging to a Kargo project
	// (namespace). Kargo uses namespace-per-project isolation.
	labelKargoProject = "kargo.akuity.io/project"

	// kargoNamespacePrefix prefixes each suparship project's Kargo Project
	// namespace. Kargo's tenancy model is one Project = one namespace, so every
	// suparship project gets its own namespace on the tooling cluster. The
	// "kargo-" prefix keeps that namespace distinct from — and uncollidable with,
	// in single-cluster setups — the project's workload/environment namespaces.
	kargoNamespacePrefix = "kargo-"
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
	// ImageSelectionStrategy is how Kargo discovers/selects the tag to promote:
	// "NewestBuild" (most-recently-pushed, used for "latest from main"),
	// "SemVer" (default), "Digest", or "Lexical". Empty = Kargo's default (SemVer).
	ImageSelectionStrategy string `yaml:"imageSelectionStrategy,omitempty"`
	// InsecureSkipTLSVerify skips TLS certificate verification when
	// connecting to the registry. Required for HTTP-only registries
	// like the local kind-registry in dev mode.
	InsecureSkipTLSVerify bool `yaml:"insecureSkipTLSVerify,omitempty"`
}

// ── Stage ─────────────────────────────────────────────────────────────────────

// KargoStage is a minimal, serialisable representation of a Kargo Stage CR.
// A Stage represents one deployment tier (e.g. staging, prod) in a promotion
// pipeline. suparship generates one Stage per stable AppEnvironment per app.
//
// Kargo docs: https://docs.kargo.io/concepts#stages
type KargoStage struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   ObjectMeta `yaml:"metadata"`
	Spec       StageSpec  `yaml:"spec"`
}

// StageSpec describes the freight sources and the promotion template for a
// Stage. Kargo v1.x runs promotionTemplate.spec.steps to promote Freight into
// the Stage (the v0.9 spec.promotionMechanisms field was removed and is silently
// stripped by the v1.x admission webhook).
type StageSpec struct {
	RequestedFreight  []FreightRequest   `yaml:"requestedFreight"`
	PromotionTemplate *PromotionTemplate `yaml:"promotionTemplate,omitempty"`
}

// PromotionTemplate wraps the ordered steps Kargo runs when promoting Freight
// into a Stage (Kargo v1.x).
type PromotionTemplate struct {
	Spec PromotionTemplateSpec `yaml:"spec"`
}

// PromotionTemplateSpec holds the ordered promotion steps.
type PromotionTemplateSpec struct {
	Steps []PromotionStep `yaml:"steps"`
}

// PromotionStep is one step in a Kargo v1.x promotion: a built-in runner
// (`uses`) with a free-form `config`. `as` optionally names the step so later
// steps can reference its outputs (e.g. the commit produced by git-push).
type PromotionStep struct {
	Uses   string         `yaml:"uses"`
	As     string         `yaml:"as,omitempty"`
	Config map[string]any `yaml:"config,omitempty"`
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

// KargoImage is one resolved image source for an app: the repository the
// Warehouse watches and the values key the Stage rewrites on promotion. It is
// the resolved, publish-time form of an explicit image binding
// (app.Spec.Images / a component's Images) or a template-declared image slot.
type KargoImage struct {
	// Name is the logical image slot (matches tpl.TemplateImage.Name), used to
	// overlay per-app repository bindings onto the template's slots. May be
	// empty for a discovery-selected binding. Not emitted into any CR —
	// internal resolution only.
	Name string
	// Repository is the container image repository to watch (no tag).
	Repository string
	// TagKey is the dotted Helm values key holding this image's tag.
	TagKey string
	// TagPattern limits which tags Kargo considers (allowTags). Empty = any.
	TagPattern string
	// SelectionStrategy maps to the subscription's imageSelectionStrategy.
	SelectionStrategy string
}

// KargoBuildOptions carries the Kargo-specific options used by the builders.
type KargoBuildOptions struct {
	// KargoNamespace is the Kubernetes namespace for Kargo CRs (= Kargo project).
	// Defaults to the suparship project name.
	KargoNamespace string

	// Images are the resolved image sources for the app — one per service. The
	// Warehouse gets one subscription per entry and the Stage one helm image
	// update per entry. When empty, applyKargoDefaults seeds a single
	// placeholder image so the CRs are still well-formed.
	Images []KargoImage

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
	// SubPath mirrors PublisherConfig.SubPath so the gitRepoUpdates.helm
	// .images[].valuesFilePath in the Stage CR points at the same
	// values.yaml the publisher actually wrote. Empty (default) = repo
	// root; otherwise prefixes the path with the configured sub-dir.
	SubPath string

	// ArgoAppNamePattern is the org ResourceNaming pattern for ArgoCD Application
	// names, used to render the argocd-update targets in the promotion template.
	// Empty → secrets.DefaultArgoAppName.
	ArgoAppNamePattern string
	// Clusters are the env's destination clusters; the argocd-update step refreshes
	// one Application per cluster (per-cluster naming). Empty → a single in-cluster
	// fallback, matching the AppSet builder.
	Clusters []ClusterTarget
	// Composed selects the composed promotion template: instead of one yaml-update
	// on the env's single values.yaml, it emits one yaml-update per component,
	// targeting envs/<env>/<proj>/<app>/components/<name>/values.yaml (grouped by
	// each KargoImage's Name = owning component). The Warehouse still gets one
	// subscription per image.
	Composed bool
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

	// One subscription per UNIQUE image repo. A composed app whose components
	// share an image (e.g. two components both running biglysales-voiceai-livekit)
	// yields several KargoImage entries with the same Repository; emitting a
	// subscription each makes Kargo's freight admission webhook reject every
	// Freight with "multiple artifacts found for subscription <repo>". Dedupe by
	// repoURL — imageFrom(<repo>) in the promotion keys on the repo alone, so the
	// promoted tag still reaches every component's tag key from the one artifact.
	subs := make([]WarehouseSubscription, 0, len(opts.Images))
	seen := make(map[string]bool, len(opts.Images))
	for _, img := range opts.Images {
		if seen[img.Repository] {
			continue
		}
		seen[img.Repository] = true
		subs = append(subs, WarehouseSubscription{
			Image: &ImageSubscription{
				RepoURL:                img.Repository,
				AllowTags:              img.TagPattern,
				ImageSelectionStrategy: img.SelectionStrategy,
				InsecureSkipTLSVerify:  opts.InsecureSkipTLSVerify,
			},
		})
	}

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
			Subscriptions:         subs,
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
		// Upstream references also use {app}-{env} naming (per-project namespace).
		qualifiedUpstreams := make([]string, len(upstreamStages))
		for i, us := range upstreamStages {
			qualifiedUpstreams[i] = KargoStageName(app.Name, us)
		}
		sources.Stages = qualifiedUpstreams
	}

	// The promotionTemplate (Kargo v1.x) commits the promoted image tag(s) into
	// the env's values.yaml in the gitops repo, then triggers an ArgoCD sync of
	// the app's Application(s) for this env. With per-cluster naming an env may
	// have one Application per cluster, so argocd-update refreshes them all (the
	// promotion stays env-level). Each Application carries the
	// kargo.akuity.io/authorized-stage annotation so argocd-update may act.
	//
	// The values.yaml path within the gitops repo for this app+env, relative to
	// the repo root. Built via joinSubPath so it matches whatever
	// PublisherConfig.SubPath the publisher used when writing the file. The
	// git-clone step checks the repo out at ./src, so steps reference ./src/<path>.
	var promoTemplate *PromotionTemplate
	if opts.Composed {
		promoTemplate = buildComposedPromotionTemplate(app, env, opts)
	} else {
		valuesFilePath := joinSubPath(opts.SubPath, "envs", env.EnvName, app.ProjectName, app.Name, "values.yaml")
		promoTemplate = buildPromotionTemplate(app, env, opts, valuesFilePath)
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
			PromotionTemplate: promoTemplate,
		},
	}
}

// buildPromotionTemplate returns the Kargo v1.x promotion steps for a Stage:
// clone the gitops repo, set each image's tag in the env values.yaml via the
// imageFrom() expression, commit, push, then sync the app's ArgoCD Application.
// Returns nil when no gitops repo URL is configured (the Stage then has no
// promotion logic — same as the old "no gitRepoUpdates" path).
// kargoImageTagValue is the Kargo expression written into a yaml-update
// updates[].value for an image tag. It wraps imageFrom(repo).Tag in quote() so a
// numeric-looking tag (e.g. "42118849", or a float-looking "1.35") is written as
// a QUOTED YAML string. Without quote(), yaml-update emits image.tag: 42118849
// unquoted → YAML parses it as an int/float → Helm rejects it
// (image.tag: Invalid type. Expected: string, given: integer). See akuity/kargo
// #3743; quote() itself was fixed to not double-quote in v1.3.4 (PR #3794), so
// this requires Kargo >= v1.3.4. Both promotion-template builders use this so the
// two yaml-update sites can't drift.
func kargoImageTagValue(repository string) string {
	return fmt.Sprintf("${{ quote(imageFrom(%q).Tag) }}", repository)
}

func buildPromotionTemplate(app *domain.App, env domain.AppEnvironment, opts KargoBuildOptions, valuesFilePath string) *PromotionTemplate {
	if opts.GitOpsRepoURL == "" {
		return nil
	}

	// One yaml-update "updates" entry per image; all images for an app write the
	// same env values.yaml. imageFrom("<repo>").Tag resolves the promoted tag
	// from the Freight at promotion time.
	updates := make([]map[string]any, 0, len(opts.Images))
	for _, img := range opts.Images {
		updates = append(updates, map[string]any{
			"key":   img.TagKey,
			"value": kargoImageTagValue(img.Repository),
		})
	}

	steps := []PromotionStep{
		{
			Uses: "git-clone",
			Config: map[string]any{
				"repoURL": opts.GitOpsRepoURL,
				"checkout": []map[string]any{
					{"branch": "main", "path": "./src"},
				},
			},
		},
		{
			Uses: "yaml-update",
			Config: map[string]any{
				"path":    "./src/" + valuesFilePath,
				"updates": updates,
			},
		},
		{
			Uses: "git-commit",
			Config: map[string]any{
				"path":    "./src",
				"message": fmt.Sprintf("promote %s/%s to %s", app.ProjectName, app.Name, env.EnvName),
			},
		},
		{
			Uses:   "git-push",
			Config: map[string]any{"path": "./src"},
		},
		{
			Uses: "argocd-update",
			Config: map[string]any{
				"apps": argoUpdateApps(opts.ArgoAppNamePattern, app.ProjectName, app.Name, env.EnvName, opts.Clusters),
			},
		},
	}

	return &PromotionTemplate{Spec: PromotionTemplateSpec{Steps: steps}}
}

// buildComposedPromotionTemplate is the composed counterpart of
// buildPromotionTemplate: each component is its own chart source reading its own
// components/<name>/values.yaml, so the promoted tag for a component's image must
// be written into THAT file. Kargo's yaml-update targets a single path, so this
// emits one yaml-update step per component (images grouped by KargoImage.Name =
// owning component), between the shared git-clone and git-commit/push/argocd-update.
func buildComposedPromotionTemplate(app *domain.App, env domain.AppEnvironment, opts KargoBuildOptions) *PromotionTemplate {
	if opts.GitOpsRepoURL == "" {
		return nil
	}

	// Group images by owning component, preserving first-seen order for determinism.
	byComp := map[string][]KargoImage{}
	var order []string
	for _, img := range opts.Images {
		if _, ok := byComp[img.Name]; !ok {
			order = append(order, img.Name)
		}
		byComp[img.Name] = append(byComp[img.Name], img)
	}

	steps := []PromotionStep{
		{
			Uses: "git-clone",
			Config: map[string]any{
				"repoURL": opts.GitOpsRepoURL,
				"checkout": []map[string]any{
					{"branch": "main", "path": "./src"},
				},
			},
		},
	}
	// Target clusters mirror the publisher's fan-out: with several clusters each
	// component's values live per-cluster under _clusters/<cluster>/…, so the
	// promoted tag must be written into every cluster's file; a single target uses
	// the shared components/<name>/values.yaml path.
	clusters := opts.Clusters
	fanOut := len(clusters) > 1
	compValuePath := func(cluster, comp string) string {
		if fanOut {
			return joinSubPath(opts.SubPath, "envs", env.EnvName, "_clusters", cluster, app.ProjectName, app.Name, "components", comp, "values.yaml")
		}
		return joinSubPath(opts.SubPath, "envs", env.EnvName, app.ProjectName, app.Name, "components", comp, "values.yaml")
	}
	// One yaml-update per (cluster, component) values file.
	for _, comp := range order {
		updates := make([]map[string]any, 0, len(byComp[comp]))
		for _, img := range byComp[comp] {
			updates = append(updates, map[string]any{
				"key":   img.TagKey,
				"value": kargoImageTagValue(img.Repository),
			})
		}
		if !fanOut {
			steps = append(steps, PromotionStep{
				Uses: "yaml-update",
				Config: map[string]any{
					"path":    "./src/" + compValuePath("", comp),
					"updates": updates,
				},
			})
			continue
		}
		for _, cl := range clusters {
			steps = append(steps, PromotionStep{
				Uses: "yaml-update",
				Config: map[string]any{
					"path":    "./src/" + compValuePath(cl.Name, comp),
					"updates": updates,
				},
			})
		}
	}
	steps = append(steps,
		PromotionStep{
			Uses: "git-commit",
			Config: map[string]any{
				"path":    "./src",
				"message": fmt.Sprintf("promote %s/%s to %s", app.ProjectName, app.Name, env.EnvName),
			},
		},
		PromotionStep{Uses: "git-push", Config: map[string]any{"path": "./src"}},
		PromotionStep{
			Uses: "argocd-update",
			Config: map[string]any{
				"apps": argoUpdateApps(opts.ArgoAppNamePattern, app.ProjectName, app.Name, env.EnvName, opts.Clusters),
			},
		},
	)
	return &PromotionTemplate{Spec: PromotionTemplateSpec{Steps: steps}}
}

// argoUpdateApps renders the argocd-update step's app list: one ArgoCD
// Application per destination cluster, named via the org ResourceNaming pattern.
// An env with no resolvable clusters falls back to a single in-cluster entry,
// mirroring the AppSet builder's naming.
func argoUpdateApps(pattern, projectName, appName, envName string, clusters []ClusterTarget) []map[string]any {
	cs := clusters
	if len(cs) == 0 {
		cs = []ClusterTarget{{Name: fallbackClusterName}}
	}
	apps := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		apps = append(apps, map[string]any{
			"name":      RenderArgoAppName(pattern, projectName, appName, envName, c.Name),
			"namespace": defaultArgoCDNS,
		})
	}
	return apps
}

// ── Kargo Project CR ───────────────────────────────────────────────────────────

// KargoProject is a minimal, serialisable representation of a Kargo Project CR.
//
// A Kargo Project marks a namespace as a Kargo tenancy: creating it causes Kargo
// to create (or adopt) the namespace with the correct labels and RBAC, within
// which Warehouse / Stage / Promotion / Freight CRs live. In Kargo v1.x the
// Project CR carries NO spec — promotion policies moved to a separate
// ProjectConfig CR (see KargoProjectConfig). One Project per suparship project;
// the Project name equals the project name (= the namespace name).
//
// Kargo docs: https://docs.kargo.io/concepts#projects
type KargoProject struct {
	APIVersion string     `yaml:"apiVersion"`
	Kind       string     `yaml:"kind"`
	Metadata   ObjectMeta `yaml:"metadata"`
}

// KargoProjectConfig is a serialisable representation of a Kargo v1.x
// ProjectConfig CR. It holds the project's PromotionPolicies (which Stages
// auto-promote). It is namespaced in, and named after, the Kargo Project.
//
// Kargo docs: https://docs.kargo.io/user-guide/how-to-guides/working-with-stages
type KargoProjectConfig struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Metadata   ObjectMeta             `yaml:"metadata"`
	Spec       KargoProjectConfigSpec `yaml:"spec"`
}

// KargoProjectConfigSpec holds the promotion policies for a Kargo project.
type KargoProjectConfigSpec struct {
	// PromotionPolicies governs which Stages auto-promote new Freight.
	PromotionPolicies []KargoPromotionPolicy `yaml:"promotionPolicies,omitempty"`
}

// KargoPromotionPolicy defines the promotion rules for one Stage within a project.
type KargoPromotionPolicy struct {
	// Stage is the name of the Kargo Stage. (Deprecated in favour of
	// stageSelector in newer Kargo, but still accepted.)
	Stage string `yaml:"stage"`
	// AutoPromotionEnabled enables automatic promotion of new Freight into
	// this Stage. Set to true for the first stage in the pipeline (staging).
	AutoPromotionEnabled bool `yaml:"autoPromotionEnabled,omitempty"`
}

// BuildKargoProject returns a project's Kargo v1.x Project CR (no spec) that owns
// its kargo-{project} namespace. The app's Warehouse / Stage / Promotion /
// Freight CRs live inside it; promotion policies live in the ProjectConfig (see
// BuildKargoProjectConfig). It carries labelProject so project teardown can prune
// it by label. BuildKargoProject is a pure function.
func BuildKargoProject(projectName string, brand branding.Config) KargoProject {
	return KargoProject{
		APIVersion: kargoAPIVersion,
		Kind:       kargoKindProject,
		Metadata: ObjectMeta{
			Name: KargoNamespaceForProject(projectName),
			Labels: branding.MergeLabels(
				brand.ManagedByLabels(),
				map[string]string{labelProject: projectName},
			),
		},
	}
}

// BuildKargoPromotionPolicies builds the promotion policies for one app's
// environments: auto-promotion on the first (staging) stage, manual on later
// stages (prod). Stage names are {app}-{env} (the project is the namespace).
// Pure function.
func BuildKargoPromotionPolicies(envs []KargoProjectEnv) []KargoPromotionPolicy {
	policies := make([]KargoPromotionPolicy, 0, len(envs))
	for _, env := range envs {
		policies = append(policies, KargoPromotionPolicy{
			Stage: KargoStageName(env.AppName, env.EnvName),
			// Staging always auto-promotes from the Warehouse. Downstream stages
			// (prod) auto-promote only when the app opts in, so the same verified
			// freight advances staging→prod without a manual promotion. A pinned
			// stage is frozen — auto-promotion is off so the pinned image holds.
			AutoPromotionEnabled: (env.IsFirstStage || env.AutoPromote) && !env.Pinned,
		})
	}
	return policies
}

// BuildKargoProjectConfig returns a project's Kargo v1.x ProjectConfig CR for its
// kargo-{project} namespace, holding the (already-aggregated across the project's
// apps) promotion policies. It is named after, and namespaced in, the
// kargo-{project} namespace and carries labelProject. Pure function.
func BuildKargoProjectConfig(projectName string, policies []KargoPromotionPolicy, brand branding.Config) KargoProjectConfig {
	ns := KargoNamespaceForProject(projectName)
	return KargoProjectConfig{
		APIVersion: kargoAPIVersion,
		Kind:       kargoKindProjectConfig,
		Metadata: ObjectMeta{
			Name:      ns,
			Namespace: ns,
			Labels: branding.MergeLabels(
				brand.ManagedByLabels(),
				map[string]string{labelProject: projectName},
			),
		},
		Spec: KargoProjectConfigSpec{
			PromotionPolicies: policies,
		},
	}
}

// MergeKargoPromotionPolicies returns existing with one app's policies replaced
// by appPolicies. It drops every existing policy whose Stage belongs to appName
// — identified by the "{app}-" name prefix — then appends appPolicies and sorts
// by Stage for deterministic output. Multiple apps in a project share one
// ProjectConfig, so a per-app publish merges (rather than clobbers). Pass an
// empty appPolicies to remove an app's policies entirely (teardown).
//
// Caveat: app DNS labels may contain hyphens, so the prefix match is the same
// best-effort convention used for Kargo Stage names; an exotic ("a","b-c") vs
// ("a-b","c") pair can alias. Acceptable because names derive from validated
// app/env names.
func MergeKargoPromotionPolicies(existing []KargoPromotionPolicy, appName string, appPolicies []KargoPromotionPolicy) []KargoPromotionPolicy {
	prefix := appName + "-"
	merged := make([]KargoPromotionPolicy, 0, len(existing)+len(appPolicies))
	for _, p := range existing {
		if strings.HasPrefix(p.Stage, prefix) {
			continue
		}
		merged = append(merged, p)
	}
	merged = append(merged, appPolicies...)
	sort.Slice(merged, func(i, j int) bool { return merged[i].Stage < merged[j].Stage })
	return merged
}

// KargoProjectEnv describes one environment for Kargo Project generation.
type KargoProjectEnv struct {
	// AppName is the application name. Used together with EnvName to form the
	// Stage name ("{app}-{env}") in PromotionPolicies.
	AppName string
	// EnvName is the environment name (e.g. "staging", "prod").
	EnvName string
	// IsFirstStage marks this as the first stage in the pipeline (receives
	// Freight directly from the Warehouse). Auto-promotion is enabled for it.
	IsFirstStage bool
	// AutoPromote, when true, enables auto-promotion for the downstream stages
	// too (prod), so verified freight flows staging→prod without a manual click.
	AutoPromote bool
	// Pinned, when true, freezes this stage: auto-promotion is disabled (even for
	// the first stage) so newer Warehouse freight does not override the pinned
	// image. The env's values.yaml holds the pinned tag (written by the publisher).
	Pinned bool
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

// KargoNamespaceForProject returns the Kargo Project namespace for a suparship
// project. Each project gets its own Kargo Project (Kargo binds a Project 1:1 to
// a namespace), named "kargo-{project}" so the control-plane namespace can never
// be confused with — or collide with — the project's workload/env namespaces.
func KargoNamespaceForProject(projectName string) string {
	return kargoNamespacePrefix + projectName
}

// KargoStageName returns the Kargo Stage name for an app environment. Stages live
// in the per-project kargo-{project} namespace, so the name is just "{app}-{env}"
// — collision-safe within the project.
func KargoStageName(appName, envName string) string {
	return appName + "-" + envName
}

// Default Kargo tag-selection settings for a CD-managed image when the app's
// selection doesn't override them. The platform convention is to tag images with
// the 7-char git commit SHA and promote the newest build, so a CD-managed app
// rolls forward on every merge. Operators can override both per image.
const (
	// DefaultImageTagPattern matches a 7-character git commit SHA (Kargo allowTags).
	DefaultImageTagPattern = "^[0-9a-f]{7}$"
	// DefaultImageSelectionStrategy promotes the most recently pushed image.
	DefaultImageSelectionStrategy = "NewestBuild"
)

// applyKargoDefaults fills zero-valued KargoBuildOptions with sensible defaults.
// Images are NOT defaulted: an app with no bindings gets no Warehouse at all
// (the publisher prunes and warns), never a placeholder subscription.
func applyKargoDefaults(opts KargoBuildOptions, app *domain.App) KargoBuildOptions {
	if opts.KargoNamespace == "" {
		opts.KargoNamespace = KargoNamespaceForProject(app.ProjectName)
	}
	return opts
}
