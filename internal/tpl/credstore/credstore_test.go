package credstore

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildSecretData(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		token    string
		username string
		password string
		want     map[string]string
		wantErr  error
	}{
		{name: "github with token", provider: "github", token: "ghp_x", want: map[string]string{"token": "ghp_x"}},
		{name: "gitlab with token", provider: "gitlab", token: "glpat", want: map[string]string{"token": "glpat"}},
		{name: "gitea with token", provider: "gitea", token: "tok", want: map[string]string{"token": "tok"}},
		{name: "github missing token", provider: "github", wantErr: ErrNoCredentials},
		{name: "bitbucket app password", provider: "bitbucket", username: "u", password: "p", want: map[string]string{"username": "u", "password": "p"}},
		{name: "generic with username only", provider: "generic", username: "u", want: map[string]string{"username": "u"}},
		{name: "generic with password only", provider: "generic", password: "p", want: map[string]string{"password": "p"}},
		{name: "empty provider treated as generic", provider: "", username: "u", password: "p", want: map[string]string{"username": "u", "password": "p"}},
		{name: "generic with no creds", provider: "generic", wantErr: ErrNoCredentials},
		{name: "unsupported provider", provider: "subversion", wantErr: nil /* checked by error message */},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildSecretData(tc.provider, tc.token, tc.username, tc.password)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want is %v", err, tc.wantErr)
				}
				return
			}
			if tc.name == "unsupported provider" {
				if err == nil || !strings.Contains(err.Error(), "unsupported provider") {
					t.Fatalf("expected unsupported provider error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("got[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestSecretNameFor(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"company-templates", "suparship-tpl-credentials-company-templates"},
		{"Company Templates", "suparship-tpl-credentials-company-templates"},
		{"acme/internal_tpl", "suparship-tpl-credentials-acme-internal-tpl"},
		{"---trim---", "suparship-tpl-credentials-trim"},
		{"", "suparship-tpl-credentials-unnamed"},
		{"UPPER", "suparship-tpl-credentials-upper"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := SecretNameFor(tc.in); got != tc.want {
				t.Errorf("SecretNameFor(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeNameTruncation(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := sanitizeName(long)
	if len(got) > 200 {
		t.Errorf("sanitizeName output exceeds 200 chars: %d", len(got))
	}
	if !strings.HasPrefix(strings.Repeat("a", 200), got) {
		t.Errorf("output not a prefix of all-a 200: %q", got)
	}
}
