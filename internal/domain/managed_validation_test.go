package domain

import (
	"strings"
	"testing"
)

func TestValidationIdentity(t *testing.T) {
	for _, tc := range []struct{ project, app, want string }{
		{"voiceai", "voiceai-lk-sh", "voiceai-lk-sh-release-validation"},
		{"demo", "web", "demo-web-release-validation"},
		{"sales", "sales", "sales-release-validation"},
	} {
		if got := ValidationIdentity(tc.project, tc.app); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
	a, b := ValidationIdentity("demo", strings.Repeat("a", 70)), ValidationIdentity("demo", strings.Repeat("a", 69)+"b")
	if len(a) > 63 || a == b {
		t.Fatal("truncated identity is unsafe")
	}
}
