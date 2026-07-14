package chartimport_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/chartimport"
)

// buildTGZ assembles an in-memory Helm-shaped tarball from a map of paths to
// contents. Paths must already include the chart root prefix (e.g. "demo/Chart.yaml").
func buildTGZ(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz.Close: %v", err)
	}
	return buf.Bytes()
}

func TestParseArchive_ChartYAMLAndValues(t *testing.T) {
	data := buildTGZ(t, map[string]string{
		"demo/Chart.yaml": `apiVersion: v2
name: demo
version: 1.2.3
description: A demo chart
`,
		"demo/values.yaml": `replicaCount: 2
image:
  repository: nginx
  tag: stable
debug: false
`,
	})
	arc, err := chartimport.ParseArchive(data)
	if err != nil {
		t.Fatalf("ParseArchive: %v", err)
	}
	if arc.Name != "demo" {
		t.Errorf("Name = %q, want %q", arc.Name, "demo")
	}
	if arc.Chart.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", arc.Chart.Version)
	}
	if arc.Values["replicaCount"] != 2 {
		t.Errorf("Values[replicaCount] = %v, want 2", arc.Values["replicaCount"])
	}
}

func TestParseArchive_RejectsMissingChartYAML(t *testing.T) {
	data := buildTGZ(t, map[string]string{
		"demo/values.yaml": "k: v\n",
	})
	if _, err := chartimport.ParseArchive(data); err == nil {
		t.Fatal("expected error when Chart.yaml is missing")
	}
}

func TestParseArchive_RejectsTarSlip(t *testing.T) {
	data := buildTGZ(t, map[string]string{
		"../escape/Chart.yaml": "name: x\nversion: 1\n",
	})
	if _, err := chartimport.ParseArchive(data); err == nil {
		t.Fatal("expected error for tar-slip path")
	}
}

func TestToTemplate_RejectsLibraryChart(t *testing.T) {
	// A Helm library chart (type: library, e.g. suparship-common) renders no
	// workloads and must never become an app template.
	data := buildTGZ(t, map[string]string{
		"suparship-common/Chart.yaml": `apiVersion: v2
name: suparship-common
version: 0.1.0
type: library
description: Shared Helm template helpers
`,
	})
	arc, err := chartimport.ParseArchive(data)
	if err != nil {
		t.Fatalf("ParseArchive: %v", err)
	}
	_, err = chartimport.ToTemplate(arc)
	if err == nil {
		t.Fatal("expected library chart to be rejected, got nil error")
	}
	if !errors.Is(err, chartimport.ErrLibraryChart) {
		t.Errorf("error = %v, want ErrLibraryChart", err)
	}
}

func TestToTemplate_FromValuesYAMLOnly(t *testing.T) {
	data := buildTGZ(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.0.0\ndescription: hi\n",
		"demo/values.yaml": `replicaCount: 1
image:
  repository: nginx
  tag: stable
debug: true
`,
	})
	arc, err := chartimport.ParseArchive(data)
	if err != nil {
		t.Fatalf("ParseArchive: %v", err)
	}
	tmpl, err := chartimport.ToTemplate(arc)
	if err != nil {
		t.Fatalf("ToTemplate: %v", err)
	}
	if tmpl.Metadata.Name != "demo" {
		t.Errorf("Name = %q", tmpl.Metadata.Name)
	}
	if tmpl.Spec.Engine.Type != tpl.EngineHelm {
		t.Errorf("Engine.Type = %q", tmpl.Spec.Engine.Type)
	}

	// Inputs should at minimum cover the scalar leaves we walked.
	wantNames := map[string]tpl.InputType{
		"replicacount":     tpl.InputTypeNumber,
		"image_repository": tpl.InputTypeString,
		"image_tag":        tpl.InputTypeString,
		"debug":            tpl.InputTypeBoolean,
	}
	got := map[string]tpl.InputType{}
	for _, in := range tmpl.Spec.Inputs {
		got[in.Name] = in.Type
	}
	for name, want := range wantNames {
		if got[name] != want {
			t.Errorf("input %q: type=%q, want %q (got=%v)", name, got[name], want, got)
		}
	}

	// Mappings must point chart-side dotted paths back at the input expr.
	if mp := tmpl.Spec.Mappings["image.repository"]; !strings.Contains(mp, ".inputs.image_repository") {
		t.Errorf("mappings[image.repository] = %q, want reference to .inputs.image_repository", mp)
	}
}

