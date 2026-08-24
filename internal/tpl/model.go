// Package tpl defines the suparship template metadata schema.
//
// A template is a plain Helm chart (registered through the template registry
// or a BYO chart upload) plus optional metadata described by a template.yaml.
// When a user picks a template, suparship creates an app — not a raw
// Kubernetes resource — that is then rendered into GitOps manifests by the
// appropriate engine (Helm in MVP).
//
// Templates declare no components and no values schema: components are
// user-declared on the app, and all configuration is Helm values overlays in
// the chart's own shape. template.yaml carries curated metadata only —
// platform-authored value overlays (defaultValues / envValues /
// previewDefaultValues), the developerValues projection, image slots for CD
// wiring, and secret-reference inputs.
//
// See docs/templates.md for the template.yaml reference.
// See docs/byo-charts.md for the chart-side contract.
// See docs/app-model.md for the App / Environment / Component model.
//
// Example template.yaml (shipped next to the chart in a chart source):
//
//	apiVersion: suparship.io/v1alpha1
//	kind: Template
//	metadata:
//	  name: web
//	  version: "1.0.0"
//	spec:
//	  title: Web App
//	  description: Create a containerized web app
//	  category: web
//	  engine:
//	    type: helm
//	    chart: ./chart
//	  developerValues:
//	    - path: image.repository
//	      title: Image Repository
//	      type: string
//	      required: true
package tpl

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

const (
	CurrentAPIVersion = "suparship.io/v1alpha1"
	TemplateKind      = "Template"
	TemplateFileName  = "template.yaml"

	EngineHelm = "helm"
)

// TemplateComponentType enumerates the supported runtime roles for template
// components. Values are identical to domain.ComponentType and are kept as
// plain string constants to avoid a cross-package import in this layer.
type TemplateComponentType string

const (
	TemplateComponentWeb    TemplateComponentType = "web"
	TemplateComponentWorker TemplateComponentType = "worker"
	TemplateComponentCron   TemplateComponentType = "cron"
	// TemplateComponentJob is a one-shot component (Kubernetes Job) — e.g. a DB
	// migration run before the long-running components. Mirrors domain.ComponentJob.
	TemplateComponentJob TemplateComponentType = "job"
)

// InputType enumerates the supported input value types.
type InputType string

const (
	InputTypeString  InputType = "string"
	InputTypeNumber  InputType = "number"
	InputTypeBoolean InputType = "boolean"
	InputTypeEnum    InputType = "enum"
)

// Template is the top-level structure of a template.yaml file.
type Template struct {
	APIVersion string       `yaml:"apiVersion"`
	Kind       string       `yaml:"kind"`
	Metadata   Metadata     `yaml:"metadata"`
	Spec       TemplateSpec `yaml:"spec"`
}

// Metadata identifies a template.
type Metadata struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

