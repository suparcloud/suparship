package project

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// --- valid YAML for reuse ---

const validProjectYAML = `
apiVersion: suparship.io/v1alpha1
kind: Project
metadata:
  name: myapi
spec:
  displayName: My API
  description: The main API project
  environments:
    - name: dev
      displayName: Development
      order: 1
    - name: staging
      order: 2
    - name: prod
      displayName: Production
      order: 3
  services:
    - name: api
      template:
        name: web-service
        version: "1.0.0"
      values:
        image_repository: ghcr.io/org/api
        size: small
        port: 8080
      secretRefs:
        - name: database_url
          secretRef: api-db.url
      environmentOverrides:
        prod:
          values:
            size: large
          secretRefs:
            - name: database_url
              secretRef: api-db-prod.url
    - name: worker
      template:
        name: web-service
      values:
        image_repository: ghcr.io/org/worker
`

// --- Parse ---

func TestParseValid(t *testing.T) {
	p, err := Parse([]byte(validProjectYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Metadata.Name != "myapi" {
		t.Fatalf("expected name %q, got %q", "myapi", p.Metadata.Name)
	}
	if p.Spec.DisplayName != "My API" {
		t.Fatalf("expected displayName %q, got %q", "My API", p.Spec.DisplayName)
	}
	if len(p.Spec.Environments) != 3 {
		t.Fatalf("expected 3 environments, got %d", len(p.Spec.Environments))
	}
	if len(p.Spec.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(p.Spec.Services))
	}
	if p.Spec.Services[0].Template.Name != "web-service" {
		t.Fatalf("expected template %q, got %q", "web-service", p.Spec.Services[0].Template.Name)
	}
}

func TestParseMinimal(t *testing.T) {
	data := []byte(`
apiVersion: suparship.io/v1alpha1
kind: Project
metadata:
  name: ab
spec:
  environments:
    - name: dev
      order: 1
`)
	p, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse minimal: %v", err)
	}
	if p.Metadata.Name != "ab" {
		t.Fatalf("expected name %q, got %q", "ab", p.Metadata.Name)
	}
}

