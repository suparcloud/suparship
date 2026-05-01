package domain_test

import (
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/addons/contracts"
	"github.com/suparcloud/suparship/internal/domain"
)

func TestAddonSpec_Validate_Valid(t *testing.T) {
	a := domain.AddonSpec{Name: "cache", Type: "redis", Size: "small"}
	if err := a.Validate(); err != nil {
		t.Errorf("expected valid: %v", err)
	}
}

func TestAddonSpec_Validate_BadName(t *testing.T) {
	for _, bad := range []string{"", "Cache", "1cache", "cache-", "cache_db", "this-is-a-very-long-name-that-exceeds-the-thirty-two-character-limit"} {
		t.Run(bad, func(t *testing.T) {
			a := domain.AddonSpec{Name: bad, Type: "redis"}
			if err := a.Validate(); err == nil {
				t.Errorf("expected error for name %q", bad)
			}
		})
	}
}

func TestAddonSpec_Validate_UnknownType(t *testing.T) {
	a := domain.AddonSpec{Name: "queue", Type: "kafka"}
	err := a.Validate()
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
	// Error should list the registered contracts to help the author.
	if !strings.Contains(err.Error(), "registered:") {
		t.Errorf("error %q should list registered types", err.Error())
	}
}

func TestValidateAddons_DuplicateName(t *testing.T) {
	addons := []domain.AddonSpec{
		{Name: "cache", Type: "redis"},
		{Name: "cache", Type: "redis"},
	}
	err := domain.ValidateAddons(addons)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate-name error, got: %v", err)
	}
}

func TestResolveAddonProfile_OrgOnly(t *testing.T) {
	org := domain.AddonProfiles{
		"redis": {Type: "redis", Provider: "valkey-operator", Chart: "valkey"},
	}
	got, err := domain.ResolveAddonProfile(org, nil, "redis")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Provider != "valkey-operator" || got.Chart != "valkey" {
		t.Errorf("resolved = %+v, want valkey-operator/valkey", got)
	}
}

func TestResolveAddonProfile_EnvOverridesOrg(t *testing.T) {
	org := domain.AddonProfiles{
		"redis": {Type: "redis", Provider: "valkey-operator", Chart: "valkey"},
	}
	env := domain.AddonProfiles{
		"redis": {Type: "redis", Provider: "crossplane-elasticache", Chart: "crossplane-redis"},
	}
	got, err := domain.ResolveAddonProfile(org, env, "redis")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Provider != "crossplane-elasticache" {
		t.Errorf("env override should win: got %s", got.Provider)
	}
}

func TestResolveAddonProfile_UnknownType(t *testing.T) {
	_, err := domain.ResolveAddonProfile(domain.AddonProfiles{}, nil, "kafka")
	if err == nil {
		t.Fatal("expected error for unconfigured type")
	}
}

func TestResolveAddonProfile_EmptyChartIsError(t *testing.T) {
	org := domain.AddonProfiles{
		"redis": {Type: "redis", Provider: "valkey-operator"}, // missing Chart
	}
	_, err := domain.ResolveAddonProfile(org, nil, "redis")
	if err == nil || !strings.Contains(err.Error(), "empty chart") {
		t.Errorf("expected empty-chart error, got: %v", err)
	}
}

// Tie-in test: every contracts type registered should be resolvable
// through ValidateAddons when AppSpec.Addons[].Type matches.
func TestAddonSpec_AcceptsAllRegisteredTypes(t *testing.T) {
	for _, ty := range contracts.Types() {
		a := domain.AddonSpec{Name: "x", Type: ty}
		if err := a.Validate(); err != nil {
			t.Errorf("type %q: should validate but got %v", ty, err)
		}
	}
}
