package helmvalues

import (
	"slices"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
)

func appWithAddons(name string, claims ...domain.AddonSpec) *domain.App {
	return &domain.App{
		Name:        name,
		ProjectName: "demo",
		Spec: domain.AppSpec{
			Components: []domain.ComponentSpec{
				{Name: "web", Type: domain.ComponentWeb},
			},
			Addons: claims,
		},
	}
}

func TestMapper_AddonClaim_AppendsConnectionSecret(t *testing.T) {
	app := appWithAddons("hello",
		domain.AddonSpec{Name: "cache", Type: "redis", Size: "small"},
	)
	orgAddons := domain.AddonProfiles{
		"redis": {Type: "redis", Provider: "valkey-operator", Chart: "valkey"},
	}

	hv := MapToHelmValuesForEnv(app, "staging", domain.AppEnvStaging,
		"localhost", "", "", noNaming(), "", "", nil, nil, orgAddons, nil)

	wantSecret := "hello-addon-cache-conn"
	if !slices.Contains(hv.Suparship.EnvFromSecrets, wantSecret) {
		t.Errorf("envFromSecrets missing %q (got %v)", wantSecret, hv.Suparship.EnvFromSecrets)
	}
	if len(hv.ServiceClaims) != 1 {
		t.Fatalf("ServiceClaims = %d, want 1", len(hv.ServiceClaims))
	}
	c := hv.ServiceClaims[0]
	if c.Addon != "cache" || c.Type != "redis" || c.SecretName != wantSecret || c.Component != "" {
		t.Errorf("ServiceClaim = %+v, want addon=cache type=redis secretName=%s component='' (implicit fan-out)", c, wantSecret)
	}
}

func TestMapper_NoAddons_EmptyClaims(t *testing.T) {
	app := appWithAddons("hello") // no claims
	hv := MapToHelmValuesForEnv(app, "staging", domain.AppEnvStaging,
		"localhost", "", "", noNaming(), "", "", nil, nil, nil, nil)

	if len(hv.ServiceClaims) != 0 {
		t.Errorf("ServiceClaims should be empty, got %v", hv.ServiceClaims)
	}
	for _, s := range hv.Suparship.EnvFromSecrets {
		if s == "hello-addon-cache-conn" {
			t.Errorf("envFromSecrets unexpectedly contains addon Secret %q", s)
		}
	}
}

func TestMapper_UnresolvedAddon_Skipped(t *testing.T) {
	// App declares an addon, but no AddonProfile is configured. The
	// mapper drops the claim silently — domain-level Validate is
	// the gate, not the mapper.
	app := appWithAddons("hello",
		domain.AddonSpec{Name: "cache", Type: "redis"},
	)
	hv := MapToHelmValuesForEnv(app, "staging", domain.AppEnvStaging,
		"localhost", "", "", noNaming(), "", "", nil, nil, nil, nil)

	if len(hv.ServiceClaims) != 0 {
		t.Errorf("unresolved addon should produce no ServiceClaims, got %v", hv.ServiceClaims)
	}
}

func TestMapper_EnvAddonProfileWins(t *testing.T) {
	// App declares the same addon claim; org and env both configure
	// redis but with different providers. The env-level mapping is
	// applied, but for the consumer (app) chart only the resolved
	// Secret name matters and that derives from the app name + addon
	// name, not the provider. Confirm the claim still binds.
	app := appWithAddons("hello",
		domain.AddonSpec{Name: "cache", Type: "redis"},
	)
	org := domain.AddonProfiles{
		"redis": {Type: "redis", Provider: "valkey-operator", Chart: "valkey"},
	}
	env := domain.AddonProfiles{
		"redis": {Type: "redis", Provider: "crossplane-elasticache", Chart: "crossplane-redis"},
	}
	hv := MapToHelmValuesForEnv(app, "prod", domain.AppEnvProd,
		"localhost", "", "", noNaming(), "", "", nil, nil, org, env)

	if len(hv.ServiceClaims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(hv.ServiceClaims))
	}
	if hv.ServiceClaims[0].SecretName != "hello-addon-cache-conn" {
		t.Errorf("Secret name should not depend on provider, got %s", hv.ServiceClaims[0].SecretName)
	}
}

func TestMapper_MultipleAddons_SortedByName(t *testing.T) {
	app := appWithAddons("hello",
		domain.AddonSpec{Name: "queue", Type: "redis"},
		domain.AddonSpec{Name: "cache", Type: "redis"},
		domain.AddonSpec{Name: "primary-db", Type: "postgres"},
	)
	org := domain.AddonProfiles{
		"redis":    {Type: "redis", Provider: "valkey-operator", Chart: "valkey"},
		"postgres": {Type: "postgres", Provider: "cnpg", Chart: "cloudnative-pg"},
	}
	hv := MapToHelmValuesForEnv(app, "staging", domain.AppEnvStaging,
		"localhost", "", "", noNaming(), "", "", nil, nil, org, nil)

	if len(hv.ServiceClaims) != 3 {
		t.Fatalf("got %d claims, want 3", len(hv.ServiceClaims))
	}
	wantOrder := []string{"cache", "primary-db", "queue"}
	for i, w := range wantOrder {
		if hv.ServiceClaims[i].Addon != w {
			t.Errorf("claims[%d].Addon = %s, want %s (alphabetical)", i, hv.ServiceClaims[i].Addon, w)
		}
	}
}