func TestParseInvalidYAML(t *testing.T) {
	_, err := Parse([]byte(`{{{not yaml`))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

// --- Validate: apiVersion / kind ---

func TestValidateWrongAPIVersion(t *testing.T) {
	p := validProject()
	p.APIVersion = "v2"
	expectValidationError(t, p, "apiVersion")
}

func TestValidateWrongKind(t *testing.T) {
	p := validProject()
	p.Kind = "Service"
	expectValidationError(t, p, "kind")
}

// --- Validate: metadata ---

func TestValidateEmptyName(t *testing.T) {
	p := validProject()
	p.Metadata.Name = ""
	expectValidationError(t, p, "metadata.name")
}

func TestValidateInvalidDNSName(t *testing.T) {
	for _, name := range []string{"A", "foo_bar", "-start", "x"} {
		p := validProject()
		p.Metadata.Name = name
		if err := p.Validate(); err == nil {
			t.Fatalf("expected error for name %q", name)
		}
	}
}

// --- Validate: environments ---

func TestValidateNoEnvironments(t *testing.T) {
	// Empty project environments are valid: the org config provides defaults.
	p := validProject()
	p.Spec.Environments = nil
	if err := p.Validate(); err != nil {
		t.Errorf("expected nil error for empty environments, got: %v", err)
	}
}

func TestValidateEmptyEnvName(t *testing.T) {
	p := validProject()
	p.Spec.Environments[0].Name = ""
	expectValidationError(t, p, "name must not be empty")
}

func TestValidateDuplicateEnvName(t *testing.T) {
	p := validProject()
	p.Spec.Environments = []Environment{
		{Name: "dev", Order: 1},
		{Name: "dev", Order: 2},
	}
	expectValidationError(t, p, "duplicate environment")
}

func TestValidateEnvOrderZero(t *testing.T) {
	p := validProject()
	p.Spec.Environments[0].Order = 0
	expectValidationError(t, p, "order must be >= 1")
}

func TestValidateDuplicateEnvOrder(t *testing.T) {
	p := validProject()
	p.Spec.Environments = []Environment{
		{Name: "dev", Order: 1},
		{Name: "staging", Order: 1},
	}
	expectValidationError(t, p, "duplicate order")
}

// --- Validate: services ---

func TestValidateEmptyServiceName(t *testing.T) {
	p := validProject()
	p.Spec.Services[0].Name = ""
	expectValidationError(t, p, "name must not be empty")
}

func TestValidateDuplicateServiceName(t *testing.T) {
	p := validProject()
	p.Spec.Services = append(p.Spec.Services, p.Spec.Services[0])
	expectValidationError(t, p, "duplicate service")
}

func TestValidateServiceMissingTemplate(t *testing.T) {
	p := validProject()
	p.Spec.Services[0].Template.Name = ""
	expectValidationError(t, p, "template.name")
}

func TestValidateServiceUnknownEnvOverride(t *testing.T) {
	p := validProject()
	p.Spec.Services[0].EnvironmentOverrides = map[string]EnvironmentOverride{
		"fantasy": {Values: map[string]any{"size": "large"}},
	}
	expectValidationError(t, p, "unknown environment")
}

// --- Validate: secret refs ---

func TestValidateEmptySecretRefName(t *testing.T) {
	p := validProject()
	p.Spec.Services[0].SecretRefs = []SecretRef{{Name: "", SecretRef: "x.y"}}
	expectValidationError(t, p, "name must not be empty")
}

func TestValidateEmptySecretRefRef(t *testing.T) {
	p := validProject()
	p.Spec.Services[0].SecretRefs = []SecretRef{{Name: "db", SecretRef: ""}}
	expectValidationError(t, p, "secretRef must not be empty")
}

func TestValidateDuplicateSecretRef(t *testing.T) {
	p := validProject()
	p.Spec.Services[0].SecretRefs = []SecretRef{
		{Name: "db", SecretRef: "a.b"},
		{Name: "db", SecretRef: "c.d"},
	}
	expectValidationError(t, p, "duplicate secretRef")
}

// --- Marshal / round-trip ---

func TestMarshalRoundTrip(t *testing.T) {
	original, err := Parse([]byte(validProjectYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	restored, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse after Marshal: %v", err)
	}

	if restored.Metadata.Name != original.Metadata.Name {
		t.Fatalf("name mismatch: %q vs %q", restored.Metadata.Name, original.Metadata.Name)
	}
	if len(restored.Spec.Environments) != len(original.Spec.Environments) {
		t.Fatalf("environments count: %d vs %d", len(restored.Spec.Environments), len(original.Spec.Environments))
	}
	if len(restored.Spec.Services) != len(original.Spec.Services) {
		t.Fatalf("services count: %d vs %d", len(restored.Spec.Services), len(original.Spec.Services))
	}
	if restored.Spec.Services[0].Template.Version != "1.0.0" {
		t.Fatalf("template version mismatch: %q", restored.Spec.Services[0].Template.Version)
	}

	overrides := restored.Spec.Services[0].EnvironmentOverrides
	if overrides == nil {
		t.Fatal("environment overrides should be preserved")
	}
	prodOverride, ok := overrides["prod"]
	if !ok {
		t.Fatal("prod override should exist")
	}
	if prodOverride.Values["size"] != "large" {
		t.Fatalf("prod size override should be large, got %v", prodOverride.Values["size"])
	}
}

func TestConfigMapName(t *testing.T) {
	p := validProject()
	expected := "suparship-project-myapi"
	if p.ConfigMapName() != expected {
		t.Fatalf("expected %q, got %q", expected, p.ConfigMapName())
	}
}

// --- K8s Store ---

func TestK8sStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	ensureNamespace(t, ctx, client)

	store := NewK8sStore(client)

	p, _ := Parse([]byte(validProjectYAML))

	if err := store.Save(ctx, p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get(ctx, "myapi")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Metadata.Name != "myapi" {
		t.Fatalf("expected name %q, got %q", "myapi", got.Metadata.Name)
	}
	if len(got.Spec.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(got.Spec.Services))
	}
}

func TestK8sStoreList(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	ensureNamespace(t, ctx, client)

	store := NewK8sStore(client)

	p1, _ := Parse([]byte(validProjectYAML))
	p2 := validProject()
	p2.Metadata.Name = "alpha"
	if err := store.Save(ctx, p1); err != nil {
		t.Fatalf("Save p1: %v", err)
	}
	if err := store.Save(ctx, p2); err != nil {
		t.Fatalf("Save p2: %v", err)
	}

	projects, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	if projects[0].Metadata.Name != "alpha" {
		t.Fatalf("expected first project %q (sorted), got %q", "alpha", projects[0].Metadata.Name)
	}
}

func TestK8sStoreUpdate(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	ensureNamespace(t, ctx, client)

	store := NewK8sStore(client)

	p, _ := Parse([]byte(validProjectYAML))
	if err := store.Save(ctx, p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	p.Spec.DisplayName = "Updated API"
	if err := store.Save(ctx, p); err != nil {
		t.Fatalf("Save update: %v", err)
	}

	got, err := store.Get(ctx, "myapi")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Spec.DisplayName != "Updated API" {
		t.Fatalf("expected updated displayName, got %q", got.Spec.DisplayName)
	}
}

func TestK8sStoreGetNotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	store := NewK8sStore(client)

	_, err := store.Get(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent project")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in error, got: %v", err)
	}
}

func TestK8sStoreDelete(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	ensureNamespace(t, ctx, client)

	store := NewK8sStore(client)

	p, _ := Parse([]byte(validProjectYAML))
	if err := store.Save(ctx, p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.Delete(ctx, "myapi"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := store.Get(ctx, "myapi")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestK8sStoreDeleteNotFound(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()

	store := NewK8sStore(client)

	err := store.Delete(ctx, "ghost")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

// --- Helpers ---

func validProject() *Project {
	return &Project{
		APIVersion: CurrentAPIVersion,
		Kind:       ProjectKind,
		Metadata:   ProjectMeta{Name: "myapi"},
		Spec: ProjectSpec{
			DisplayName: "My API",
			Environments: []Environment{
				{Name: "dev", Order: 1},
				{Name: "staging", Order: 2},
			},
			Services: []Service{
				{
					Name:     "api",
					Template: TemplateRef{Name: "web-service"},
					Values:   map[string]any{"size": "small"},
				},
			},
		},
	}
}

func expectValidationError(t *testing.T, p *Project, substr string) {
	t.Helper()
	err := p.Validate()
	if err == nil {
		t.Fatalf("expected validation error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("expected error containing %q, got: %v", substr, err)
	}
}

func ensureNamespace(t *testing.T, ctx context.Context, client *fake.Clientset) {
	t.Helper()
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: Namespace},
	}
	if _, err := client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
}
