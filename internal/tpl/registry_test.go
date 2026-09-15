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

func TestTemplateRegistry_PruneOrphanSources(t *testing.T) {
	reg := &TemplateRegistry{
		External: []ExternalTemplateRepo{{Name: "live", RepoURL: "https://example.com/live.git"}},
		Sources: []TemplateSource{
			{Name: "web", Origin: "external", ExternalRepo: "live"},
			{Name: "worker", Origin: "external", ExternalRepo: "deleted-with-typo"},
			{Name: "cronjob", Origin: "external", ExternalRepo: "deleted-with-typo"},
			{Name: "byo-upload", Origin: "cluster"},
			{Name: "web-service", Origin: "builtin", Version: "1.0.0"},
		},
	}

	removed := reg.PruneOrphanSources()

	if len(removed) != 2 || removed[0].Name != "worker" || removed[1].Name != "cronjob" {
		t.Fatalf("removed = %+v, want the two rows of the deleted repo", removed)
	}
	var kept []string
	for _, s := range reg.Sources {
		kept = append(kept, s.Name)
	}
	want := []string{"web", "byo-upload", "web-service"}
	if len(kept) != len(want) {
		t.Fatalf("kept = %v, want %v", kept, want)
	}
	for i := range want {
		if kept[i] != want[i] {
			t.Errorf("kept[%d] = %q, want %q", i, kept[i], want[i])
		}
	}

	// Idempotent: a clean registry loses nothing.
	if again := reg.PruneOrphanSources(); len(again) != 0 {
		t.Errorf("second prune removed %+v, want nothing", again)
	}
}

func TestQualifiedTemplateName(t *testing.T) {
	if got := QualifiedTemplateName("acme", "web"); got != "acme.web" {
		t.Fatalf("QualifiedTemplateName = %q, want acme.web", got)
	}
	src, chart, ok := SplitQualifiedTemplateName("acme.web-service")
	if !ok || src != "acme" || chart != "web-service" {
		t.Errorf("Split(acme.web-service) = %q,%q,%v", src, chart, ok)
	}
	for _, bare := range []string{"web", ".web", "acme.", ""} {
		if _, _, ok := SplitQualifiedTemplateName(bare); ok {
			t.Errorf("Split(%q) reported a namespaced name", bare)
		}
	}
}

func TestExternalTemplateRepo_Validate_NamespacedName(t *testing.T) {
	ok := ExternalTemplateRepo{Name: "acme-charts", RepoURL: "https://example.com/c.git", Namespaced: true}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid namespaced source rejected: %v", err)
	}
	for _, bad := range []string{"Acme", "acme_charts", "acme.charts", "-acme"} {
		r := ExternalTemplateRepo{Name: bad, RepoURL: "https://example.com/c.git", Namespaced: true}
		if err := r.Validate(); err == nil {
			t.Errorf("namespaced source name %q accepted", bad)
		}
		// The same name is fine while un-namespaced: it never prefixes anything.
		r.Namespaced = false
		if err := r.Validate(); err != nil {
			t.Errorf("un-namespaced source name %q rejected: %v", bad, err)
		}
	}
}
