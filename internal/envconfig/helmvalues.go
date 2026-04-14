package envconfig

// HelmSecretRef is the Helm-values representation of a SecretRef.
// It is safe to include in values.yaml — no secret values, only references.
type HelmSecretRef struct {
	Provider string `json:"provider" yaml:"provider"`
	Name     string `json:"name"     yaml:"name"`
	Key      string `json:"key"      yaml:"key"`
	EnvKey   string `json:"envKey"   yaml:"envKey"`
}

// HelmEnvLayer is the values.yaml representation of one hierarchy level's
// env vars and secret refs. Only non-empty fields are serialised.
type HelmEnvLayer struct {
	// Vars are rendered as ConfigMap data in the app namespace.
	Vars map[string]string `json:"vars,omitempty" yaml:"vars,omitempty"`
	// SecretRefs are rendered as ExternalSecret data items in the app namespace.
	SecretRefs []HelmSecretRef `json:"secretRefs,omitempty" yaml:"secretRefs,omitempty"`
}

// IsEmpty reports whether the layer has no data.
func (l *HelmEnvLayer) IsEmpty() bool {
	return l == nil || (len(l.Vars) == 0 && len(l.SecretRefs) == 0)
}

// HelmEnvLayers is the top-level values.yaml key that holds env layer data
// for the two levels managed by the GitOps publisher (App and AppEnv).
// Upper levels (Org, Environment, Project) are replicated separately and are
// not included here to avoid mass re-publish on upper-level changes.
type HelmEnvLayers struct {
	// App holds env config applied to all environments of this app.
	App *HelmEnvLayer `json:"app,omitempty" yaml:"app,omitempty"`
	// AppEnv holds env config for this specific app+environment combination.
	AppEnv *HelmEnvLayer `json:"appEnv,omitempty" yaml:"appEnv,omitempty"`
}

// IsEmpty reports whether there are no layers with data.
func (h HelmEnvLayers) IsEmpty() bool {
	return h.App.IsEmpty() && h.AppEnv.IsEmpty()
}

// ToHelmEnvLayers converts the lower two levels of an EnvLayers into the
// HelmEnvLayers struct suitable for inclusion in values.yaml.
// Upper levels are intentionally excluded (they are replicated separately).
func ToHelmEnvLayers(layers EnvLayers) HelmEnvLayers {
	h := HelmEnvLayers{}
	if !layers.App.IsEmpty() {
		h.App = toHelmLayer(layers.App)
	}
	if !layers.AppEnv.IsEmpty() {
		h.AppEnv = toHelmLayer(layers.AppEnv)
	}
	return h
}

func toHelmLayer(c EnvConfig) *HelmEnvLayer {
	if c.IsEmpty() {
		return nil
	}
	layer := &HelmEnvLayer{}
	if len(c.Vars) > 0 {
		layer.Vars = make(map[string]string, len(c.Vars))
		for k, v := range c.Vars {
			layer.Vars[k] = v
		}
	}
	if len(c.SecretRefs) > 0 {
		layer.SecretRefs = make([]HelmSecretRef, len(c.SecretRefs))
		for i, ref := range c.SecretRefs {
			layer.SecretRefs[i] = HelmSecretRef{
				Provider: ref.Provider,
				Name:     ref.Name,
				Key:      ref.Key,
				EnvKey:   ref.EnvKey,
			}
		}
	}
	return layer
}

// SecretRefsByProvider groups a slice of HelmSecretRef by their Provider field.
// The result is used by the Helm chart to create one ExternalSecret per
// (layer, provider) pair.
func SecretRefsByProvider(refs []HelmSecretRef) map[string][]HelmSecretRef {
	if len(refs) == 0 {
		return nil
	}
	out := make(map[string][]HelmSecretRef)
	for _, ref := range refs {
		out[ref.Provider] = append(out[ref.Provider], ref)
	}
	return out
}
