package tpl

import (
	"strings"
	"testing"
)

const validTemplateYAML = `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: web-service
  version: "1.0.0"
spec:
  title: Web Service
  description: Deploy a containerized web service
  category: web
  engine:
    type: helm
    chart: ./chart
  inputs:
    - name: image
      title: Container Image
      type: string
      required: true
    - name: replicas
      title: Replicas
      type: number
      default: 2
      min: 1
      max: 10
    - name: public
      title: Public Access
      type: boolean
      default: false
    - name: tier
      title: Service Tier
      type: enum
      options: [small, medium, large]
      default: small
  advancedInputs:
    - name: cpu_limit
      title: CPU Limit
      type: string
      default: "500m"
  secretInputs:
    - name: database_url
      title: Database URL
      secretRef: db-credentials.url
  mappings:
    image: "{{ .inputs.image }}"
    replicaCount: "{{ .inputs.replicas }}"
  presets:
    - name: starter
      title: Starter
      description: Minimal resources
      values:
        replicas: 1
        tier: small
    - name: production
      title: Production
      values:
        replicas: 3
        tier: large
`

func TestParseValidTemplate(t *testing.T) {
	tmpl, err := Parse([]byte(validTemplateYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if tmpl.Metadata.Name != "web-service" {
		t.Fatalf("expected name %q, got %q", "web-service", tmpl.Metadata.Name)
	}
	if tmpl.Metadata.Version != "1.0.0" {
		t.Fatalf("expected version %q, got %q", "1.0.0", tmpl.Metadata.Version)
	}
	if tmpl.Spec.Title != "Web Service" {
		t.Fatalf("expected title %q, got %q", "Web Service", tmpl.Spec.Title)
	}
	if tmpl.Spec.Engine.Type != EngineHelm {
		t.Fatalf("expected engine type %q, got %q", EngineHelm, tmpl.Spec.Engine.Type)
	}
	if len(tmpl.Spec.Inputs) != 4 {
		t.Fatalf("expected 4 inputs, got %d", len(tmpl.Spec.Inputs))
	}
	if len(tmpl.Spec.AdvancedInputs) != 1 {
		t.Fatalf("expected 1 advanced input, got %d", len(tmpl.Spec.AdvancedInputs))
	}
	if len(tmpl.Spec.SecretInputs) != 1 {
		t.Fatalf("expected 1 secret input, got %d", len(tmpl.Spec.SecretInputs))
	}
	if len(tmpl.Spec.Presets) != 2 {
		t.Fatalf("expected 2 presets, got %d", len(tmpl.Spec.Presets))
	}
	if len(tmpl.Spec.Mappings) != 2 {
		t.Fatalf("expected 2 mappings, got %d", len(tmpl.Spec.Mappings))
	}
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := Parse([]byte(`{{{not yaml`))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParseMinimalValid(t *testing.T) {
	yaml := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: minimal
  version: "0.1.0"
spec:
  title: Minimal
  category: misc
  engine:
    type: helm
`
	tmpl, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("minimal template should be valid: %v", err)
	}
	if tmpl.Metadata.Name != "minimal" {
		t.Fatalf("expected name %q, got %q", "minimal", tmpl.Metadata.Name)
	}
}

func TestParseDefaultAndEnvValues(t *testing.T) {
	yaml := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: layered
  version: "0.1.0"
spec:
  title: Layered
  category: misc
  engine:
    type: helm
  defaultValues:
    replicas: 1
    image:
      repository: ghcr.io/org/app
  envValues:
    staging:
      replicas: 1
    prod:
      replicas: 4
      resources:
        requests:
          cpu: "500m"
`
	tmpl, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("template with defaultValues/envValues should parse: %v", err)
	}
	if tmpl.Spec.DefaultValues["replicas"] != 1 {
		t.Errorf("defaultValues.replicas = %v, want 1", tmpl.Spec.DefaultValues["replicas"])
	}
	if tmpl.Spec.EnvValues["prod"]["replicas"] != 4 {
		t.Errorf("envValues.prod.replicas = %v, want 4", tmpl.Spec.EnvValues["prod"]["replicas"])
	}
}

func mustContain(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("expected error containing %q, got: %v", substr, err)
	}
}

func TestValidateWrongAPIVersion(t *testing.T) {
	tmpl := validTemplate()
	tmpl.APIVersion = "v99"
	mustContain(t, tmpl.Validate(), "unsupported apiVersion")
}

func TestValidateWrongKind(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Kind = "Service"
	mustContain(t, tmpl.Validate(), "unsupported kind")
}

func TestValidateMissingName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Metadata.Name = ""
	mustContain(t, tmpl.Validate(), "metadata.name")
}

