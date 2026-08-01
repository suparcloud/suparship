package secrets

import "testing"

func TestDedupProjectPrefix(t *testing.T) {
	cases := []struct{ project, app, want string }{
		{"foo", "foo-bar", "foo-bar"},   // already prefixed
		{"foo", "foo", "foo"},           // app == project
		{"foo", "bar", "foo-bar"},       // prepend
		{"foo", "foobar", "foo-foobar"}, // "foobar" not a "foo-" boundary
		{"", "bar", "bar"},              // empty project
		{"voiceai", "voiceai-lk-sh", "voiceai-lk-sh"},
	}
	for _, c := range cases {
		if got := DedupProjectPrefix(c.project, c.app); got != c.want {
			t.Errorf("DedupProjectPrefix(%q,%q) = %q, want %q", c.project, c.app, got, c.want)
		}
	}
}

func TestRenderPattern_ProjectAppToken(t *testing.T) {
	p := NamingParams{Project: "foo", App: "foo-bar", Cluster: "blah"}
	if got := RenderPattern("{projectApp}-{cluster}", p); got != "foo-bar-blah" {
		t.Errorf("{projectApp} folded = %q, want foo-bar-blah", got)
	}
	// {project}/{app} remain non-deduped for custom patterns.
	if got := RenderPattern("{project}-{app}-{cluster}", p); got != "foo-foo-bar-blah" {
		t.Errorf("{project}-{app} = %q, want foo-foo-bar-blah (no dedup)", got)
	}
}
