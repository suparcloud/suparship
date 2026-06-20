package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/domain"
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
