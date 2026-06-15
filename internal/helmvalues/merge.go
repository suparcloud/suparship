package helmvalues

// DeepMerge recursively merges overlay into base and returns base. Nested maps
// are merged key-by-key; any non-map value (scalar, slice) in overlay replaces
// the base value wholesale. A nil base is initialized.
//
// This is the single merge used to layer Helm values overlays (chart defaults ⊕
// platform/env defaults ⊕ developer rawValues) both at publish time and when the
// API previews effective values, so the two paths agree exactly.
func DeepMerge(base, overlay map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	for k, ov := range overlay {
		if ovMap, ok := ov.(map[string]any); ok {
			if baseMap, ok := base[k].(map[string]any); ok {
				base[k] = DeepMerge(baseMap, ovMap)
				continue
			}
		}
		base[k] = ov
	}
	return base
}

// DeepCopyMap returns a deep copy of a YAML/JSON-decoded map so callers can
// mutate (merge/interpolate) without touching the stored source.
func DeepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = deepCopyValue(v)
	}
	return out
}

func deepCopyValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return DeepCopyMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = deepCopyValue(e)
		}
		return out
	default:
		return v
	}
}
