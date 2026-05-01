// Package contracts defines the per-type connection contracts addons
// publish into consumer components.
//
// A connection contract is the stable env-var surface a wrapper template
// MUST produce in the addon's connection Secret, regardless of which
// provider (CloudNativePG, Crossplane RDS, valkey-operator, …) backs it
// at the cluster level. App developers write code against the contract;
// suparship binds the right Secret automatically per environment.
//
// New addon types are registered with Register() at init time.
package contracts

import (
	"fmt"
	"slices"
	"sort"
	"sync"
)

// Contract describes a single addon type's connection surface. The
// fields are self-explanatory but kept minimal: any per-provider
// extension belongs in the wrapper chart's values, not on the
// contract.
type Contract struct {
	// Type is the addon type identifier used in AppSpec.Addons[].Type
	// and AddonProfile.Type (e.g. "redis", "postgres").
	Type string
	// RequiredKeys is the set of env-var keys the wrapper Secret MUST
	// contain. Order is significant for documentation but not for
	// validation; the chart-validate pass checks set membership.
	RequiredKeys []string
	// OptionalKeys are documented extras providers MAY emit (e.g. a
	// read-only replica URL). Not asserted by chart-validate.
	OptionalKeys []string
	// Description is shown in template authoring docs and the UI's
	// addon picker.
	Description string
}

// HasKey reports whether `key` is one of the contract's required keys.
func (c Contract) HasKey(key string) bool {
	return slices.Contains(c.RequiredKeys, key)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Contract{}
)

// Register adds a contract to the registry. Panics on duplicate type
// since contracts are init-time data.
func Register(c Contract) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[c.Type]; exists {
		panic(fmt.Sprintf("contracts: duplicate registration for type %q", c.Type))
	}
	registry[c.Type] = c
}

// Lookup returns the contract for `addonType` and whether it was found.
// The contract's slices are returned by value (copies); callers may not
// mutate them.
func Lookup(addonType string) (Contract, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	c, ok := registry[addonType]
	return c, ok
}

// Types returns the sorted list of registered addon types. Used by the
// template-import path to validate AppSpec.Addons[].Type.
func Types() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for t := range registry {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
