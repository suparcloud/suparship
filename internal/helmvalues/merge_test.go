package helmvalues

import "testing"

func TestDeepMerge_NestedMergeAndReplace(t *testing.T) {
	base := map[string]any{
		"a":   map[string]any{"x": 1, "y": 2},
		"b":   "keep",
		"arr": []any{1, 2},
	}
	overlay := map[string]any{
		"a":   map[string]any{"y": 20, "z": 3}, // merge: x kept, y replaced, z added
		"arr": []any{9},                        // slice replaced wholesale
		"c":   "new",
	}
	got := DeepMerge(base, overlay)
	a := got["a"].(map[string]any)
	if a["x"] != 1 || a["y"] != 20 || a["z"] != 3 {
		t.Errorf("nested merge wrong: %v", a)
	}
	if got["b"] != "keep" || got["c"] != "new" {
		t.Errorf("top-level merge wrong: %v", got)
	}
	if arr := got["arr"].([]any); len(arr) != 1 || arr[0] != 9 {
		t.Errorf("slice should be replaced, got %v", arr)
	}
}

func TestDeepMerge_NilBaseInitialized(t *testing.T) {
	got := DeepMerge(nil, map[string]any{"a": 1})
	if got == nil || got["a"] != 1 {
		t.Fatalf("nil base should be initialized and merged, got %v", got)
	}
}

func TestDeepCopyMap_IsolatesNestedMutation(t *testing.T) {
	src := map[string]any{
		"a":   map[string]any{"x": 1},
		"arr": []any{map[string]any{"k": "v"}},
	}
	cp := DeepCopyMap(src)

	// Mutating the copy must not touch the source.
	cp["a"].(map[string]any)["x"] = 99
	cp["arr"].([]any)[0].(map[string]any)["k"] = "changed"

	if src["a"].(map[string]any)["x"] != 1 {
		t.Error("nested map not deep-copied")
	}
	if src["arr"].([]any)[0].(map[string]any)["k"] != "v" {
		t.Error("slice element not deep-copied")
	}
}

func TestDeepCopyMap_Nil(t *testing.T) {
	if DeepCopyMap(nil) != nil {
		t.Error("DeepCopyMap(nil) should return nil")
	}
}