func TestToTemplate_FromSchemaPreferred(t *testing.T) {
	schema := `{
  "$schema": "https://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["serviceName"],
  "properties": {
    "serviceName": {"type": "string", "title": "Service Name", "description": "the service name"},
    "replicaCount": {"type": "integer", "minimum": 1, "maximum": 10, "default": 1},
    "logLevel": {"type": "string", "enum": ["debug", "info", "warn", "error"], "default": "info"},
    "image": {
      "type": "object",
      "required": ["repository"],
      "properties": {
        "repository": {"type": "string"},
        "tag": {"type": "string", "default": "stable"}
      }
    }
  }
}`
	data := buildTGZ(t, map[string]string{
		"demo/Chart.yaml":          "apiVersion: v2\nname: demo\nversion: 1.0.0\n",
		"demo/values.yaml":         "irrelevant: 1\n",
		"demo/values.schema.json": schema,
	})
	arc, err := chartimport.ParseArchive(data)
	if err != nil {
		t.Fatalf("ParseArchive: %v", err)
	}
	tmpl, err := chartimport.ToTemplate(arc)
	if err != nil {
		t.Fatalf("ToTemplate: %v", err)
	}

	got := map[string]tpl.Input{}
	for _, in := range tmpl.Spec.Inputs {
		got[in.Name] = in
	}

	if in, ok := got["servicename"]; !ok || !in.Required {
		t.Errorf("expected required servicename input, got %+v", in)
	}
	if in, ok := got["replicacount"]; !ok || in.Type != tpl.InputTypeNumber || in.Min == nil || *in.Min != 1 {
		t.Errorf("replicacount input not surfaced with constraints: %+v", in)
	}
	if in, ok := got["loglevel"]; !ok || in.Type != tpl.InputTypeEnum || len(in.Options) != 4 {
		t.Errorf("loglevel enum not surfaced: %+v", in)
	}
	if in, ok := got["image_repository"]; !ok || in.Type != tpl.InputTypeString || !in.Required {
		t.Errorf("image.repository not surfaced as required string: %+v", in)
	}
	if mp := tmpl.Spec.Mappings["image.repository"]; mp == "" {
		t.Errorf("expected mapping for image.repository, got %v", tmpl.Spec.Mappings)
	}

	// values.yaml must NOT be consulted when a schema is present — the
	// "irrelevant" key from values.yaml should not appear as an input.
	if _, has := got["irrelevant"]; has {
		t.Error("schema is preferred over values.yaml; found values-only input")
	}
}

func TestToTemplate_GeneratedAlwaysValidates(t *testing.T) {
	data := buildTGZ(t, map[string]string{
		"demo/Chart.yaml": "apiVersion: v2\nname: My-Demo\nversion: 1.0.0\n",
		"demo/values.yaml": "x: 1\n",
	})
	arc, err := chartimport.ParseArchive(data)
	if err != nil {
		t.Fatalf("ParseArchive: %v", err)
	}
	tmpl, err := chartimport.ToTemplate(arc)
	if err != nil {
		t.Fatalf("ToTemplate: %v", err)
	}
	if err := tmpl.Validate(); err != nil {
		t.Fatalf("generated template did not validate: %v", err)
	}
	// Sanitized name should be DNS-1123-friendly even when chart name is not.
	if tmpl.Metadata.Name != "my-demo" {
		t.Errorf("expected sanitized name 'my-demo', got %q", tmpl.Metadata.Name)
	}
}

// A chart can steer inferred metadata via suparship.io/* Chart.yaml annotations
// without shipping a full template.yaml.
func TestToTemplate_AnnotationsDriveMetadata(t *testing.T) {
	data := buildTGZ(t, map[string]string{
		"voiceai-livekit-agent/Chart.yaml": `apiVersion: v2
name: voiceai-livekit-agent
version: 0.1.0
description: chart-level description
annotations:
  suparship.io/category: worker
  suparship.io/title: VoiceAI LiveKit Agent
  suparship.io/description: overridden description
`,
		"voiceai-livekit-agent/values.yaml": "replicas: 1\n",
	})
	arc, err := chartimport.ParseArchive(data)
	if err != nil {
		t.Fatalf("ParseArchive: %v", err)
	}
	tmpl, err := chartimport.ToTemplate(arc)
	if err != nil {
		t.Fatalf("ToTemplate: %v", err)
	}
	if tmpl.Spec.Category != "worker" {
		t.Errorf("category = %q, want worker (from annotation)", tmpl.Spec.Category)
	}
	if tmpl.Spec.Title != "VoiceAI LiveKit Agent" {
		t.Errorf("title = %q, want annotation title", tmpl.Spec.Title)
	}
	if tmpl.Spec.Description != "overridden description" {
		t.Errorf("description = %q, want annotation description", tmpl.Spec.Description)
	}
}

// Without annotations, category falls back to the inferred "web" default.
func TestToTemplate_NoAnnotationsDefaultsWeb(t *testing.T) {
	data := buildTGZ(t, map[string]string{
		"plain/Chart.yaml":  "apiVersion: v2\nname: plain\nversion: 1.0.0\n",
		"plain/values.yaml": "k: v\n",
	})
	arc, _ := chartimport.ParseArchive(data)
	tmpl, err := chartimport.ToTemplate(arc)
	if err != nil {
		t.Fatalf("ToTemplate: %v", err)
	}
	if tmpl.Spec.Category != "web" {
		t.Errorf("category = %q, want web (default)", tmpl.Spec.Category)
	}
}
