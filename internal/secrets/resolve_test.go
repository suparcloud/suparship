package secrets

import (
	"sort"
	"testing"
)

func TestResolveSecretLayers_Empty(t *testing.T) {
	resolved := ResolveSecretLayers(nil, nil, nil, nil, nil)
	if len(resolved) != 0 {
		t.Errorf("expected empty resolved, got %d entries", len(resolved))
	}
}

func TestResolveSecretLayers_SingleLevel(t *testing.T) {
	resolved := ResolveSecretLayers(
		[]string{"DB_URL"},
		nil, nil, nil, nil,
	)
	if len(resolved) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(resolved))
	}
	if resolved["DB_URL"].Source != LevelOrg {
		t.Errorf("DB_URL source = %q, want %q", resolved["DB_URL"].Source, LevelOrg)
	}
}

func TestResolveSecretLayers_LastWins(t *testing.T) {
	resolved := ResolveSecretLayers(
		[]string{"DB_URL", "SHARED"},
		[]string{"SHARED"},
		[]string{"DB_URL", "PROJ_KEY"},
		[]string{"APP_KEY"},
		[]string{"DB_URL", "APP_ENV_KEY"},
	)

	tests := []struct {
		key    string
		source string
	}{
		{"DB_URL", LevelAppEnv},
		{"SHARED", LevelEnvironment},
		{"PROJ_KEY", LevelProject},
		{"APP_KEY", LevelApp},
		{"APP_ENV_KEY", LevelAppEnv},
	}
	for _, tt := range tests {
		r, ok := resolved[tt.key]
		if !ok {
			t.Errorf("key %q not found in resolved", tt.key)
			continue
		}
		if r.Source != tt.source {
			t.Errorf("key %q source = %q, want %q", tt.key, r.Source, tt.source)
		}
	}
	if len(resolved) != 5 {
		t.Errorf("expected 5 entries, got %d", len(resolved))
	}
}

func TestResolveSecretLayers_OverridePrecedence(t *testing.T) {
	// Same key at all 5 levels — app-env must win.
	resolved := ResolveSecretLayers(
		[]string{"KEY"},
		[]string{"KEY"},
		[]string{"KEY"},
		[]string{"KEY"},
		[]string{"KEY"},
	)
	if resolved["KEY"].Source != LevelAppEnv {
		t.Errorf("KEY source = %q, want %q", resolved["KEY"].Source, LevelAppEnv)
	}
}

func TestResolveSecretLayers_KeyFieldSet(t *testing.T) {
	resolved := ResolveSecretLayers(
		[]string{"A"}, nil, nil, nil, []string{"B"},
	)
	if resolved["A"].Key != "A" {
		t.Errorf("expected Key field to be set to %q, got %q", "A", resolved["A"].Key)
	}
	if resolved["B"].Key != "B" {
		t.Errorf("expected Key field to be set to %q, got %q", "B", resolved["B"].Key)
	}
}

func TestResolveSecretLayers_Deterministic(t *testing.T) {
	for i := 0; i < 5; i++ {
		resolved := ResolveSecretLayers(
			[]string{"Z", "A", "M"},
			[]string{"B"},
			nil,
			[]string{"A"},
			[]string{"Z"},
		)
		keys := make([]string, 0, len(resolved))
		for k := range resolved {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		expected := []string{"A", "B", "M", "Z"}
		if len(keys) != len(expected) {
			t.Fatalf("run %d: expected %d keys, got %d", i, len(expected), len(keys))
		}
		for j, k := range keys {
			if k != expected[j] {
				t.Errorf("run %d: key[%d] = %q, want %q", i, j, k, expected[j])
			}
		}
	}
}