func TestValidateMissingVersion(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Metadata.Version = ""
	mustContain(t, tmpl.Validate(), "metadata.version")
}

func TestValidateMissingTitle(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Title = ""
	mustContain(t, tmpl.Validate(), "spec.title")
}

func TestValidateMissingCategory(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Category = ""
	mustContain(t, tmpl.Validate(), "spec.category")
}

func TestValidateMissingEngineType(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Engine.Type = ""
	mustContain(t, tmpl.Validate(), "spec.engine.type")
}

func TestValidateUnsupportedEngineType(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Engine.Type = "kustomize"
	mustContain(t, tmpl.Validate(), "unsupported engine type")
}

func TestValidateInputMissingName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Title: "X", Type: InputTypeString}}
	mustContain(t, tmpl.Validate(), "name is required")
}

func TestValidateInputMissingTitle(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "x", Type: InputTypeString}}
	mustContain(t, tmpl.Validate(), "title is required")
}

func TestValidateInputInvalidType(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "x", Title: "X", Type: "file"}}
	mustContain(t, tmpl.Validate(), "unsupported type")
}

func TestValidateDuplicateInputName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{
		{Name: "dup", Title: "A", Type: InputTypeString},
		{Name: "dup", Title: "B", Type: InputTypeString},
	}
	mustContain(t, tmpl.Validate(), "duplicate input name")
}

func TestValidateDuplicateAcrossInputAndAdvanced(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "x", Title: "X", Type: InputTypeString}}
	tmpl.Spec.AdvancedInputs = []Input{{Name: "x", Title: "X2", Type: InputTypeString}}
	mustContain(t, tmpl.Validate(), "duplicate input name")
}

func TestValidateEnumWithoutOptions(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "e", Title: "E", Type: InputTypeEnum}}
	mustContain(t, tmpl.Validate(), "enum type requires at least one option")
}

func TestValidateNumberMinExceedsMax(t *testing.T) {
	tmpl := validTemplate()
	min, max := 10.0, 5.0
	tmpl.Spec.Inputs = []Input{{Name: "n", Title: "N", Type: InputTypeNumber, Min: &min, Max: &max}}
	mustContain(t, tmpl.Validate(), "min")
}

func TestValidateDefaultTypeMismatchString(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "s", Title: "S", Type: InputTypeString, Default: 42}}
	mustContain(t, tmpl.Validate(), "default must be a string")
}

func TestValidateDefaultTypeMismatchNumber(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "n", Title: "N", Type: InputTypeNumber, Default: "abc"}}
	mustContain(t, tmpl.Validate(), "default must be a number")
}

func TestValidateDefaultTypeMismatchBoolean(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "b", Title: "B", Type: InputTypeBoolean, Default: "yes"}}
	mustContain(t, tmpl.Validate(), "default must be a boolean")
}

func TestValidateEnumDefaultNotInOptions(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "e", Title: "E", Type: InputTypeEnum, Options: []string{"a", "b"}, Default: "c"}}
	mustContain(t, tmpl.Validate(), "not in options")
}

func TestValidateSecretInputMissingRef(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.SecretInputs = []SecretInput{{Name: "s", Title: "S"}}
	mustContain(t, tmpl.Validate(), "secretRef is required")
}

func TestValidateSecretInputBadRefFormat(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.SecretInputs = []SecretInput{{Name: "s", Title: "S", SecretRef: "noDot"}}
	mustContain(t, tmpl.Validate(), "secret-name.key")
}

func TestValidatePresetUnknownInput(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "x", Title: "X", Type: InputTypeString}}
	tmpl.Spec.Presets = []Preset{{Name: "p", Title: "P", Values: map[string]any{"ghost": "val"}}}
	mustContain(t, tmpl.Validate(), "unknown input")
}

func TestValidatePresetMissingName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Presets = []Preset{{Title: "P", Values: map[string]any{}}}
	mustContain(t, tmpl.Validate(), "name is required")
}

func validTemplate() *Template {
	return &Template{
		APIVersion: CurrentAPIVersion,
		Kind:       TemplateKind,
		Metadata:   Metadata{Name: "test", Version: "1.0.0"},
		Spec: TemplateSpec{
			Title:    "Test",
			Category: "misc",
			Engine:   Engine{Type: EngineHelm},
		},
	}
}

