package rbac

import (
	"strings"
	"testing"
)

// orgYAML wraps environment YAML in the minimum valid Org document.
func orgYAML(envs string) []byte {
	return []byte("name: default\ndisplayName: Default\nenvironments:\n" + envs)
}

// TestParseOrg_SingularClusterRefBinds is the regression guard for the bug where
// config/seed/org.yaml and the Helm chart both wrote `clusterRef:` (singular)
// while OrgEnvironment only ever parsed `clusterRefs`. yaml.Unmarshal is
// non-strict, so the key was dropped without a word and EVERY environment loaded
// unbound — in local dev and in any install that configured environments through
// Helm values. Nothing failed loudly; deploys just had nowhere to go.
func TestParseOrg_SingularClusterRefBinds(t *testing.T) {
	org, err := ParseOrg(orgYAML(
		"  - name: staging\n    order: 1\n    clusterRef: staging-cluster\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	env := org.Environments[0]
	if got := env.EffectiveClusterRef(); got != "staging-cluster" {
		t.Errorf("EffectiveClusterRef() = %q, want staging-cluster (env parsed UNBOUND)", got)
	}
	if len(env.ClusterRefs) != 1 || env.ClusterRefs[0] != "staging-cluster" {
		t.Errorf("ClusterRefs = %v, want [staging-cluster]", env.ClusterRefs)
	}
	if env.ActiveClusterRef != "staging-cluster" {
		t.Errorf("ActiveClusterRef = %q, want staging-cluster", env.ActiveClusterRef)
	}
	// The deprecated field must be cleared so a re-marshal writes only the
	// modern keys — that is what makes an old config self-heal once rewritten.
	if env.ClusterRef != "" {
		t.Errorf("ClusterRef = %q, want cleared after folding", env.ClusterRef)
	}
	out, err := org.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "clusterRef:") {
		t.Errorf("re-marshal still emits the deprecated singular key:\n%s", out)
	}
	if !strings.Contains(string(out), "clusterRefs:") {
		t.Errorf("re-marshal lost the binding:\n%s", out)
	}
}

// TestParseOrg_PluralWinsOverSingular pins that we never second-guess a config
// already written in the modern form, even if a stale singular key lingers.
func TestParseOrg_PluralWinsOverSingular(t *testing.T) {
	org, err := ParseOrg(orgYAML(
		"  - name: prod\n    order: 1\n    clusterRef: old\n" +
			"    clusterRefs: [a, b]\n    activeClusterRef: b\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	env := org.Environments[0]
	if len(env.ClusterRefs) != 2 || env.ClusterRefs[0] != "a" || env.ClusterRefs[1] != "b" {
		t.Errorf("ClusterRefs = %v, want [a b] — the singular alias must not win", env.ClusterRefs)
	}
	if env.ActiveClusterRef != "b" {
		t.Errorf("ActiveClusterRef = %q, want b (explicit value must survive)", env.ActiveClusterRef)
	}
}

// TestParseOrg_PluralStillParses guards the ordinary modern path.
func TestParseOrg_PluralStillParses(t *testing.T) {
	org, err := ParseOrg(orgYAML(
		"  - name: prod\n    order: 1\n    clusterRefs: [x]\n    deployMode: all\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	env := org.Environments[0]
	if got := env.EffectiveClusterRef(); got != "x" {
		t.Errorf("EffectiveClusterRef() = %q, want x", got)
	}
	if got := env.ResolveDeployTargets(); len(got) != 1 || got[0] != "x" {
		t.Errorf("ResolveDeployTargets() = %v, want [x]", got)
	}
}

// TestParseOrg_UnboundEnvIsDetectable documents the legitimate unbound case: an
// env with no cluster at all still parses (you can define the pipeline before
// registering clusters), but reports itself unbound rather than looking fine.
func TestParseOrg_UnboundEnvIsDetectable(t *testing.T) {
	org, err := ParseOrg(orgYAML("  - name: staging\n    order: 1\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	env := org.Environments[0]
	if got := env.EffectiveClusterRef(); got != "" {
		t.Errorf("EffectiveClusterRef() = %q, want empty for an unbound env", got)
	}
	if got := env.ResolveDeployTargets(); len(got) != 0 {
		t.Errorf("ResolveDeployTargets() = %v, want none for an unbound env", got)
	}
}

// TestParseOrg_SingularAliasSatisfiesFanOutValidation checks the folded value is
// visible to Validate, not just to readers afterwards — deployMode: all requires
// at least one clusterRef, so folding has to happen before validation runs.
func TestParseOrg_SingularAliasSatisfiesFanOutValidation(t *testing.T) {
	if _, err := ParseOrg(orgYAML(
		"  - name: prod\n    order: 1\n    clusterRef: only\n    deployMode: all\n")); err != nil {
		t.Fatalf("singular alias should satisfy deployMode=all validation, got: %v", err)
	}
}
