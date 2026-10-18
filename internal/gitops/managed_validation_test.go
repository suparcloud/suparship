package gitops

import (
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/secrets"
	"path/filepath"
	"strings"
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

func TestManagedValidationPlatformDeletionKeepsSibling(t *testing.T) {
	root := t.TempDir()
	p, err := NewPublisher(PublisherConfig{RepoURL: "http://localhost/gitops.git", ArgoCDRepoURL: "http://localhost/gitops.git"})
	if err != nil {
		t.Fatal(err)
	}
	env := AppPublishEnv{EnvName: domain.ManagedValidationEnv, BaseEnv: "staging", Namespace: "demo-web-release-validation", Clusters: []ClusterTarget{{Name: "local", Server: "https://kubernetes.default.svc"}}}
	for _, name := range []string{"web", "sibling"} {
		app := &domain.App{Name: name, ProjectName: "demo"}
		if err := p.writeValidationPlatformApplications(root, app, env); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := p.unpublishAppFiles(root, "demo", "web")
	if err != nil || !removed {
		t.Fatalf("cleanup: %v %v", removed, err)
	}
	matches, err := filepath.Glob(filepath.Join(root, "_infra", "*platform.yaml"))
	if err != nil || len(matches) != 1 || !strings.Contains(matches[0], "sibling") {
		t.Fatalf("wrong cleanup: %v %v", matches, err)
	}
}
