package domain

import "fmt"

// DefaultRoutingProfiles returns the seed RoutingProfiles map for fresh
// installs: a single "internal" profile pointing at the conventional nginx
// IngressClass with no TLS. Operators add an "external" profile (and any
// custom tiers) themselves via the org config UI.
//
// The internal-only default keeps local-dev installs working out of the box
// without requiring cert-manager: components that say `exposeMode: internal`
// get a plain HTTP ingress on the nginx class, which is the standard kind
// cluster setup.
func DefaultRoutingProfiles() RoutingProfiles {
	return RoutingProfiles{
		string(ExposeInternal): {
			IngressClassName: "nginx",
		},
	}
}

// ResolveRoutingProfile returns the effective RoutingProfile for the given
// ExposeMode, merging org-level defaults with per-environment overrides.
//
// Resolution rules:
//
//  1. mode == ExposeDisabled returns the zero value with a nil error. Callers
//     should not emit an ingress in that case.
//  2. envProfiles wins over orgProfiles when a key is present in both. The
//     env entry replaces (not field-merges) the org entry — sparse override
//     happens at the map level, not within RoutingProfile fields.
//  3. The mode name must be present in either map; an unknown mode returns
//     an error so app save and gitops publish fail loud rather than silently
//     dropping the ingress.
//  4. The resolved profile must have a non-empty IngressClassName; an empty
//     class is a configuration error.
//
// Callers should pass the org's full RoutingProfiles map and the (optionally
// nil) environment's override map. mode is the component's
// EffectiveExposeMode().
func ResolveRoutingProfile(orgProfiles, envProfiles RoutingProfiles, mode ExposeMode) (RoutingProfile, error) {
	if mode == ExposeDisabled || mode == "" {
		return RoutingProfile{}, nil
	}
	if !mode.Valid() {
		return RoutingProfile{}, fmt.Errorf("resolve routing profile: %w",
			fmt.Errorf("invalid expose mode %q", mode))
	}

	key := string(mode)
	if profile, ok := envProfiles[key]; ok {
		return validateProfile(profile, mode)
	}
	if profile, ok := orgProfiles[key]; ok {
		return validateProfile(profile, mode)
	}
	return RoutingProfile{}, fmt.Errorf("resolve routing profile: no profile named %q configured for org or environment", mode)
}

func validateProfile(p RoutingProfile, mode ExposeMode) (RoutingProfile, error) {
	if p.IngressClassName == "" {
		return RoutingProfile{}, fmt.Errorf("resolve routing profile: profile %q has empty ingressClassName", mode)
	}
	return p, nil
}
