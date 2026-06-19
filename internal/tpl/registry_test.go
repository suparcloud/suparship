package tpl

import "testing"

func TestTemplateRegistry_FindSource(t *testing.T) {
	reg := &TemplateRegistry{
		Sources: []TemplateSource{
			{Name: "web-service", Origin: "builtin", Version: "1.0.0"},
			{Name: "worker", Origin: "builtin", Version: "1.0.0"},
		},
	}

	src := reg.FindSource("web-service")
	if src == nil {
		t.Fatal("expected to find web-service")
	}
	if src.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", src.Version)
	}

	if reg.FindSource("nonexistent") != nil {
		t.Error("expected nil for nonexistent template")
	}
}

func TestTemplateRegistry_UpsertSource_New(t *testing.T) {
	reg := &TemplateRegistry{
		Sources: []TemplateSource{
			{Name: "web-service", Origin: "builtin", Version: "1.0.0"},
		},
	}

	reg.UpsertSource(TemplateSource{Name: "worker", Origin: "builtin", Version: "1.0.0"})

	if len(reg.Sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(reg.Sources))
	}
	if reg.FindSource("worker") == nil {
		t.Error("expected worker to be added")
	}
}

func TestTemplateRegistry_UpsertSource_Update(t *testing.T) {
	reg := &TemplateRegistry{
		Sources: []TemplateSource{
			{Name: "web-service", Origin: "builtin", Version: "1.0.0"},
		},
	}

	reg.UpsertSource(TemplateSource{Name: "web-service", Origin: "builtin", Version: "2.0.0"})

	if len(reg.Sources) != 1 {
		t.Fatalf("expected 1 source (updated), got %d", len(reg.Sources))
	}
	src := reg.FindSource("web-service")
	if src.Version != "2.0.0" {
		t.Errorf("version = %q, want 2.0.0", src.Version)
	}
}

func TestExternalTemplateRepo_GitChartsValidate(t *testing.T) {
	// A gitcharts source needs only name + repoURL; path/ref are optional
	// (path defaults to charts/, ref to main).
	r := &ExternalTemplateRepo{Name: "charts", Type: SourceTypeGitCharts, RepoURL: "https://example.com/charts.git"}
	if err := r.Validate(); err != nil {
		t.Fatalf("gitcharts with name+repoURL should validate, got %v", err)
	}
	if r.EffectiveType() != SourceTypeGitCharts {
		t.Errorf("EffectiveType = %q, want %q", r.EffectiveType(), SourceTypeGitCharts)
	}
	// Missing repoURL is still rejected.
	bad := &ExternalTemplateRepo{Name: "charts", Type: SourceTypeGitCharts}
	if err := bad.Validate(); err == nil {
		t.Error("expected error when repoURL is missing")
	}
}
