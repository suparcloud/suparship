package rbac

import (
	"testing"

	"github.com/suparcloud/suparship/internal/version"
)

func TestCheckSchema(t *testing.T) {
	cur := version.Schema // "1" at time of writing

	tests := []struct {
		name   string
		stored string
		want   SchemaCheck
	}{
		{"current", cur, SchemaCurrent},
		{"unversioned", "", SchemaUnversioned},
		{"older", "0", SchemaUpgrade},
		{"newer", "999", SchemaDowngrade},
		{"non-integer differs", "weird", SchemaUpgrade},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, msg := CheckSchema(&Org{SchemaVersion: tt.stored})
			if got != tt.want {
				t.Errorf("CheckSchema(%q) = %v, want %v", tt.stored, got, tt.want)
			}
			if tt.want != SchemaCurrent && msg == "" {
				t.Error("expected a non-empty guidance message")
			}
			if tt.want == SchemaCurrent && msg != "" {
				t.Errorf("expected empty message when current, got %q", msg)
			}
		})
	}
}

func TestCheckSchema_NilOrg(t *testing.T) {
	if got, _ := CheckSchema(nil); got != SchemaCurrent {
		t.Errorf("nil org = %v, want SchemaCurrent", got)
	}
}
