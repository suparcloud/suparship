package onepassword

import (
	"encoding/json"
	"testing"
)

// TestActiveItemsFilter_NonEmpty guards the workaround for the v0.4.0 WASM core
// crash: Items().List must be called with an explicit ByState filter, never the
// empty no-filter form that traps in load_input. The filter must marshal to a
// concrete ByState entry (active items), not an empty array.
func TestActiveItemsFilter_NonEmpty(t *testing.T) {
	b, err := json.Marshal(activeItemsFilter())
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}
	got := string(b)
	want := `{"type":"ByState","content":{"active":true,"archived":false}}`
	if got != want {
		t.Errorf("activeItemsFilter() marshaled to %s, want %s", got, want)
	}
}
