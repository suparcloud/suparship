package license

import (
	"reflect"
	"testing"
)

func TestCommunity(t *testing.T) {
	var v Validator = Community{}
	if v.Edition() != EditionCommunity {
		t.Errorf("edition = %q, want %q", v.Edition(), EditionCommunity)
	}
	if v.Has("saml") {
		t.Error("community edition should not entitle any feature")
	}
	if len(v.Features()) != 0 {
		t.Errorf("community features = %v, want empty", v.Features())
	}
}

func TestStatic(t *testing.T) {
	v := NewStatic(EditionEnterprise, "saml", "audit-siem")
	if v.Edition() != EditionEnterprise {
		t.Errorf("edition = %q, want %q", v.Edition(), EditionEnterprise)
	}
	if !v.Has("saml") || !v.Has("audit-siem") {
		t.Error("expected entitled features to report Has() == true")
	}
	if v.Has("scim") {
		t.Error("unentitled feature should report Has() == false")
	}
	// Features are returned sorted for stable API output.
	if got := v.Features(); !reflect.DeepEqual(got, []Feature{"audit-siem", "saml"}) {
		t.Errorf("features = %v, want [audit-siem saml]", got)
	}
}

func TestStaticEmptyEditionDefaultsCommunity(t *testing.T) {
	if got := NewStatic("").Edition(); got != EditionCommunity {
		t.Errorf("empty edition = %q, want %q", got, EditionCommunity)
	}
}

func TestResolve(t *testing.T) {
	if _, ok := Resolve(nil).(Community); !ok {
		t.Error("Resolve(nil) should return Community")
	}
	s := NewStatic(EditionEnterprise, "saml")
	if got := Resolve(s); got.Edition() != EditionEnterprise {
		t.Errorf("Resolve(static) edition = %q, want enterprise", got.Edition())
	}
}
