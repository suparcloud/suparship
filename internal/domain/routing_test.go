package domain

import (
	"strings"
	"testing"
)

func TestResolveRoutingProfile_Disabled(t *testing.T) {
	got, err := ResolveRoutingProfile(nil, nil, ExposeDisabled)
	if err != nil {
		t.Fatalf("disabled mode should not error: %v", err)
	}
	if got != (RoutingProfile{}) {
		t.Errorf("disabled mode should return zero RoutingProfile, got %+v", got)
	}
}

func TestResolveRoutingProfile_EmptyModeTreatedAsDisabled(t *testing.T) {
	got, err := ResolveRoutingProfile(
		RoutingProfiles{string(ExposeInternal): {IngressClassName: "nginx"}},
		nil,
		ExposeMode(""),
	)
	if err != nil {
		t.Fatalf("empty mode should not error: %v", err)
	}
	if got != (RoutingProfile{}) {
		t.Errorf("empty mode should return zero RoutingProfile, got %+v", got)
	}
}

func TestResolveRoutingProfile_OrgLookup(t *testing.T) {
	org := RoutingProfiles{
		string(ExposeInternal): {IngressClassName: "nginx-internal"},
		string(ExposeExternal): {IngressClassName: "nginx", ClusterIssuer: "letsencrypt-prod"},
	}
	got, err := ResolveRoutingProfile(org, nil, ExposeExternal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.IngressClassName != "nginx" {
		t.Errorf("class = %q, want nginx", got.IngressClassName)
	}
	if got.ClusterIssuer != "letsencrypt-prod" {
		t.Errorf("issuer = %q, want letsencrypt-prod", got.ClusterIssuer)
	}
}

func TestResolveRoutingProfile_EnvOverridesOrg(t *testing.T) {
	org := RoutingProfiles{
		string(ExposeExternal): {IngressClassName: "nginx", ClusterIssuer: "letsencrypt-prod"},
	}
	env := RoutingProfiles{
		string(ExposeExternal): {IngressClassName: "nginx-staging", ClusterIssuer: "letsencrypt-staging"},
	}
	got, err := ResolveRoutingProfile(org, env, ExposeExternal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ClusterIssuer != "letsencrypt-staging" {
		t.Errorf("env should win: issuer = %q, want letsencrypt-staging", got.ClusterIssuer)
	}
	if got.IngressClassName != "nginx-staging" {
		t.Errorf("env should win: class = %q, want nginx-staging", got.IngressClassName)
	}
}

func TestResolveRoutingProfile_EnvOverrideIsReplacementNotMerge(t *testing.T) {
	// Env entry omits ClusterIssuer; the merged result should also omit it,
	// not inherit from org. This documents the "replacement not merge" rule.
	org := RoutingProfiles{
		string(ExposeExternal): {IngressClassName: "nginx", ClusterIssuer: "letsencrypt-prod"},
	}
	env := RoutingProfiles{
		string(ExposeExternal): {IngressClassName: "nginx"},
	}
	got, err := ResolveRoutingProfile(org, env, ExposeExternal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ClusterIssuer != "" {
		t.Errorf("env entry should replace org entry; issuer = %q, want empty", got.ClusterIssuer)
	}
}

func TestResolveRoutingProfile_UnknownMode(t *testing.T) {
	org := RoutingProfiles{
		string(ExposeInternal): {IngressClassName: "nginx"},
	}
	_, err := ResolveRoutingProfile(org, nil, ExposeExternal)
	if err == nil {
		t.Fatal("expected error for missing profile, got nil")
	}
	if !strings.Contains(err.Error(), "external") {
		t.Errorf("error should mention the missing mode name, got: %v", err)
	}
}

func TestResolveRoutingProfile_InvalidMode(t *testing.T) {
	org := RoutingProfiles{
		"weird": {IngressClassName: "nginx"},
	}
	_, err := ResolveRoutingProfile(org, nil, ExposeMode("weird"))
	if err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
	if !strings.Contains(err.Error(), "invalid expose mode") {
		t.Errorf("error should flag invalid mode, got: %v", err)
	}
}

func TestResolveRoutingProfile_EmptyClassName(t *testing.T) {
	org := RoutingProfiles{
		string(ExposeInternal): {ClusterIssuer: "internal-ca"},
	}
	_, err := ResolveRoutingProfile(org, nil, ExposeInternal)
	if err == nil {
		t.Fatal("expected error for empty ingressClassName, got nil")
	}
	if !strings.Contains(err.Error(), "ingressClassName") {
		t.Errorf("error should mention ingressClassName, got: %v", err)
	}
}

func TestDefaultRoutingProfiles_HasInternalOnly(t *testing.T) {
	def := DefaultRoutingProfiles()
	if _, ok := def[string(ExposeInternal)]; !ok {
		t.Error("default should include internal profile")
	}
	if _, ok := def[string(ExposeExternal)]; ok {
		t.Error("default should NOT include external profile (org opts in)")
	}
	if got := def[string(ExposeInternal)].IngressClassName; got != "nginx" {
		t.Errorf("default internal class = %q, want nginx", got)
	}
	if got := def[string(ExposeInternal)].ClusterIssuer; got != "" {
		t.Errorf("default internal should have no TLS, got issuer %q", got)
	}
}
