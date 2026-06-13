package helmvalues

import (
	"encoding/json"
	"testing"
)

func TestSchema_TopLevelStructure(t *testing.T) {
	s := Schema()

	if got, want := s["$schema"], "http://json-schema.org/draft-07/schema#"; got != want {
		t.Errorf("$schema = %v, want %v", got, want)
	}
	if got := s["x-suparship-schema-version"]; got != SchemaVersion {
		t.Errorf("x-suparship-schema-version = %v, want %v", got, SchemaVersion)
	}
	if got := s["additionalProperties"]; got != true {
		t.Errorf("root additionalProperties = %v, want true (chart extensions must be tolerated)", got)
	}

	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing or wrong type: %T", s["properties"])
	}
	for _, key := range []string{"app", "platform", "components", "routing", "suparship"} {
		if _, ok := props[key]; !ok {
			t.Errorf("missing top-level property %q", key)
		}
	}
}

func TestSchema_Platform_HasIdentityAndRoutingFields(t *testing.T) {
	s := Schema()
	platform, ok := s["properties"].(map[string]any)["platform"].(map[string]any)
	if !ok {
		t.Fatalf("platform property missing or wrong type")
	}
	props := platform["properties"].(map[string]any)
	for _, k := range []string{"org", "project", "app", "env", "envType", "namespace", "baseDomain", "routingHost"} {
		f, ok := props[k].(map[string]any)
		if !ok {
			t.Errorf("platform.%s missing", k)
			continue
		}
		if f["type"] != "string" {
			t.Errorf("platform.%s type = %v, want string", k, f["type"])
		}
	}
}

func TestSchema_AppContext_RequiresNameAndEnv(t *testing.T) {
	s := Schema()
	app := s["properties"].(map[string]any)["app"].(map[string]any)

	required, _ := app["required"].([]string)
	mustContain(t, required, "name", "env")

	props := app["properties"].(map[string]any)
	for _, k := range []string{"name", "env"} {
		got := props[k].(map[string]any)["type"]
		if got != "string" {
			t.Errorf("app.%s type = %v, want string", k, got)
		}
	}
}

func TestSchema_Components_AllowsAnyKey_TypedValue(t *testing.T) {
	s := Schema()
	comps := s["properties"].(map[string]any)["components"].(map[string]any)

	if got := comps["type"]; got != "object" {
		t.Errorf("components.type = %v, want object", got)
	}
	addProps, ok := comps["additionalProperties"].(map[string]any)
	if !ok {
		t.Fatalf("components.additionalProperties is not an object schema: %T", comps["additionalProperties"])
	}
	cprops, ok := addProps["properties"].(map[string]any)
	if !ok {
		t.Fatalf("ComponentValues schema missing properties")
	}
	// Spot-check that ComponentValues fields appear with expected types.
	for field, want := range map[string]string{
		"enabled":  "boolean",
		"replicas": "integer",
	} {
		got := cprops[field].(map[string]any)["type"]
		if got != want {
			t.Errorf("components.<>.%s type = %v, want %v", field, got, want)
		}
	}
}

func TestSchema_OmitemptyFields_NotRequired(t *testing.T) {
	s := Schema()
	comps := s["properties"].(map[string]any)["components"].(map[string]any)
	cv := comps["additionalProperties"].(map[string]any)

	required, _ := cv["required"].([]string)
	for _, k := range []string{"port", "healthCheck", "ingress", "env", "resources"} {
		for _, r := range required {
			if r == k {
				t.Errorf("optional field %q wrongly listed in required", k)
			}
		}
	}
	// Sanity: enabled is required (no omitempty).
	mustContain(t, required, "enabled", "image", "replicas")
}

func TestSchema_Marshallable(t *testing.T) {
	// Generator output must round-trip through JSON without error.
	data, err := json.Marshal(Schema())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
}

func TestSchema_SuparshipBlock_ListsOfStrings(t *testing.T) {
	s := Schema()
	sup := s["properties"].(map[string]any)["suparship"].(map[string]any)
	props := sup["properties"].(map[string]any)
	for _, k := range []string{"envFromConfigMaps", "envFromSecrets"} {
		f := props[k].(map[string]any)
		if f["type"] != "array" {
			t.Errorf("suparship.%s.type = %v, want array", k, f["type"])
		}
		items := f["items"].(map[string]any)
		if items["type"] != "string" {
			t.Errorf("suparship.%s.items.type = %v, want string", k, items["type"])
		}
	}
}

func mustContain(t *testing.T, slice []string, items ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, s := range slice {
		set[s] = true
	}
	for _, w := range items {
		if !set[w] {
			t.Errorf("required slice missing %q (got %v)", w, slice)
		}
	}
}