// TemplateSpec defines the template's behavior and user-facing configuration.
type TemplateSpec struct {
	Title          string            `yaml:"title"`
	Description    string            `yaml:"description,omitempty"`
	Category       string            `yaml:"category"`
	Engine         Engine            `yaml:"engine"`
	Inputs         []Input           `yaml:"inputs,omitempty"`
	AdvancedInputs []Input           `yaml:"advancedInputs,omitempty"`
	SecretInputs   []SecretInput     `yaml:"secretInputs,omitempty"`
	Mappings       map[string]string `yaml:"mappings,omitempty"`
	Presets        []Preset          `yaml:"presets,omitempty"`
	// DefaultValues is a Platform-Engineer-authored Helm values overlay applied
	// to EVERY environment, layered on top of the chart's own default values
	// (and below per-env EnvValues and developer overrides). Arbitrary Helm
	// values (BYO-chart friendly); string leaves may use ((platform.*))/((vars.*))
	// tokens resolved at publish.
	DefaultValues map[string]any `yaml:"defaultValues,omitempty"`
	// EnvValues holds per-environment Helm values overlays keyed by environment
	// name (e.g. "staging", "prod"), layered after DefaultValues — so an org can
	// set a smaller staging baseline and a larger prod one.
	EnvValues map[string]map[string]any `yaml:"envValues,omitempty"`
	// PreviewDefaultValues is a default Helm values overlay applied to EVERY
	// preview of apps built from this template, on top of the base env's
	// composition and below the app's own preview override. Preview-only — does
	// not affect stable envs. String leaves may use ((platform.*))/((vars.*)) tokens.
	PreviewDefaultValues map[string]any `yaml:"previewDefaultValues,omitempty"`
	// Images declares the container images this chart deploys, one entry per
	// service, so external-CD (Kargo) can be wired generically: each entry says
	// which image repository to watch and which Helm values key holds its tag.
	// Auto-detected at chart import and editable in template settings. Empty for
	// templates that don't opt into image-driven CD (the publisher then falls
	// back to the legacy single-image behaviour).
	Images []TemplateImage `yaml:"images,omitempty"`
	// DeveloperValues declares the small, ordered projection of this chart's Helm
	// values that belongs to the DEVELOPER — everything else in DefaultValues /
	// EnvValues / PreviewDefaultValues (and in the chart) stays platform-owned and
	// out of the app-creation editor. Empty = no projection declared, and the
	// editor keeps seeding from the full concise platform base (today's behaviour).
	//
	// This is a VIEW, not an enforcement boundary: the editor's "Advanced" toggle
	// still reveals the full base, and the API still accepts arbitrary rawValues.
	DeveloperValues []ValueField `yaml:"developerValues,omitempty"`
	// DeliveryMode is the default delivery mode for apps created from this
	// template: "pipeline" (default; "" == this) for CI-image apps promoted via
	// Kargo, or "direct" for off-the-shelf software (valkey, redis, postgres)
	// deployed to each env straight from values with no Kargo/promotion. The
	// create wizard pre-selects this; the user can override per app.
	DeliveryMode string `yaml:"deliveryMode,omitempty"`
	// Features declares optional platform operations this template's chart
	// supports and the values keys that drive them (currently suspend/resume).
	Features *TemplateFeatures `yaml:"features,omitempty"`
}

// TemplateImage maps one of a chart's services to its image source and the Helm
// values key that holds its tag. It carries everything Kargo needs: the repo to
// watch (Warehouse subscription), the values key to rewrite on promotion (Stage
// helm image update), and how to discover/select tags.
type TemplateImage struct {
	// Name is a logical service identifier (e.g. "agent", "web"). Unique per template.
	Name string `yaml:"name"`
	// Repository is the container image repository to watch (no tag).
	Repository string `yaml:"repository"`
	// TagKey is the dotted Helm values key that holds this image's tag
	// (e.g. "image.tag" or "components.web.image.tag").
	TagKey string `yaml:"tagKey"`
	// TagPattern is an optional regex limiting which tags Kargo considers
	// (maps to the Warehouse subscription's allowTags).
	TagPattern string `yaml:"tagPattern,omitempty"`
	// SelectionStrategy is how Kargo picks the tag to promote: one of
	// "NewestBuild", "SemVer", "Digest", "Lexical". Empty defaults to "SemVer"
	// (Kargo's own default).
	SelectionStrategy string `yaml:"selectionStrategy,omitempty"`
	// Declared is a TRANSIENT discovery annotation (never persisted — yaml:"-"): set
	// on a DISCOVERED image when it matches one of the template's declared image
	// slots by TagKey, i.e. the template defines a pull rule for it. Drives the
	// "inherit from template" default (auto-watch declared images; sidecars/undeclared
	// images default off).
	Declared bool `yaml:"-"`
}

// DefaultSuspendKey is the convention Helm values key the platform toggles for
// suspend/resume when a template does not declare its own. Generic charts honor
// a top-level `suspend: true` to scale the workload down.
const DefaultSuspendKey = "suspend"

// TemplateFeatures declares optional platform operations this template's chart
// supports and how to drive them. Extensible; currently only suspend/resume.
type TemplateFeatures struct {
	// Suspend, when set, declares the chart supports suspend/resume. Its ValuesKey
	// names the dotted Helm values key the platform sets true to suspend. Absent
	// ValuesKey => DefaultSuspendKey ("suspend"). Leaving Suspend nil also falls
	// back to the convention key — every app can suspend via `suspend: true`; a
	// chart that ignores it simply doesn't react.
	Suspend *FeatureToggle `yaml:"suspend,omitempty"`
}

