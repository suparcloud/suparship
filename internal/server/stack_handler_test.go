package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
)

// stackToDTO must never produce a null Apps slice — the UI maps over it, and a
// JSON null crashes the project/stack pages for a stack with no members.
func TestStackToDTO_AppsNeverNil(t *testing.T) {
	dto := stackToDTO(&domain.Stack{Name: "voiceai", ProjectName: "p"}, nil)
	if dto.Apps == nil {
		t.Fatal("Apps must be a non-nil slice")
	}
	b, _ := json.Marshal(dto)
	if !strings.Contains(string(b), `"apps":[]`) {
		t.Errorf("expected apps to marshal as [], got %s", b)
	}
}

// Stack per-environment variables round-trip through the DTO conversions and are
// kept separate from the global stack variables.
func TestStackEnvConfigByEnv_RoundTrip(t *testing.T) {
	spec := domain.StackSpec{
		EnvConfig: envconfig.EnvConfig{Vars: map[string]string{"GLOBAL": "g"}},
		EnvConfigByEnv: map[string]envconfig.EnvConfig{
			"staging": {Vars: map[string]string{"VALKEY_URL": "redis://staging"}},
		},
	}
	dto := stackToDTO(&domain.Stack{Name: "lk-sh", ProjectName: "voiceai", Spec: spec}, nil)
	if got := dto.EnvConfigByEnv["staging"].Vars["VALKEY_URL"]; got != "redis://staging" {
		t.Fatalf("staging var = %q, want redis://staging", got)
	}
	if _, leaked := dto.EnvConfig.Vars["VALKEY_URL"]; leaked {
		t.Errorf("per-env var leaked into global stack variables")
	}

	// Round-trip back through fromDTO.
	back := envConfigByEnvFromDTO(dto.EnvConfigByEnv)
	if back["staging"].Vars["VALKEY_URL"] != "redis://staging" {
		t.Errorf("round-trip lost staging var: %+v", back)
	}
	if envConfigByEnvToDTO(nil) != nil {
		t.Errorf("empty map should convert to nil (omitted from JSON)")
	}
}
