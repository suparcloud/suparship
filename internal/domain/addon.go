package domain

import (
	"fmt"
	"regexp"

	"github.com/suparcloud/suparship/internal/addons/contracts"
)

// AddonSpec is one addon claim on an app — the developer's intent
// declaration ("I need a postgres, size small"). The implementation
// (which K8s operator or managed service backs it) is decided per
// environment by the org's AddonProfiles map.
//
// Each claim materialises at publish time as a parallel ArgoCD
// Application alongside the main app, deployed into the same
// namespace. The wrapper chart referenced by the resolved AddonProfile
// produces a connection Secret (the per-type contract); the publisher
// adds that Secret name to every consumer component's
// suparship.envFromSecrets[] hierarchy so app code reads the
// connection from env vars without per-env knowledge.
//
// Secret values MUST NOT appear in Values; route credentials through
// env-config or chart-side SecretRefs (chart-specific contract).
type AddonSpec struct {
	// Name is the local identifier within the app (e.g. "cache",
	// "primary-db"). Used in connection-Secret naming and as the
	// addon's per-app key in HelmValues.Addons. DNS-label rules.
	Name string `json:"name" yaml:"name"`
	// Type identifies the addon's connection contract (e.g.
	// "redis", "postgres"). Must match a registered
	// contracts.Lookup entry and an AddonProfile keyed by the same
	// type at app save time.
	Type string `json:"type" yaml:"type"`
	// Size is the addon's resource preset; the wrapper chart maps it
	// (small / medium / large) to provider-specific sizing. Optional;
	// the wrapper picks a sensible default when empty.
	Size string `json:"size,omitempty" yaml:"size,omitempty"`
	// Version pins a major version for the underlying engine
	// (e.g. "16" for postgres). Optional. Wrapper chart honours it
	// where supported; ignored otherwise.
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	// Values carries per-app overrides passed through to the wrapper
	// chart's values. Merged on top of the AddonProfile.Defaults at
	// publish time (per-app wins on conflict).
	Values map[string]any `json:"values,omitempty" yaml:"values,omitempty"`
}

// addonNameRE matches DNS-label addon names: starts with a lowercase
// letter, only lowercase alphanumerics and hyphens, ends in alphanumeric.
// 1–63 chars (Kubernetes label limit minus the `<app>-addon-` prefix
// budget kept under 63 in practice).
var addonNameRE = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,30}[a-z0-9])?$`)

// Validate returns nil when the addon spec is well-formed and the
// type matches a registered connection contract.
func (a AddonSpec) Validate() error {
	if !addonNameRE.MatchString(a.Name) {
		return fmt.Errorf("addon name %q must be a DNS label (lowercase, alphanumeric + hyphen, ≤ 32 chars)", a.Name)
	}
	if _, ok := contracts.Lookup(a.Type); !ok {
		return fmt.Errorf("addon %q: unknown type %q (registered: %v)", a.Name, a.Type, contracts.Types())
	}
	return nil
}

// ValidateAddons checks every claim is well-formed and that no two
// claims share a name within the same app.
func ValidateAddons(addons []AddonSpec) error {
	seen := make(map[string]bool, len(addons))
	for i, a := range addons {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("addons[%d]: %w", i, err)
		}
		if seen[a.Name] {
			return fmt.Errorf("addons[%d]: duplicate name %q", i, a.Name)
		}
		seen[a.Name] = true
	}
	return nil
}

// ResolveAddonProfile returns the effective AddonProfile for the given
// addon type, merging org-level defaults with per-environment
// overrides. Mirrors ResolveRoutingProfile semantics:
//
//  1. envProfiles wins over orgProfiles when a type key is present in
//     both. The env entry replaces (does not field-merge) the org
//     entry.
//  2. The type must be present in either map; an unknown type returns
//     an error so app save fails loudly rather than silently dropping
//     the addon.
//  3. The resolved profile must declare a non-empty Chart name; an
//     empty chart is a configuration error.
func ResolveAddonProfile(orgProfiles, envProfiles AddonProfiles, addonType string) (AddonProfile, error) {
	if addonType == "" {
		return AddonProfile{}, fmt.Errorf("resolve addon profile: empty addon type")
	}
	if profile, ok := envProfiles[addonType]; ok {
		return validateAddonProfile(profile, addonType)
	}
	if profile, ok := orgProfiles[addonType]; ok {
		return validateAddonProfile(profile, addonType)
	}
	return AddonProfile{}, fmt.Errorf("resolve addon profile: no profile configured for type %q at org or environment level", addonType)
}

func validateAddonProfile(p AddonProfile, addonType string) (AddonProfile, error) {
	if p.Chart == "" {
		return AddonProfile{}, fmt.Errorf("resolve addon profile: profile %q has empty chart name", addonType)
	}
	if p.Type == "" {
		// Tolerate omission; canonicalise from the lookup key.
		p.Type = addonType
	}
	return p, nil
}
