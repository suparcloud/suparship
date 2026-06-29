package domain

// TemplateOverride is an org/platform-level Helm values overlay layered on top of
// a template's own DefaultValues/EnvValues and below a developer's per-app
// overrides at publish. Platform engineers author it from the template UI to
// apply org-specific defaults (e.g. resource limits, env values) to a chart
// WITHOUT forking the upstream template.
//
// It is stored as its own org resource — separate from the template's ConfigMap —
// so an external template sync (which overwrites the template body) never clobbers
// it. Shape mirrors tpl.TemplateSpec.DefaultValues/EnvValues so the publisher can
// merge them into the same platform layer.
type TemplateOverride struct {
	// DefaultValues applies to every environment.
	DefaultValues map[string]any `json:"defaultValues,omitempty" yaml:"defaultValues,omitempty"`
	// EnvValues holds per-environment overlays keyed by environment name,
	// layered after DefaultValues.
	EnvValues map[string]map[string]any `json:"envValues,omitempty" yaml:"envValues,omitempty"`
	// ClusterValues holds per-cluster overlays keyed by cluster ref, layered
	// after EnvValues. Env-agnostic: a cluster's block applies in every env that
	// deploys to it — for cloud-intrinsic structured annotations (e.g. an
	// Azure-internal-LB or AWS-NLB annotation) that token substitution can't
	// express. Simple per-cluster values can instead use {platform.cluster} or
	// cluster-scoped {vars.*}.
	ClusterValues map[string]map[string]any `json:"clusterValues,omitempty" yaml:"clusterValues,omitempty"`
	// PreviewDefaultValues is a default Helm values overlay applied to EVERY
	// preview of apps built from this template, layered on top of the base env's
	// composition and BELOW the app's own preview override (apps can modify/
	// extend). Preview-only — does not affect stable envs. Sync-safe.
	PreviewDefaultValues map[string]any `json:"previewDefaultValues,omitempty" yaml:"previewDefaultValues,omitempty"`
	// Metadata holds operator-set display-metadata overrides (title/category/
	// description) that win over the template's own. Lets operators fix
	// auto-import mistakes on read-only synced/built-in templates from the UI
	// without editing the source — stored here so a re-sync can't clobber it.
	Metadata *TemplateMetadataOverride `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	// Images holds the per-service image mapping (external-CD / Kargo wiring)
	// when set from the UI for a read-only synced/built-in template. When
	// non-empty it REPLACES the template's own image mapping at read + publish,
	// so operators can wire up CD for a BYO chart without editing its source.
	// Mirrors tpl.TemplateImage; the server layer converts.
	Images []TemplateImageOverride `json:"images,omitempty" yaml:"images,omitempty"`
	// DeliveryMode overrides the template's default app delivery mode ("pipeline"
	// or "direct") for read-only synced/built-in templates, so an operator can
	// mark a synced off-the-shelf chart (valkey, redis, postgres) "direct" without
	// editing its source. Empty = no override (use the template's own). Sync-safe.
	DeliveryMode string `json:"deliveryMode,omitempty" yaml:"deliveryMode,omitempty"`
}

// TemplateMetadataOverride carries display-metadata overrides. Each field is
// applied only when non-empty (an empty string means "no override — use the
// template's own value").
type TemplateMetadataOverride struct {
	Title       string `json:"title,omitempty" yaml:"title,omitempty"`
	Category    string `json:"category,omitempty" yaml:"category,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// TemplateImageOverride mirrors tpl.TemplateImage for storage in a sync-safe
// override (domain must not import tpl). The server layer converts between them.
type TemplateImageOverride struct {
	Name              string `json:"name" yaml:"name"`
	Repository        string `json:"repository" yaml:"repository"`
	TagKey            string `json:"tagKey" yaml:"tagKey"`
	TagPattern        string `json:"tagPattern,omitempty" yaml:"tagPattern,omitempty"`
	SelectionStrategy string `json:"selectionStrategy,omitempty" yaml:"selectionStrategy,omitempty"`
}

// IsEmpty reports whether the metadata override carries nothing.
func (m *TemplateMetadataOverride) IsEmpty() bool {
	return m == nil || (m.Title == "" && m.Category == "" && m.Description == "")
}

// IsEmpty reports whether the override carries nothing — used to decide whether
// to persist a ConfigMap at all (an empty override is deleted, not stored).
func (o *TemplateOverride) IsEmpty() bool {
	if o == nil {
		return true
	}
	if len(o.DefaultValues) > 0 {
		return false
	}
	for _, ev := range o.EnvValues {
		if len(ev) > 0 {
			return false
		}
	}
	for _, cv := range o.ClusterValues {
		if len(cv) > 0 {
			return false
		}
	}
	if len(o.Images) > 0 {
		return false
	}
	if o.DeliveryMode != "" {
		return false
	}
	return o.Metadata.IsEmpty()
}