// FeatureToggle maps a platform feature to the Helm values key that drives it.
type FeatureToggle struct {
	// ValuesKey is the dotted Helm values path the platform writes (e.g.
	// "suspend" or "components.web.suspend"). Empty uses the feature's default.
	ValuesKey string `yaml:"valuesKey,omitempty"`
}

// SuspendKey returns the dotted Helm values key the platform toggles to suspend
// this template's workload: the declared key when set, else DefaultSuspendKey.
func (s TemplateSpec) SuspendKey() string {
	if s.Features != nil && s.Features.Suspend != nil && s.Features.Suspend.ValuesKey != "" {
		return s.Features.Suspend.ValuesKey
	}
	return DefaultSuspendKey
}

// Engine specifies the rendering backend for the template.
type Engine struct {
	Type  string       `yaml:"type"`
	Chart ChartLocator `yaml:"chart,omitempty"`
}

// ChartLocator is a oneOf describing where the Helm chart for this template
// lives. Three legal shapes (the field is omitted from YAML in the bundled
// case via IsZero):
//
//   - Bundled  (zero value):  The chart is the .tgz this template was loaded
//     from. Used when a chart author ships a template.yaml inside their chart
//     and it gets pulled from a registry, or when the BYO upload flow imports
//     a chart that ships its own template.yaml.
//   - Inline   (Path != ""):  Relative path (e.g. "./chart") inside the
//     templates repo this template was loaded from. Today's convention.
//   - External (Ref != nil): Reference to a chart in a Helm registry. The
//     publisher emits an Argo-native multi-source Application; the chart is
//     never copied into the gitops repo.
//
// At most one of Path / Ref may be set; the validator enforces this.
//
// The custom YAML marshaller emits the right shape for each mode and accepts
// either a scalar (string → Path) or a mapping (→ Ref) on parse.
type ChartLocator struct {
	Path string
	Ref  *ChartRef
}

// ChartRef is the registry-ref form of a chart locator.
type ChartRef struct {
	// Repository is the chart registry URL. Examples:
	//   oci://ghcr.io/myorg/charts
	//   https://charts.acme.io
	Repository string `yaml:"repository" json:"repository"`
	// Name is the chart name within the registry.
	Name string `yaml:"name" json:"name"`
	// Version pins the chart to a specific release. SemVer string.
	Version string `yaml:"version" json:"version"`
}

// IsBundled reports whether the locator is the bundled (zero) form.
func (l ChartLocator) IsBundled() bool { return l.Path == "" && l.Ref == nil }

// IsInline reports whether the locator is the relative-path form.
func (l ChartLocator) IsInline() bool { return l.Path != "" }

// IsExternal reports whether the locator is the registry-ref form.
func (l ChartLocator) IsExternal() bool { return l.Ref != nil }

// IsZero satisfies yaml.IsZeroer so `omitempty` drops the field for bundled
// locators. Without this the encoder emits `chart: {}` for the zero struct.
func (l ChartLocator) IsZero() bool { return l.IsBundled() }

// MarshalYAML emits the locator in the right shape for its current mode.
// Bundled is handled by omitempty + IsZero (this method isn't called for
// the zero value).
func (l ChartLocator) MarshalYAML() (any, error) {
	if l.IsExternal() {
		return l.Ref, nil
	}
	if l.IsInline() {
		return l.Path, nil
	}
	// Defensive: shouldn't be reached because IsZero covers bundled.
	return nil, nil
}

// UnmarshalYAML accepts either a scalar (relative path) or a mapping
// (ChartRef). An absent field leaves the locator zero (bundled).
func (l *ChartLocator) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		// Empty scalar → treat as bundled. Lets `chart:` (with nothing
		// after the colon) parse as bundled rather than as path "".
		if node.Tag == "!!null" || node.Value == "" {
			return nil
		}
		l.Path = node.Value
		return nil
	case yaml.MappingNode:
		var ref ChartRef
		if err := node.Decode(&ref); err != nil {
			return fmt.Errorf("engine.chart: %w", err)
		}
		l.Ref = &ref
		return nil
	default:
		return fmt.Errorf("engine.chart: expected string or mapping, got node kind %d", node.Kind)
	}
}

