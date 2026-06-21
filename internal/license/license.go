// Package license defines the entitlement seam for suparship editions.
//
// The open-source core ships only the Community validator, which reports the
// "community" edition and grants no enterprise feature entitlements. Enterprise
// builds supply their own Validator (backed by a signed license key) that
// enables specific Features.
//
// Core functionality is never gated through this interface — it exists solely
// so that (a) enterprise modules can switch behaviour on entitlements and
// (b) the API can advertise the active edition and enabled features to clients
// (see GET /api/v1/meta), letting the UI conditionally surface enterprise
// panels. There is deliberately no license-key parsing or "phone home" in the
// core; that logic lives only in enterprise builds.
package license

import "sort"

// Feature is the identifier for a gated enterprise capability (e.g. "saml",
// "scim", "audit-siem", "rbac-abac").
type Feature string

// Edition identifiers reported by Validator.Edition.
const (
	EditionCommunity  = "community"
	EditionEnterprise = "enterprise"
)

// Validator reports the active edition and which enterprise features are
// entitled. Implementations must be safe for concurrent use.
type Validator interface {
	// Edition returns the edition identifier (e.g. "community", "enterprise").
	Edition() string
	// Has reports whether the given enterprise feature is entitled.
	Has(f Feature) bool
	// Features returns the entitled enterprise features in sorted order.
	Features() []Feature
}

// Community is the open-source Validator. It reports the community edition and
// grants no enterprise entitlements. It is the zero value used whenever no
// Validator is supplied.
type Community struct{}

// Edition returns EditionCommunity.
func (Community) Edition() string { return EditionCommunity }

// Has always returns false: the community edition has no enterprise features.
func (Community) Has(Feature) bool { return false }

// Features returns nil: the community edition has no enterprise features.
func (Community) Features() []Feature { return nil }

// Static is a Validator backed by a fixed set of features. Enterprise builds
// can construct one after verifying a signed license key, or it can be used in
// tests. The community core never constructs a Static with features.
type Static struct {
	edition  string
	features map[Feature]struct{}
}

// NewStatic returns a Static validator reporting the given edition and
// entitled features.
func NewStatic(edition string, features ...Feature) Static {
	m := make(map[Feature]struct{}, len(features))
	for _, f := range features {
		m[f] = struct{}{}
	}
	return Static{edition: edition, features: m}
}

// Edition returns the configured edition.
func (s Static) Edition() string {
	if s.edition == "" {
		return EditionCommunity
	}
	return s.edition
}

// Has reports whether f is in the entitled set.
func (s Static) Has(f Feature) bool {
	_, ok := s.features[f]
	return ok
}

// Features returns the entitled features in sorted order.
func (s Static) Features() []Feature {
	out := make([]Feature, 0, len(s.features))
	for f := range s.features {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Resolve returns v, or a Community validator when v is nil. Call sites that
// accept an optional Validator use this to obtain a non-nil value.
func Resolve(v Validator) Validator {
	if v == nil {
		return Community{}
	}
	return v
}
