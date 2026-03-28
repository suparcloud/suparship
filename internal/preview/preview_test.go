package preview

import "testing"

func TestNewPreview(t *testing.T) {
	p := New("pr-42", "myapi", "api")
	if p.Metadata.Name != "pr-42" {
		t.Fatalf("expected name %q, got %q", "pr-42", p.Metadata.Name)
	}
	if p.Spec.Project != "myapi" {
		t.Fatalf("expected project %q, got %q", "myapi", p.Spec.Project)
	}
	if p.Spec.Service != "api" {
		t.Fatalf("expected service %q, got %q", "api", p.Spec.Service)
	}
	if p.Metadata.CreatedAt == "" {
		t.Fatal("expected createdAt to be set")
	}
	if p.APIVersion != CurrentAPIVersion {
		t.Fatalf("expected apiVersion %q, got %q", CurrentAPIVersion, p.APIVersion)
	}
}

func TestPreviewNamespace(t *testing.T) {
	p := New("pr-42", "myapi", "api")
	want := "myapi-preview-pr-42"
	if got := p.PreviewNamespace(); got != want {
		t.Fatalf("expected namespace %q, got %q", want, got)
	}
}

func TestPreviewNS(t *testing.T) {
	want := "billing-preview-feat-x"
	if got := PreviewNS("billing", "feat-x"); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestPreviewConfigMapName(t *testing.T) {
	p := New("pr-42", "myapi", "api")
	want := "suparship-preview-pr-42"
	if got := p.ConfigMapName(); got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestValidateValid(t *testing.T) {
	p := New("pr-42", "myapi", "api")
	if err := p.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestValidateEmptyName(t *testing.T) {
	p := New("pr-42", "myapi", "api")
	p.Metadata.Name = ""
	if err := p.Validate(); err == nil {
		t.Fatal("expected validation error for empty name")
	}
}

func TestValidateInvalidDNSName(t *testing.T) {
	cases := []string{"PR_42", "UPPER", "-leading", "a"}
	for _, name := range cases {
		p := New(name, "myapi", "api")
		if err := p.Validate(); err == nil {
			t.Fatalf("expected validation error for name %q", name)
		}
	}
}

func TestValidateEmptyProject(t *testing.T) {
	p := New("pr-42", "", "api")
	if err := p.Validate(); err == nil {
		t.Fatal("expected validation error for empty project")
	}
}

func TestValidateEmptyService(t *testing.T) {
	p := New("pr-42", "myapi", "")
	if err := p.Validate(); err == nil {
		t.Fatal("expected validation error for empty service")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	p := New("pr-42", "myapi", "api")
	data, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if parsed.Metadata.Name != p.Metadata.Name {
		t.Fatalf("name: want %q, got %q", p.Metadata.Name, parsed.Metadata.Name)
	}
	if parsed.Spec.Project != p.Spec.Project {
		t.Fatalf("project: want %q, got %q", p.Spec.Project, parsed.Spec.Project)
	}
	if parsed.Spec.Service != p.Spec.Service {
		t.Fatalf("service: want %q, got %q", p.Spec.Service, parsed.Spec.Service)
	}
}

func TestParseInvalid(t *testing.T) {
	_, err := Parse([]byte(`{invalid yaml`))
	if err == nil {
		t.Fatal("expected parse error for invalid YAML")
	}
}

func TestParseValidationFailure(t *testing.T) {
	_, err := Parse([]byte(`
apiVersion: suparship.io/v1alpha1
kind: Preview
metadata:
  name: pr-42
spec:
  project: ""
  service: api
`))
	if err == nil {
		t.Fatal("expected validation error for empty project")
	}
}