// Input defines a user-configurable parameter.
type Input struct {
	Name        string    `yaml:"name"`
	Title       string    `yaml:"title"`
	Type        InputType `yaml:"type"`
	Description string    `yaml:"description,omitempty"`
	Required    bool      `yaml:"required,omitempty"`
	Default     any       `yaml:"default,omitempty"`
	Options     []string  `yaml:"options,omitempty"`
	Min         *float64  `yaml:"min,omitempty"`
	Max         *float64  `yaml:"max,omitempty"`
	Pattern     string    `yaml:"pattern,omitempty"`
	// Component scopes this input to a specific named component defined in
	// spec.components. When set, the input value configures only that
	// component. Must match a name in spec.components when non-empty.
	// Omit for app-level inputs that apply across all components.
	Component string `yaml:"component,omitempty"`
}

// ValueField declares ONE developer-facing Helm value: the values path the
// developer owns, plus the metadata needed to present it.
//
// It supersedes the Input + Mappings pair. Input is keyed by a synthetic name and
// needs a `mappings:` entry ("{{ .inputs.port }}") to reach a values path; a
// ValueField IS the path, which is how every consumer downstream of the
// values-editor-first flow already works.
//
// The presentation metadata deliberately mirrors Input's so a future form renderer
// and the existing project.validateInputValue both apply unchanged: today the
// platform seeds a commented YAML overlay from these, later it can render a form
// from the same declaration with no data-model migration.
type ValueField struct {
	// Path is the dotted Helm values path this field owns (e.g.
	// "components.web.image.repository"). A path resolving to a map projects that
	// whole subtree. Dotted form cannot express a key containing a dot — the same
	// constraint TemplateImage.TagKey and Mappings keys already carry.
	Path string `yaml:"path"`
	// Mirrors lists additional dotted paths that receive the SAME value as Path —
	// one question, many keys (e.g. path containerPort with mirror service.port).
	// Purely an editor concern: the form writes the value to every path and the
	// seeded YAML emits every path, so the stored overlay and publish stay plain
	// Helm values. Until the developer sets the field, each path keeps its own
	// inherited value — mirrors may legitimately diverge before opt-in.
	Mirrors []string `yaml:"mirrors,omitempty"`
	// Title is the human label; falls back to Path when empty.
	Title string `yaml:"title,omitempty"`
	// Type drives future form rendering and value validation. Empty = free-form.
	Type InputType `yaml:"type,omitempty"`
	// Description is shown as help text (a YAML comment in the 0.1 editor).
	Description string `yaml:"description,omitempty"`
	// Required marks a field the developer MUST supply. Required fields are seeded
	// live (uncommented); everything else is seeded commented-out so an untouched
	// value keeps inheriting from the chart/platform instead of being pinned into
	// the app's overlay.
	Required bool `yaml:"required,omitempty"`
	// Default is the value to show when the path is absent from the effective values.
	Default any `yaml:"default,omitempty"`
	// Options enumerates the allowed values for Type "enum".
	Options []string `yaml:"options,omitempty"`
	// Min/Max/Pattern constrain numeric and string fields.
	Min     *float64 `yaml:"min,omitempty"`
	Max     *float64 `yaml:"max,omitempty"`
	Pattern string   `yaml:"pattern,omitempty"`
}

// SecretInput defines a parameter whose value is a reference to a
// Kubernetes Secret key, never a literal value.
type SecretInput struct {
	Name        string `yaml:"name"`
	Title       string `yaml:"title"`
	Description string `yaml:"description,omitempty"`
	SecretRef   string `yaml:"secretRef"`
}

// Preset provides a named set of default input values.
type Preset struct {
	Name        string         `yaml:"name"`
	Title       string         `yaml:"title"`
	Description string         `yaml:"description,omitempty"`
	Values      map[string]any `yaml:"values"`
}

// Parse deserializes and validates a Template from YAML bytes.
func Parse(data []byte) (*Template, error) {
	var t Template
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parsing template: %w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("invalid template: %w", err)
	}
	return &t, nil
}

// Marshal serializes a validated Template back to YAML.
func Marshal(t *Template) ([]byte, error) {
	if t == nil {
		return nil, fmt.Errorf("nil template")
	}
	out, err := yaml.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("marshal template: %w", err)
	}
	return out, nil
}
