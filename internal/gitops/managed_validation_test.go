package gitops

import (
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/secrets"
	"testing"
)

func TestManagedValidationInheritanceDoesNotCarryPins(t *testing.T) {
	a := &domain.App{Name: "voiceai-web", ProjectName: "voiceai"}
	a.Spec.CD = domain.CDConfig{ReleaseValidationManaged: true, ReleaseValidationEnvironment: domain.ManagedValidationEnv}
	a.Spec.EnvironmentDefaults = map[string]domain.EnvironmentOverride{
		"staging":                   {RawValues: map[string]any{"base": "yes", "override": "base"}, PinnedFrom: "pr-1", PinnedImageTag: "preview"},
		domain.ManagedValidationEnv: {RawValues: map[string]any{"override": "validation"}},
	}
	v := ValidationApp(a, "staging").Spec.EnvironmentDefaults[domain.ManagedValidationEnv]
	if v.RawValues["base"] != "yes" || v.RawValues["override"] != "validation" || v.PinnedFrom != "" || v.PinnedImageTag != "" {
		t.Fatalf("bad inheritance: %+v", v)
	}
	v.RawValues["base"] = "changed"
	if a.Spec.EnvironmentDefaults["staging"].RawValues["base"] != "yes" {
		t.Fatal("mutated base")
	}
	if validationPattern(a, domain.ManagedValidationEnv, secrets.DefaultArgoAppName) == secrets.DefaultArgoAppName {
		t.Fatal("validation identity collides")
	}
}
func TestValidationExternalSecretUsesBaseVaultAndIndependentOverrides(t *testing.T) {
	p := WorkloadExternalSecretParams{App: "voiceai-web", Project: "voiceai", Env: "staging", Namespace: "voiceai-web-release-validation", Validation: true, Presence: ScopePresence{EnvApp: true}, Backend: secrets.BackendVault}
	es := BuildAppExternalSecret(p)
	if len(es.Items) != 2 {
		t.Fatalf("items: %+v", es.Items)
	}
	want := "suparship-secrets-env-staging/voiceai-web-env-release-validation"
	if es.Items[1].Key != want || es.Namespace != p.Namespace {
		t.Fatalf("bad override: %+v", es)
	}
	if es.Items[0].Key == es.Items[1].Key {
		t.Fatal("overrides share base identity")
	}
}
