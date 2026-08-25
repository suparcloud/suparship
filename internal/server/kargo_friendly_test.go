package server

import (
	"strings"
	"testing"
)

func TestFriendlyKargoIssue(t *testing.T) {
	cases := []struct {
		raw     string
		wantSub string
		wantRaw bool
	}{
		{"", "", false},
		{"source stage has no current freight to promote", "No release has flowed through the CD pipeline yet", false},
		{"Argo CD integration is disabled on this controller", "Argo CD integration is disabled", true},
		{"Get \"https://kind-registry:5000/v2/\": http: server gave HTTP response to HTTPS client", "reached securely (TLS)", true},
		{"refused to get credentials for insecure HTTP endpoint", "authenticate to the git server", true},
		{"something entirely novel exploded", "reported an error", true},
	}
	for _, c := range cases {
		got := friendlyKargoIssue(c.raw)
		if c.wantSub == "" {
			if got != "" {
				t.Errorf("friendlyKargoIssue(%q) = %q, want empty", c.raw, got)
			}
			continue
		}
		if !strings.Contains(got, c.wantSub) {
			t.Errorf("friendlyKargoIssue(%q) = %q, want substring %q", c.raw, got, c.wantSub)
		}
		if c.wantRaw && !strings.Contains(got, c.raw) {
			t.Errorf("friendlyKargoIssue(%q) should append the raw detail, got %q", c.raw, got)
		}
	}
}

func TestBenignCDCondition(t *testing.T) {
	benign := []string{
		"Stage has no current Freight",
		"Stage health evaluated to Unknown",
		"Stage is currently being promoted",
		"Promotion is running",
	}
	for _, b := range benign {
		if !benignCDCondition(b) {
			t.Errorf("benignCDCondition(%q) = false, want true", b)
		}
	}
	real := []string{
		"step \"step-6\" met error threshold of 1: Argo CD integration is disabled on this controller",
		"error discovering artifacts: server gave HTTP response to HTTPS client",
	}
	for _, r := range real {
		if benignCDCondition(r) {
			t.Errorf("benignCDCondition(%q) = true, want false (a real problem)", r)
		}
	}
}

func TestSummarizeCDPipeline(t *testing.T) {
	ok := func(env, freight string) KargoStageStatusDTO {
		health := ""
		if freight != "" {
			health = "Healthy"
		}
		return KargoStageStatusDTO{EnvName: env, CurrentFreight: freight, Health: health}
	}
	wh := &KargoWarehouseDTO{Exists: true, Ready: true}

	if got := summarizeCDPipeline(wh, []KargoStageStatusDTO{ok("staging", "f1"), ok("prod", "")}); got.State != "active" {
		t.Errorf("staging has freight, prod pre-first-promotion: want active, got %+v", got)
	}
	if got := summarizeCDPipeline(wh, []KargoStageStatusDTO{ok("staging", ""), ok("prod", "")}); got.State != "setting_up" {
		t.Errorf("no freight anywhere: want setting_up, got %+v", got)
	}
	if got := summarizeCDPipeline(&KargoWarehouseDTO{Exists: false}, nil); got.State != "setting_up" {
		t.Errorf("warehouse absent: want setting_up, got %+v", got)
	}
	broken := KargoStageStatusDTO{EnvName: "staging", Issue: "The CD controller cannot update deployments"}
	if got := summarizeCDPipeline(wh, []KargoStageStatusDTO{broken}); got.State != "attention" || got.Message == "" {
		t.Errorf("stage issue: want attention+message, got %+v", got)
	}
	whBad := &KargoWarehouseDTO{Exists: true, Ready: false, Issue: "registry unreachable"}
	if got := summarizeCDPipeline(whBad, []KargoStageStatusDTO{ok("staging", "f1")}); got.State != "attention" {
		t.Errorf("warehouse issue: want attention, got %+v", got)
	}
	if got := summarizeCDPipeline(nil, []KargoStageStatusDTO{ok("staging", "f1")}); got.State != "active" {
		t.Errorf("nil warehouse reader, freight flowing: want active, got %+v", got)
	}

	// A running promotion, or freshly promoted freight whose health is still
	// Unknown while ArgoCD syncs, reads as encouraging progress — never as a
	// problem, and not as generic setting_up either.
	promoting := KargoStageStatusDTO{EnvName: "staging", Phase: "Promoting"}
	if got := summarizeCDPipeline(wh, []KargoStageStatusDTO{promoting}); got.State != "deploying" {
		t.Errorf("promotion in flight: want deploying, got %+v", got)
	}
	// An in-flight promotion SUPPRESSES issue states — mid-flight pipeline
	// errors are judged only after the sync settles.
	promotingWithIssue := KargoStageStatusDTO{EnvName: "staging", Phase: "Promoting", Issue: "step failed (stale)"}
	if got := summarizeCDPipeline(whBad, []KargoStageStatusDTO{promotingWithIssue}); got.State != "deploying" {
		t.Errorf("promotion in flight with issues: want deploying (suppressed), got %+v", got)
	}
	syncing := KargoStageStatusDTO{EnvName: "staging", CurrentFreight: "f1", Health: "Unknown"}
	if got := summarizeCDPipeline(wh, []KargoStageStatusDTO{syncing}); got.State != "deploying" {
		t.Errorf("freight present, health not yet assessed: want deploying, got %+v", got)
	}
}
