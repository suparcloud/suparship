package branding_test

import (
	"testing"

	"github.com/suparcloud/suparship/internal/branding"
)

func TestConfig_DefaultsApplyWhenEmpty(t *testing.T) {
	var c branding.Config
	if got := c.EffectiveName(); got != branding.DefaultName {
		t.Errorf("EffectiveName default = %q, want %q", got, branding.DefaultName)
	}
	if got := c.EffectiveLabelDomain(); got != branding.DefaultLabelDomain {
		t.Errorf("EffectiveLabelDomain default = %q, want %q", got, branding.DefaultLabelDomain)
	}
}

func TestConfig_ManagedByLabelsHonoursOverride(t *testing.T) {
	c := branding.Config{Name: "acme-platform", LabelDomain: "platform.acme.io"}
	got := c.ManagedByLabels()
	if got["app.kubernetes.io/managed-by"] != "acme-platform" {
		t.Errorf("managed-by = %q, want acme-platform", got["app.kubernetes.io/managed-by"])
	}
	if got["platform.acme.io/generator-version"] == "" {
		t.Errorf("expected generator-version under custom domain, got: %v", got)
	}
	// Each call returns a fresh map — callers mutate it.
	got2 := c.ManagedByLabels()
	got2["x"] = "y"
	if _, present := c.ManagedByLabels()["x"]; present {
		t.Error("ManagedByLabels returned a shared map; callers can corrupt subsequent calls")
	}
}

func TestConfig_SourceLabelEmptyPathIsEmpty(t *testing.T) {
	c := branding.Config{}
	got := c.SourceLabel("")
	if len(got) != 0 {
		t.Errorf("SourceLabel(\"\") = %v, want empty", got)
	}
}

func TestConfig_SourceLabelUsesDomain(t *testing.T) {
	c := branding.Config{LabelDomain: "platform.acme.io"}
	got := c.SourceLabel("/templates/web-service")
	if got["platform.acme.io/source"] != "/templates/web-service" {
		t.Errorf("source label missing or wrong: %v", got)
	}
}

func TestConfig_LabelKey(t *testing.T) {
	c := branding.Config{LabelDomain: "platform.acme.io"}
	if got := c.LabelKey("env"); got != "platform.acme.io/env" {
		t.Errorf("LabelKey(env) = %q", got)
	}
}

func TestMergeLabels_LaterWins(t *testing.T) {
	a := map[string]string{"k": "1", "x": "a"}
	b := map[string]string{"k": "2"}
	got := branding.MergeLabels(a, b)
	if got["k"] != "2" {
		t.Errorf("merge override failed: %v", got)
	}
	if got["x"] != "a" {
		t.Errorf("merge dropped non-conflicting key: %v", got)
	}
}
