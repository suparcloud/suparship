// Package chartimport turns an arbitrary Helm chart .tgz into a suparship
// Template, so operators can ship existing charts through the suparship UI
// without hand-authoring template.yaml.
//
// The generated template is a best-effort starting point: it always validates,
// but operators are expected to review and refine it (titles, categories,
// descriptions, advanced inputs) before saving. ParseArchive + ToTemplate are
// pure functions to keep them trivially testable.
package chartimport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/tpl"
)

// maxArchiveSize caps the uncompressed size we will read from a .tgz to
// 4 MiB. The MVP storage path is a Kubernetes ConfigMap (1 MiB binaryData
// limit), so any chart larger than this will fail to save anyway. Stopping
// early prevents a malicious archive from filling memory.
const maxArchiveSize = 4 * 1024 * 1024

// ChartArchive holds the parsed contents of a Helm chart .tgz. Only the
// fields we actually translate into a suparship Template are kept.
type ChartArchive struct {
	// Name is the chart's root directory name inside the tarball, which by
	// convention matches Chart.yaml's `name` field.
	Name string
	// Chart holds the parsed Chart.yaml metadata.
	Chart ChartMetadata
	// Values is the decoded values.yaml as a generic map. Empty when
	// values.yaml is absent.
	Values map[string]any
	// Schema is the parsed values.schema.json. Nil when the chart does not
	// ship one; ToTemplate falls back to walking Values in that case.
	Schema *ValuesSchema
	// TemplateYAML holds the raw bytes of an embedded "<root>/template.yaml"
	// when the chart author shipped one. ToTemplate prefers this over the
	// best-effort inference path. Nil when the chart doesn't ship one.
	TemplateYAML []byte
	// Raw preserves the original tarball bytes so callers can persist them
	// to the cluster bundle without re-tarring.
	Raw []byte
}

