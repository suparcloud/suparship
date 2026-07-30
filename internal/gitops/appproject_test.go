package gitops

import (
	"reflect"
	"testing"
)

// BuildArgoAppProject must canonicalize spec.destinations: dedup identical
// entries and sort deterministically, so the same set of clusters written in
// any caller order (k8s-list order on a per-app sync vs. config-file order on a
// startup infra republish) produces byte-identical output — no reorder-only
// churn commits, and no duplicate entry when a cluster is shared across envs.
func TestBuildArgoAppProject_DestinationsSortedAndDeduped(t *testing.T) {
	az := AppProjectDestination{Server: "https://prod-aks.example:443", Namespace: "*"}
	eks := AppProjectDestination{Server: "https://eks.example:443", Namespace: "*"}
	stg := AppProjectDestination{Server: "https://staging-aks.example:443", Namespace: "*"}

	// Two callers, different orders, and az/eks duplicated (shared cluster).
	orderA := []AppProjectDestination{az, eks, stg, eks}
	orderB := []AppProjectDestination{stg, eks, az, az}

	want := []AppProjectDestination{eks, az, stg} // sorted by Server asc

	got := BuildArgoAppProject("voiceai", AppProjectOptions{Destinations: orderA}).Spec.Destinations
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orderA destinations = %+v, want sorted+deduped %+v", got, want)
	}

	// Determinism: a different input order yields identical output.
	got2 := BuildArgoAppProject("voiceai", AppProjectOptions{Destinations: orderB}).Spec.Destinations
	if !reflect.DeepEqual(got, got2) {
		t.Errorf("output depends on input order:\n orderA=%+v\n orderB=%+v", got, got2)
	}
}

// A destination with an empty namespace inherits NamespaceGlob before dedup, so
// it doesn't spuriously differ from an explicit "*" entry for the same server.
func TestBuildArgoAppProject_EmptyNamespaceInheritsGlobThenDedups(t *testing.T) {
	server := "https://c1.example:443"
	got := BuildArgoAppProject("voiceai", AppProjectOptions{
		NamespaceGlob: "*",
		Destinations: []AppProjectDestination{
			{Server: server, Namespace: ""},  // inherits "*"
			{Server: server, Namespace: "*"}, // duplicate after inheritance
		},
	}).Spec.Destinations
	want := []AppProjectDestination{{Server: server, Namespace: "*"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("destinations = %+v, want single deduped %+v", got, want)
	}
}
