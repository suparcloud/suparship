package helmvalues

import (
	"reflect"
	"slices"
	"sort"
	"strings"
)

// SchemaVersion is the canonical-values schema version. Bumped when the
// HelmValues struct changes in a backwards-incompatible way. Charts MAY
// declare their target schema version in Chart.yaml annotations under
// `suparship.io/values-schema-version` so the publisher can negotiate
// compat shims when the schema evolves.
const SchemaVersion = "v1"

// Schema returns the JSON Schema (draft-07) describing the canonical
// HelmValues document. The schema validates types of *known* fields but
// permits additional properties everywhere — chart authors layer their
// own values (autoscaling, pdb, gateway, …) onto the canonical shape,
// and the publisher does not strip them, so additional keys are
// expected at every level.
//
// Returns a value suitable for marshalling to JSON or YAML.
func Schema() map[string]any {
	root := schemaForType(reflect.TypeOf(HelmValues{}))
	root["$schema"] = "http://json-schema.org/draft-07/schema#"
	root["title"] = "suparship canonical Helm values"
	root["description"] = "Generated from internal/helmvalues/values.go. Validates the publisher-injected canonical shape. " +
		"Chart-specific extension fields (autoscaling, pdb, gateway, etc.) are tolerated via additionalProperties."
	root["x-suparship-schema-version"] = SchemaVersion
	return root
}

// schemaForType builds a schema fragment for a single Go type. Recurses
// into nested struct / pointer / map / slice types.
func schemaForType(t reflect.Type) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return schemaForStruct(t)
	case reflect.Map:
		// Only string-keyed maps are supported (everything we serialize is
		// JSON-friendly).
		valSchema := schemaForType(t.Elem())
		return map[string]any{
			"type":                 "object",
			"additionalProperties": valSchema,
		}
	case reflect.Slice, reflect.Array:
		return map[string]any{
			"type":  "array",
			"items": schemaForType(t.Elem()),
		}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Interface:
		// Untyped fall-through; allow anything.
		return map[string]any{}
	default:
		// Unknown kind — permissive fallback.
		return map[string]any{}
	}
}

// schemaForStruct builds an object schema from an exported struct. Field
// names come from the json tag (or the lowercase Go name when absent);
// `omitempty` makes the field optional. additionalProperties is true so
// charts can layer extension fields without false-negative validation.
func schemaForStruct(t reflect.Type) map[string]any {
	props := map[string]any{}
	required := []string{}

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		jsonTag := f.Tag.Get("json")
		name, opts := parseTag(jsonTag)
		if name == "-" {
			continue
		}
		if name == "" {
			// Go convention: lowercase field name when no tag.
			name = strings.ToLower(f.Name[:1]) + f.Name[1:]
		}
		props[name] = schemaForType(f.Type)
		if !opts.has("omitempty") {
			required = append(required, name)
		}
	}

	sort.Strings(required)

	out := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": true,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

type tagOpts []string

func parseTag(tag string) (string, tagOpts) {
	if tag == "" {
		return "", nil
	}
	parts := strings.Split(tag, ",")
	return parts[0], tagOpts(parts[1:])
}

func (o tagOpts) has(opt string) bool {
	return slices.Contains(o, opt)
}
