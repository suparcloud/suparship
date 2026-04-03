package domain

import (
	"testing"
)

func TestParseAppEnvironmentType(t *testing.T) {
	tests := []struct {
		input   string
		want    AppEnvironmentType
		wantErr bool
	}{
		{input: "staging", want: AppEnvStaging},
		{input: "prod", want: AppEnvProd},
		{input: "preview", want: AppEnvPreview},
		{input: "", wantErr: true},
		{input: "dev", wantErr: true},
		{input: "STAGING", wantErr: true},
		{input: "production", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseAppEnvironmentType(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseAppEnvironmentType(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseAppEnvironmentType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAppEnvironmentTypeValid(t *testing.T) {
	tests := []struct {
		input AppEnvironmentType
		want  bool
	}{
		{AppEnvStaging, true},
		{AppEnvProd, true},
		{AppEnvPreview, true},
		{"", false},
		{"dev", false},
		{"STAGING", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("AppEnvironmentType(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseComponentType(t *testing.T) {
	tests := []struct {
		input   string
		want    ComponentType
		wantErr bool
	}{
		{input: "web", want: ComponentWeb},
		{input: "worker", want: ComponentWorker},
		{input: "cron", want: ComponentCron},
		{input: "", wantErr: true},
		{input: "gateway", wantErr: true},
		{input: "WEB", wantErr: true},
		{input: "scheduler", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseComponentType(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseComponentType(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseComponentType(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestComponentTypeValid(t *testing.T) {
	tests := []struct {
		input ComponentType
		want  bool
	}{
		{ComponentWeb, true},
		{ComponentWorker, true},
		{ComponentCron, true},
		{"", false},
		{"job", false},
		{"WEB", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("ComponentType(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestAppEnvironmentTypeErrorMessage(t *testing.T) {
	_, err := ParseAppEnvironmentType("bogus")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	for _, keyword := range []string{"bogus", "staging", "prod", "preview"} {
		if !contains(msg, keyword) {
			t.Errorf("error message %q missing keyword %q", msg, keyword)
		}
	}
}

func TestComponentTypeErrorMessage(t *testing.T) {
	_, err := ParseComponentType("bogus")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	for _, keyword := range []string{"bogus", "web", "worker", "cron"} {
		if !contains(msg, keyword) {
			t.Errorf("error message %q missing keyword %q", msg, keyword)
		}
	}
}

// contains is a small helper so the test file has no extra imports.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
