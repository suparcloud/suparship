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
	return true
}