// ── spec.components — parse ───────────────────────────────────────────────────

const templateWithComponentsYAML = `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: multi-component
  version: "1.0.0"
spec:
  title: Multi Component
  category: web
  engine:
    type: helm
    chart: ./chart
  components:
    - name: web
      type: web
      required: true
      defaultEnabled: true
      previewEnabled: true
      exposed: true
    - name: worker
      type: worker
      defaultEnabled: false
      previewEnabled: false
      exposed: false
    - name: cron
      type: cron
      previewEnabled: false
  inputs:
    - name: image
      title: Container Image
      type: string
      component: web
    - name: schedule
      title: Cron Schedule
      type: string
      component: cron
    - name: replicas
      title: Replica Count
      type: number
      default: 2
`

func TestParseTemplateWithComponents(t *testing.T) {
	tmpl, err := Parse([]byte(templateWithComponentsYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(tmpl.Spec.Components) != 3 {
		t.Fatalf("expected 3 components, got %d", len(tmpl.Spec.Components))
	}

	web := tmpl.Spec.Components[0]
	if web.Name != "web" {
		t.Errorf("components[0].Name = %q, want %q", web.Name, "web")
	}
	if web.Type != TemplateComponentWeb {
		t.Errorf("components[0].Type = %q, want %q", web.Type, TemplateComponentWeb)
	}
	if !web.Required {
		t.Error("components[0].Required should be true")
	}
	if !web.Exposed {
		t.Error("components[0].Exposed should be true")
	}
	if !web.IsDefaultEnabled() {
		t.Error("components[0].IsDefaultEnabled() should be true when defaultEnabled: true")
	}

	worker := tmpl.Spec.Components[1]
	if worker.Type != TemplateComponentWorker {
		t.Errorf("components[1].Type = %q, want %q", worker.Type, TemplateComponentWorker)
	}
	if worker.IsDefaultEnabled() {
		t.Error("components[1].IsDefaultEnabled() should be false when defaultEnabled: false")
	}
	if worker.Exposed {
		t.Error("components[1].Exposed should be false")
	}

	cron := tmpl.Spec.Components[2]
	if cron.Type != TemplateComponentCron {
		t.Errorf("components[2].Type = %q, want %q", cron.Type, TemplateComponentCron)
	}
	// defaultEnabled omitted → nil → treated as true
	if !cron.IsDefaultEnabled() {
		t.Error("components[2].IsDefaultEnabled() should be true when defaultEnabled is omitted")
	}
}

func TestParseComponentsInputScoping(t *testing.T) {
	tmpl, err := Parse([]byte(templateWithComponentsYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byName := make(map[string]Input)
	for _, inp := range tmpl.Spec.Inputs {
		byName[inp.Name] = inp
	}

	if byName["image"].Component != "web" {
		t.Errorf("input 'image'.Component = %q, want %q", byName["image"].Component, "web")
	}
	if byName["schedule"].Component != "cron" {
		t.Errorf("input 'schedule'.Component = %q, want %q", byName["schedule"].Component, "cron")
	}
	if byName["replicas"].Component != "" {
		t.Errorf("input 'replicas'.Component = %q, want %q (app-level)", byName["replicas"].Component, "")
	}
}

// ── IsDefaultEnabled ──────────────────────────────────────────────────────────

func TestIsDefaultEnabledNilMeansTrue(t *testing.T) {
	c := TemplateComponent{Name: "web", Type: TemplateComponentWeb}
	if !c.IsDefaultEnabled() {
		t.Error("IsDefaultEnabled() should be true when DefaultEnabled is nil")
	}
}

func TestIsDefaultEnabledExplicitTrue(t *testing.T) {
	v := true
	c := TemplateComponent{Name: "web", Type: TemplateComponentWeb, DefaultEnabled: &v}
	if !c.IsDefaultEnabled() {
		t.Error("IsDefaultEnabled() should be true when DefaultEnabled is &true")
	}
}

func TestIsDefaultEnabledExplicitFalse(t *testing.T) {
	v := false
	c := TemplateComponent{Name: "web", Type: TemplateComponentWeb, DefaultEnabled: &v}
	if c.IsDefaultEnabled() {
		t.Error("IsDefaultEnabled() should be false when DefaultEnabled is &false")
	}
}

// ── spec.components — validation ──────────────────────────────────────────────

func TestValidateComponentMissingName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{{Type: TemplateComponentWeb}}
	mustContain(t, tmpl.Validate(), "name is required")
}

func TestValidateComponentMissingType(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{{Name: "web"}}
	mustContain(t, tmpl.Validate(), "type is required")
}

func TestValidateComponentUnsupportedType(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{{Name: "db", Type: "database"}}
	mustContain(t, tmpl.Validate(), "unsupported type")
}

func TestValidateComponentDuplicateName(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{
		{Name: "web", Type: TemplateComponentWeb},
		{Name: "web", Type: TemplateComponentWorker},
	}
	mustContain(t, tmpl.Validate(), "duplicate component name")
}

func TestValidateComponentAllValidTypes(t *testing.T) {
	for _, ct := range []TemplateComponentType{
		TemplateComponentWeb,
		TemplateComponentWorker,
		TemplateComponentCron,
	} {
		ct := ct
		t.Run(string(ct), func(t *testing.T) {
			tmpl := validTemplate()
			tmpl.Spec.Components = []TemplateComponent{{Name: "c", Type: ct}}
			if err := tmpl.Validate(); err != nil {
				t.Errorf("expected valid, got: %v", err)
			}
		})
	}
}

func TestValidateComponentMultipleComponentsValid(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{
		{Name: "web", Type: TemplateComponentWeb},
		{Name: "worker", Type: TemplateComponentWorker},
		{Name: "cron", Type: TemplateComponentCron},
	}
	if err := tmpl.Validate(); err != nil {
		t.Errorf("expected valid multi-component template, got: %v", err)
	}
}

// ── Input.Component cross-reference ──────────────────────────────────────────

func TestValidateInputComponentRefValid(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Components = []TemplateComponent{{Name: "web", Type: TemplateComponentWeb}}
	tmpl.Spec.Inputs = []Input{{Name: "port", Title: "Port", Type: InputTypeNumber, Component: "web"}}
	if err := tmpl.Validate(); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestValidateInputComponentRefUnknown(t *testing.T) {
	tmpl := validTemplate()
	// No components defined; "web" is unknown.
	tmpl.Spec.Inputs = []Input{{Name: "port", Title: "Port", Type: InputTypeNumber, Component: "web"}}
	mustContain(t, tmpl.Validate(), "not defined in spec.components")
}

func TestValidateAdvancedInputComponentRefUnknown(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.AdvancedInputs = []Input{
		{Name: "cpu", Title: "CPU", Type: InputTypeString, Component: "nonexistent"},
	}
	mustContain(t, tmpl.Validate(), "not defined in spec.components")
}

func TestValidateInputNoComponentFieldIsAlwaysValid(t *testing.T) {
	tmpl := validTemplate()
	tmpl.Spec.Inputs = []Input{{Name: "image", Title: "Image", Type: InputTypeString}}
	if err := tmpl.Validate(); err != nil {
		t.Errorf("input without component field should always be valid: %v", err)
	}
}

// ── Backward compatibility ────────────────────────────────────────────────────

// Templates without a spec.components section must continue to parse and
// validate without modification (existing templates must not break).
func TestBackwardCompatNoComponentsSection(t *testing.T) {
	tmpl, err := Parse([]byte(validTemplateYAML))
	if err != nil {
		t.Fatalf("existing template without components section should still be valid: %v", err)
	}
	if len(tmpl.Spec.Components) != 0 {
		t.Errorf("expected 0 components when section is absent, got %d", len(tmpl.Spec.Components))
	}
}

func TestBackwardCompatMinimalTemplate(t *testing.T) {
	yaml := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: minimal
  version: "0.1.0"
spec:
  title: Minimal
  category: misc
  engine:
    type: helm
`
	if _, err := Parse([]byte(yaml)); err != nil {
		t.Errorf("minimal template without components should parse: %v", err)
	}
}

// ── Capabilities ──────────────────────────────────────────────────────────────

func TestResolvedCapabilities_WebDefaults(t *testing.T) {
	c := TemplateComponent{Name: "web", Type: TemplateComponentWeb}
	got := c.ResolvedCapabilities()
	want := ResolvedCapabilities{
		Expose:      true,
		Routing:     "ingress",
		Autoscaling: "keda",
		PDB:         true,
		Resources:   true,
		Replicas:    true,
	}
	if got != want {
		t.Errorf("web defaults = %+v, want %+v", got, want)
	}
}

func TestResolvedCapabilities_WorkerDefaults(t *testing.T) {
	c := TemplateComponent{Name: "worker", Type: TemplateComponentWorker}
	got := c.ResolvedCapabilities()
	if got.Expose {
		t.Errorf("worker.Expose = true, want false")
	}
	if got.Routing != "none" {
		t.Errorf("worker.Routing = %q, want none", got.Routing)
	}
	if got.Autoscaling != "keda" {
		t.Errorf("worker.Autoscaling = %q, want keda", got.Autoscaling)
	}
	if got.Schedule {
		t.Errorf("worker.Schedule = true, want false")
	}
}

func TestResolvedCapabilities_CronDefaults(t *testing.T) {
	c := TemplateComponent{Name: "nightly", Type: TemplateComponentCron}
	got := c.ResolvedCapabilities()
	if !got.Schedule {
		t.Error("cron.Schedule = false, want true")
	}
	if got.Replicas {
		t.Error("cron.Replicas = true, want false")
	}
	if got.Autoscaling != "" {
		t.Errorf("cron.Autoscaling = %q, want empty (no scaling for cron)", got.Autoscaling)
	}
}

func TestResolvedCapabilities_ExplicitOverridesWin(t *testing.T) {
	off := false
	on := true
	c := TemplateComponent{
		Name: "stateful-worker",
		Type: TemplateComponentWorker,
		Capabilities: ComponentCapabilities{
			PDB:         &off,   // override worker default
			Resources:   &off,   // chart bakes its own resources
			Autoscaling: "none", // no scaling for this stateful workload
			Expose:      &on,    // expose a metrics port
			Routing:     "ingress",
		},
	}
	got := c.ResolvedCapabilities()
	if got.PDB {
		t.Error("explicit pdb=false should override worker default of true")
	}
	if got.Resources {
		t.Error("explicit resources=false should override worker default of true")
	}
	if got.Autoscaling != "none" {
		t.Errorf("explicit autoscaling=none should win, got %q", got.Autoscaling)
	}
	if !got.Expose {
		t.Error("explicit expose=true should override worker default of false")
	}
	if got.Routing != "ingress" {
		t.Errorf("explicit routing=ingress should win, got %q", got.Routing)
	}
}

func TestParseTemplate_CapabilitiesYAML(t *testing.T) {
	yaml := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: parse-caps
  version: "0.1.0"
spec:
  title: Parse Caps
  category: misc
  engine:
    type: helm
  components:
    - name: web
      type: web
      capabilities:
        expose: false
        autoscaling: hpa
        pdb: false
`
	tmpl, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len(tmpl.Spec.Components); got != 1 {
		t.Fatalf("components count = %d, want 1", got)
	}
	c := tmpl.Spec.Components[0]
	if c.Capabilities.Expose == nil || *c.Capabilities.Expose != false {
		t.Errorf("Capabilities.Expose = %v, want explicit false", c.Capabilities.Expose)
	}
	if c.Capabilities.Autoscaling != "hpa" {
		t.Errorf("Capabilities.Autoscaling = %q, want hpa", c.Capabilities.Autoscaling)
	}
	resolved := c.ResolvedCapabilities()
	if resolved.Expose {
		t.Error("ResolvedCapabilities should honor explicit expose=false against web default")
	}
	if resolved.Autoscaling != "hpa" {
		t.Errorf("ResolvedCapabilities.Autoscaling = %q, want hpa", resolved.Autoscaling)
	}
}

// --- ChartLocator round-trip tests ---
//
// Three accepted YAML shapes for engine.chart, plus the absent (bundled)
// case. Round-tripping must preserve each.

func TestChartLocator_ParseInline(t *testing.T) {
	src := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata: { name: t, version: "1.0.0" }
spec:
  title: T
  category: web
  engine:
    type: helm
    chart: ./chart
  inputs: []
`
	tmpl, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := tmpl.Spec.Engine.Chart
	if !c.IsInline() {
		t.Fatalf("locator = %+v, want inline", c)
	}
	if c.Path != "./chart" {
		t.Errorf("Path = %q, want ./chart", c.Path)
	}
}

func TestChartLocator_ParseExternal(t *testing.T) {
	src := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata: { name: t, version: "1.0.0" }
spec:
  title: T
  category: web
  engine:
    type: helm
    chart:
      repository: oci://ghcr.io/suparcloud/charts
      name: web-service
      version: 1.2.0
  inputs: []
`
	tmpl, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := tmpl.Spec.Engine.Chart
	if !c.IsExternal() {
		t.Fatalf("locator = %+v, want external", c)
	}
	if c.Ref.Repository != "oci://ghcr.io/suparcloud/charts" || c.Ref.Name != "web-service" || c.Ref.Version != "1.2.0" {
		t.Errorf("Ref = %+v, want repository/name/version populated", c.Ref)
	}
}

func TestChartLocator_ParseBundled_OmittedField(t *testing.T) {
	src := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata: { name: t, version: "1.0.0" }
spec:
  title: T
  category: web
  engine:
    type: helm
  inputs: []
`
	tmpl, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := tmpl.Spec.Engine.Chart
	if !c.IsBundled() {
		t.Fatalf("locator = %+v, want bundled", c)
	}
}

func TestChartLocator_ParseBundled_NullField(t *testing.T) {
	// `chart:` with nothing after the colon — yaml parses this as a null
	// scalar. Should also be treated as bundled.
	src := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata: { name: t, version: "1.0.0" }
spec:
  title: T
  category: web
  engine:
    type: helm
    chart:
  inputs: []
`
	tmpl, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !tmpl.Spec.Engine.Chart.IsBundled() {
		t.Fatalf("locator = %+v, want bundled", tmpl.Spec.Engine.Chart)
	}
}

func TestChartLocator_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   ChartLocator
		want string // expected substring in marshalled output (for the chart line(s))
	}{
		{
			name: "bundled — field omitted",
			in:   ChartLocator{},
			want: "type: helm\n",
		},
		{
			name: "inline",
			in:   ChartLocator{Path: "./chart"},
			want: "chart: ./chart",
		},
		{
			name: "external",
			in:   ChartLocator{Ref: &ChartRef{Repository: "oci://ghcr.io/x", Name: "y", Version: "1.0.0"}},
			want: "repository: oci://ghcr.io/x",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := &Template{
				APIVersion: "suparship.io/v1alpha1",
				Kind:       "Template",
				Metadata:   Metadata{Name: "t", Version: "1.0.0"},
				Spec: TemplateSpec{
					Title:    "T",
					Category: "web",
					Engine:   Engine{Type: EngineHelm, Chart: tc.in},
					Inputs:   []Input{},
				},
			}
			out, err := Marshal(tmpl)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("output missing %q\nfull output:\n%s", tc.want, out)
			}
			// For the bundled case, also assert the field is genuinely
			// absent (not emitted as `chart: {}` or `chart: null`).
			if tc.in.IsBundled() && strings.Contains(string(out), "chart:") {
				t.Errorf("bundled mode should omit chart field; got:\n%s", out)
			}
			// Round-trip should land the same locator back.
			parsed, err := Parse(out)
			if err != nil {
				t.Fatalf("re-parse: %v", err)
			}
			got := parsed.Spec.Engine.Chart
			if got.Path != tc.in.Path {
				t.Errorf("Path lost: got %q, want %q", got.Path, tc.in.Path)
			}
			if (got.Ref == nil) != (tc.in.Ref == nil) {
				t.Errorf("Ref nil-ness mismatch: got %v, want %v", got.Ref, tc.in.Ref)
			}
			if got.Ref != nil && tc.in.Ref != nil && *got.Ref != *tc.in.Ref {
				t.Errorf("Ref mismatch: got %+v, want %+v", *got.Ref, *tc.in.Ref)
			}
		})
	}
}

func TestTemplateSpec_CanonicalValues(t *testing.T) {
	tr, fa := true, false
	cases := []struct {
		name string
		flag *bool
		want bool
	}{
		{"unset defaults true", nil, true},
		{"explicit true", &tr, true},
		{"explicit false", &fa, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := TemplateSpec{InjectCanonicalValues: tc.flag}
			if got := s.CanonicalValues(); got != tc.want {
				t.Errorf("CanonicalValues() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTemplateSpec_InjectCanonicalValues_Unmarshal(t *testing.T) {
	y := `
apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: byo
  version: "1.0.0"
spec:
  title: BYO
  category: web
  engine:
    type: helm
  injectCanonicalValues: false
`
	tmpl, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tmpl.Spec.CanonicalValues() {
		t.Error("expected passthrough (CanonicalValues false) for injectCanonicalValues: false")
	}
}