// ChartMetadata mirrors the subset of Chart.yaml we care about. Other fields
// (dependencies, maintainers, …) are ignored; the operator can edit the
// generated Template if they want them surfaced.
type ChartMetadata struct {
	APIVersion  string `yaml:"apiVersion"`
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	AppVersion  string `yaml:"appVersion"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
	Icon        string `yaml:"icon"`
	// Annotations is Chart.yaml's free-form annotations map. suparship reads
	// suparship.io/* keys (see annotation* consts) so a chart author can steer
	// the inferred template's metadata without hand-authoring a template.yaml.
	Annotations map[string]string `yaml:"annotations"`
}

// Chart.yaml annotation keys suparship honours on the inferred-template path.
// A bundled template.yaml is already the author's source of truth, so these are
// ignored there.
const (
	annotationCategory    = "suparship.io/category"
	annotationTitle       = "suparship.io/title"
	annotationDescription = "suparship.io/description"
)

// ValuesSchema is a tiny, schema-draft-7 subset that handles the shapes we
// see in real Helm charts. We intentionally do not implement full JSON Schema
// — combinators, refs, allOf/oneOf get dropped because they cannot map
// cleanly onto the suparship Input model.
type ValuesSchema struct {
	Type       any                              `json:"type,omitempty"`
	Title      string                           `json:"title,omitempty"`
	Properties map[string]*ValuesSchemaProperty `json:"properties,omitempty"`
	Required   []string                         `json:"required,omitempty"`
}

// ValuesSchemaProperty is the per-property subset of JSON Schema we honour.
type ValuesSchemaProperty struct {
	Type        any                              `json:"type,omitempty"`
	Title       string                           `json:"title,omitempty"`
	Description string                           `json:"description,omitempty"`
	Default     any                              `json:"default,omitempty"`
	Enum        []any                            `json:"enum,omitempty"`
	Pattern     string                           `json:"pattern,omitempty"`
	Minimum     *float64                         `json:"minimum,omitempty"`
	Maximum     *float64                         `json:"maximum,omitempty"`
	Properties  map[string]*ValuesSchemaProperty `json:"properties,omitempty"`
	Required    []string                         `json:"required,omitempty"`
}

// ParseArchive reads a Helm chart .tgz from data and returns the parsed
// pieces we need to generate a Template. data is preserved verbatim in the
// returned ChartArchive.Raw so callers can store the chart byte-identically.
//
// Errors:
//   - data is not a valid gzip stream
//   - the archive does not contain a Chart.yaml at the top of any directory
//   - Chart.yaml fails to parse
//   - the uncompressed payload exceeds maxArchiveSize
func ParseArchive(data []byte) (*ChartArchive, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("not a gzip archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	arc := &ChartArchive{Raw: data}

	var (
		chartYAMLContent  []byte
		valuesYAMLContent []byte
		schemaContent     []byte
		rootDir           string
		readBudget        int64 = maxArchiveSize
	)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar header: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		// Helm convention: chart files live under "<name>/...". Reject any
		// path that escapes its parent (defensive against tar-slip archives).
		clean := path.Clean(hdr.Name)
		if strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return nil, fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		parts := strings.SplitN(clean, "/", 2)
		if len(parts) < 2 {
			continue
		}
		if rootDir == "" {
			rootDir = parts[0]
		}
		// Every chart file must share the same root. If a tarball mixes
		// roots (e.g. dependency charts at the same level) we ignore the
		// stragglers — only the first root is treated as the importable chart.
		if parts[0] != rootDir {
			continue
		}

		switch parts[1] {
		case "Chart.yaml":
			chartYAMLContent, err = readCapped(tr, &readBudget)
		case "values.yaml":
			valuesYAMLContent, err = readCapped(tr, &readBudget)
		case "values.schema.json":
			schemaContent, err = readCapped(tr, &readBudget)
		case tpl.TemplateFileName:
			arc.TemplateYAML, err = readCapped(tr, &readBudget)
		default:
			// Other files (templates/, README, helpers, ...) are not parsed
			// here but still count toward the read budget so we can't be
			// fed a malicious archive that hides bytes in template files.
			if _, err = io.CopyN(io.Discard, tr, hdr.Size); err != nil && err != io.EOF {
				return nil, fmt.Errorf("read tar entry %s: %w", hdr.Name, err)
			}
			readBudget -= hdr.Size
			if readBudget < 0 {
				return nil, fmt.Errorf("archive exceeds %d byte limit", maxArchiveSize)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", hdr.Name, err)
		}
	}

	if chartYAMLContent == nil {
		return nil, fmt.Errorf("Chart.yaml not found in archive")
	}
	if err := yaml.Unmarshal(chartYAMLContent, &arc.Chart); err != nil {
		return nil, fmt.Errorf("parse Chart.yaml: %w", err)
	}
	if arc.Chart.Name == "" {
		return nil, fmt.Errorf("Chart.yaml is missing required field 'name'")
	}
	arc.Name = arc.Chart.Name

	if valuesYAMLContent != nil {
		if err := yaml.Unmarshal(valuesYAMLContent, &arc.Values); err != nil {
			return nil, fmt.Errorf("parse values.yaml: %w", err)
		}
	}

	if schemaContent != nil {
		var sch ValuesSchema
		if err := json.Unmarshal(schemaContent, &sch); err != nil {
			return nil, fmt.Errorf("parse values.schema.json: %w", err)
		}
		arc.Schema = &sch
	}

	return arc, nil
}

// ToTemplate produces a best-effort *tpl.Template from a parsed chart. The
// result always passes tpl.Template.Validate; operators are expected to edit
// title, category, and inputs before saving.
//
// Input generation precedence:
//  1. values.schema.json — typed inputs with constraints (preferred)
//  2. values.yaml top-level walk — type inferred from the value
//
// Each generated input gets a Mapping entry that bridges the input's
// snake_case name back to its dotted path inside the chart's values:
//
//	inputs[].name = "image_repository" (from "image.repository")
//	mappings["image.repository"] = "{{ .inputs.image_repository }}"
//
// At chart render time project.RenderHelmValues already expands dotted keys
// into nested maps, so the generated mappings drop straight into the
// existing render pipeline.
func ToTemplate(arc *ChartArchive) (*tpl.Template, error) {
	if arc == nil {
		return nil, fmt.Errorf("nil archive")
	}
	if arc.Chart.Name == "" {
		return nil, fmt.Errorf("chart name is empty")
	}

	// Bundled-template mode: chart author shipped a template.yaml at
	// <root>/template.yaml. Prefer it over inference so the operator's
	// hand-authored metadata wins. The template's engine.chart should
	// be omitted (bundled — chart IS the artifact); we don't enforce
	// that here so an author can also opt for an inline-style "./chart"
	// path if they really want.
	if len(arc.TemplateYAML) > 0 {
		bundled, err := tpl.Parse(arc.TemplateYAML)
		if err != nil {
			return nil, fmt.Errorf("parse bundled template.yaml: %w", err)
		}
		// Ensure the parsed template carries a sane name+version even
		// if the bundled file omitted them — fall back to Chart.yaml.
		if bundled.Metadata.Name == "" {
			bundled.Metadata.Name = sanitizeName(arc.Chart.Name)
		}
		if bundled.Metadata.Version == "" {
			bundled.Metadata.Version = firstNonEmpty(arc.Chart.Version, "0.1.0")
		}
		if err := bundled.Validate(); err != nil {
			return nil, fmt.Errorf("bundled template.yaml failed validation: %w", err)
		}
		return bundled, nil
	}

	inputs, mappings := buildInputs(arc)

	title := arc.Chart.Name
	if t := strings.TrimSpace(firstNonEmpty(arc.Chart.AppVersion, arc.Chart.Version)); t != "" {
		title = arc.Chart.Name + " " + t
	}
	// "web" is the most common chart shape and matches the existing built-ins;
	// a chart can override category/title/description via suparship.io/*
	// Chart.yaml annotations (lighter than shipping a full template.yaml).
	category := "web"
	description := strings.TrimSpace(arc.Chart.Description)
	if a := arc.Chart.Annotations; a != nil {
		if v := strings.TrimSpace(a[annotationCategory]); v != "" {
			category = v
		}
		if v := strings.TrimSpace(a[annotationTitle]); v != "" {
			title = v
		}
		if v := strings.TrimSpace(a[annotationDescription]); v != "" {
			description = v
		}
	}

	t := &tpl.Template{
		APIVersion: tpl.CurrentAPIVersion,
		Kind:       tpl.TemplateKind,
		Metadata: tpl.Metadata{
			Name:    sanitizeName(arc.Chart.Name),
			Version: firstNonEmpty(arc.Chart.Version, "0.1.0"),
		},
		Spec: tpl.TemplateSpec{
			Title:       title,
			Description: description,
			Category:    category,
			Engine: tpl.Engine{
				Type: tpl.EngineHelm,
				// Inferred-template path: chart sits at ./chart relative
				// to the generated template.yaml. A bundled-template
				// chart (one shipping its own template.yaml inside the
				// .tgz) takes a different path and never reaches here.
				Chart: tpl.ChartLocator{Path: "./chart"},
			},
			Components: []tpl.TemplateComponent{
				{
					Name:     "web",
					Type:     tpl.TemplateComponentWeb,
					Required: true,
					Exposed:  true,
				},
			},
			Inputs:   inputs,
			Mappings: mappings,
			Images:   DetectImageMappings(arc.Values),
		},
	}
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("generated template failed validation: %w", err)
	}
	return t, nil
}

// DetectImageMappings infers per-service image mappings from a chart's
// values.yaml so the import preview can pre-fill them (the operator reviews and
// fills tagPattern/strategy before saving). It emits a mapping only when an
// image block carries a "repository" string, so the generated template stays
// valid; tag-only image blocks are left for the operator to complete manually.
//
// Recognised shapes: a root `image` block, `components.<name>.image` (canonical
// suparship layout), and any top-level service map containing an `image` block
// (e.g. `agent.image`, `caller.image` in multi-service BYO charts).
func DetectImageMappings(values map[string]any) []tpl.TemplateImage {
	if values == nil {
		return nil
	}
	var out []tpl.TemplateImage
	seen := map[string]bool{}
	add := func(name, prefix string, image map[string]any) {
		repo, _ := image["repository"].(string)
		if repo == "" || seen[name] {
			return
		}
		seen[name] = true
		tagKey := "image.tag"
		if prefix != "" {
			tagKey = prefix + ".image.tag"
		}
		out = append(out, tpl.TemplateImage{Name: name, Repository: repo, TagKey: tagKey})
	}

	if img, ok := values["image"].(map[string]any); ok {
		add("image", "", img)
	}
	if comps, ok := values["components"].(map[string]any); ok {
		for _, name := range sortedMapKeys(comps) {
			if cm, ok := comps[name].(map[string]any); ok {
				if img, ok := cm["image"].(map[string]any); ok {
					add(name, "components."+name, img)
				}
			}
		}
	}
	for _, name := range sortedMapKeys(values) {
		if name == "image" || name == "components" {
			continue
		}
		if sm, ok := values[name].(map[string]any); ok {
			if img, ok := sm["image"].(map[string]any); ok {
				add(name, name, img)
			}
		}
	}
	return out
}

// sortedMapKeys returns a map's keys in lexical order for deterministic output.
func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// buildInputs picks the schema path when available and falls back to walking
// values.yaml. Returned inputs are sorted by name for determinism.
func buildInputs(arc *ChartArchive) ([]tpl.Input, map[string]string) {
	mappings := map[string]string{}

	var inputs []tpl.Input
	if arc.Schema != nil && len(arc.Schema.Properties) > 0 {
		inputs = inputsFromSchema(arc.Schema, mappings)
	} else {
		inputs = inputsFromValues(arc.Values, mappings)
	}

	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Name < inputs[j].Name })
	return inputs, mappings
}

// inputsFromSchema walks the schema two levels deep — top-level scalars and
// the immediate children of top-level objects (e.g. image.repository). Deeper
// nesting is intentionally skipped: the suparship Input model is flat, and
// drilling further produces noise the operator must mostly delete anyway.
func inputsFromSchema(schema *ValuesSchema, mappings map[string]string) []tpl.Input {
	required := toSet(schema.Required)
	var out []tpl.Input
	for name, prop := range schema.Properties {
		if prop == nil {
			continue
		}
		if isObjectType(prop.Type) && len(prop.Properties) > 0 {
			subRequired := toSet(prop.Required)
			for sub, subProp := range prop.Properties {
				if subProp == nil || isObjectType(subProp.Type) {
					continue
				}
				dotted := name + "." + sub
				input := schemaPropToInput(dotted, subProp, subRequired[sub])
				if input == nil {
					continue
				}
				out = append(out, *input)
				mappings[dotted] = inputExpr(input.Name)
			}
			continue
		}
		input := schemaPropToInput(name, prop, required[name])
		if input == nil {
			continue
		}
		out = append(out, *input)
		mappings[name] = inputExpr(input.Name)
	}
	return out
}

// schemaPropToInput converts a single scalar JSON Schema property into a
// suparship Input. Objects/arrays return nil — they are unsupported in the
// flat Input model and would only confuse operators.
func schemaPropToInput(dottedPath string, prop *ValuesSchemaProperty, required bool) *tpl.Input {
	t := schemaTypeToInputType(prop.Type, prop.Enum)
	if t == "" {
		return nil
	}
	in := &tpl.Input{
		Name:        toInputName(dottedPath),
		Title:       firstNonEmpty(prop.Title, humanize(dottedPath)),
		Type:        t,
		Description: strings.TrimSpace(prop.Description),
		Required:    required,
		Default:     prop.Default,
		Pattern:     prop.Pattern,
		Min:         prop.Minimum,
		Max:         prop.Maximum,
	}
	if t == tpl.InputTypeEnum {
		in.Options = enumOptions(prop.Enum)
	}
	return in
}

// inputsFromValues walks values.yaml two levels deep when no schema is
// shipped. Inferred types only (string/number/boolean) — there's no way to
// recover required-ness or constraints from defaults.
func inputsFromValues(values map[string]any, mappings map[string]string) []tpl.Input {
	var out []tpl.Input
	for k, v := range values {
		switch val := v.(type) {
		case map[string]any:
			for sub, subVal := range val {
				if isScalar(subVal) {
					dotted := k + "." + sub
					out = append(out, valueToInput(dotted, subVal))
					mappings[dotted] = inputExpr(toInputName(dotted))
				}
			}
		default:
			if isScalar(val) {
				out = append(out, valueToInput(k, val))
				mappings[k] = inputExpr(toInputName(k))
			}
		}
	}
	return out
}

// valueToInput infers an Input from a values.yaml leaf. The original value
// becomes the Input's Default so the rendered chart matches the chart's own
// defaults until the operator changes anything.
func valueToInput(dottedPath string, v any) tpl.Input {
	in := tpl.Input{
		Name:    toInputName(dottedPath),
		Title:   humanize(dottedPath),
		Default: v,
	}
	switch v.(type) {
	case bool:
		in.Type = tpl.InputTypeBoolean
	case int, int64, float64:
		in.Type = tpl.InputTypeNumber
	default:
		in.Type = tpl.InputTypeString
	}
	return in
}

// schemaTypeToInputType maps JSON Schema types to suparship InputTypes.
// Returns "" when the type is unsupported (object, array, null) so the caller
// can skip it.
func schemaTypeToInputType(t any, enum []any) tpl.InputType {
	if len(enum) > 0 {
		return tpl.InputTypeEnum
	}
	switch v := t.(type) {
	case string:
		return primitiveTypeToInput(v)
	case []any:
		// `["string", "null"]` is common for nullable fields; pick the
		// first non-null primitive so the input still surfaces.
		for _, x := range v {
			if s, ok := x.(string); ok && s != "null" {
				if it := primitiveTypeToInput(s); it != "" {
					return it
				}
			}
		}
	}
	return ""
}

func primitiveTypeToInput(t string) tpl.InputType {
	switch t {
	case "string":
		return tpl.InputTypeString
	case "boolean":
		return tpl.InputTypeBoolean
	case "number", "integer":
		return tpl.InputTypeNumber
	}
	return ""
}

// enumOptions converts JSON Schema enum values (any[]) to the string options
// suparship expects. Non-string entries are stringified via fmt.Sprint.
func enumOptions(enum []any) []string {
	out := make([]string, 0, len(enum))
	for _, e := range enum {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

// isObjectType reports whether the JSON Schema type is "object" (or a list
// containing it). Used to decide whether to recurse into Properties.
func isObjectType(t any) bool {
	switch v := t.(type) {
	case string:
		return v == "object"
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && s == "object" {
				return true
			}
		}
	}
	return false
}

// isScalar reports whether v is a primitive value we can surface as an Input
// default. Maps and slices are treated as structural and skipped by the
// values.yaml walker.
func isScalar(v any) bool {
	switch v.(type) {
	case string, bool, int, int64, float64:
		return true
	}
	return false
}

// toSet turns a string slice into a presence map for O(1) lookup.
func toSet(ss []string) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[s] = true
	}
	return out
}

// toInputName converts a dotted chart-value path into a snake_case Input
// name. Non-alphanumeric runs collapse to a single underscore so that
// well-formed chart paths produce valid DNS-label-ish names.
func toInputName(dotted string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range dotted {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(unicode.ToLower(r))
			prevUnderscore = false
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore && b.Len() > 0 {
				b.WriteRune('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// humanize is a low-effort fallback Title generator: "image.repository" →
// "Image Repository". Operators almost always override this in review, so we
// don't try to be clever.
func humanize(dotted string) string {
	parts := strings.FieldsFunc(dotted, func(r rune) bool {
		return r == '.' || r == '_' || r == '-'
	})
	for i, p := range parts {
		if p == "" {
			continue
		}
		runes := []rune(p)
		runes[0] = unicode.ToUpper(runes[0])
		parts[i] = string(runes)
	}
	return strings.Join(parts, " ")
}

// sanitizeName forces a chart name into a DNS-1123-ish shape: lowercase,
// alphanumerics and hyphens. Leading/trailing non-alphanumerics are trimmed.
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// inputExpr returns the `{{ .inputs.<name> }}` expression that
// project.RenderHelmValues consumes when applying mappings.
func inputExpr(name string) string {
	return "{{ .inputs." + name + " }}"
}

// firstNonEmpty returns the first non-empty trimmed string, or "" when none
// are non-empty. Used for fallback chains in metadata generation.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// readCapped reads up to budget bytes from r, decrementing budget as it goes.
// Returns an error if the entry would exceed the budget — used to enforce
// the maxArchiveSize cap across the whole archive.
func readCapped(r io.Reader, budget *int64) ([]byte, error) {
	limited := io.LimitReader(r, *budget+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > *budget {
		return nil, fmt.Errorf("archive exceeds %d byte limit", maxArchiveSize)
	}
	*budget -= int64(len(data))
	return data, nil
}
