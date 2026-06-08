package gitops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/gitops"
)

func TestChartVersionDir(t *testing.T) {
	cases := map[string]string{
		"":             "latest",
		"1.2.3":        "1.2.3",
		"v1.2.3":       "v1.2.3",
		"1.0.0-rc.1":   "1.0.0-rc.1",
		"1.0.0+build5": "1.0.0-build5", // '+' sanitized
		"  ":           "latest",       // whitespace-only → no usable chars
	}
	for in, want := range cases {
		if got := gitops.ChartVersionDirForTest(in); got != want {
			t.Errorf("chartVersionDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChartPathFor(t *testing.T) {
	if got := gitops.ChartPathForTest("web-service", "1.2.3"); got != "web-service/1.2.3" {
		t.Errorf("chartPathFor pinned = %q", got)
	}
	if got := gitops.ChartPathForTest("web-service", ""); got != "web-service/latest" {
		t.Errorf("chartPathFor unpinned = %q", got)
	}
}

func TestBuildArgoAppSet_UsesVersionScopedChartPath(t *testing.T) {
	as := gitops.BuildArgoAppSet(gitops.AppSetEnv{EnvName: "staging", ClusterServer: "https://k"}, "https://git/repo.git", gitops.AppSetOptions{})
	// The chart source must reference the per-app {{chartPath}} param, not the
	// version-less {{template}}, so different versions resolve to distinct dirs.
	var chartPath string
	for _, s := range as.Spec.Template.Spec.Sources {
		if strings.Contains(s.Path, "charts/") {
			chartPath = s.Path
		}
	}
	if !strings.Contains(chartPath, "charts/{{chartPath}}") {
		t.Errorf("appset chart source path = %q, want charts/{{chartPath}}", chartPath)
	}
	if strings.Contains(chartPath, "{{template}}") {
		t.Errorf("appset still uses version-less {{template}}: %q", chartPath)
	}
}

func TestAppMetadata_SerializesChartPath(t *testing.T) {
	b, err := yaml.Marshal(gitops.AppMetadata{Name: "web", Project: "demo", Template: "web-service", ChartPath: "web-service/1.2.3", Namespace: "demo-web-staging"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "chartPath: web-service/1.2.3") {
		t.Errorf("app.yaml missing chartPath:\n%s", b)
	}
}

// Cross-check the original bug: two apps pinning different versions of one
// template must materialize into distinct chart dirs, not overwrite a shared one.
func TestSyncChart_DistinctVersionsCoexist(t *testing.T) {
	repoDir := t.TempDir()
	v1 := buildPackagedChart(t, "web", map[string]string{"Chart.yaml": "apiVersion: v2\nname: web\nversion: 1.0.0\n"})
	v2 := buildPackagedChart(t, "web", map[string]string{"Chart.yaml": "apiVersion: v2\nname: web\nversion: 2.0.0\n"})
	fetcher := &versionAwareFetcher{byVersion: map[string][]byte{"1.0.0": v1, "2.0.0": v2}}

	p, err := gitops.NewPublisher(gitops.PublisherConfig{RepoURL: "http://localhost/fake.git", ChartFetcher: fetcher})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SyncChartForTest(context.Background(), repoDir, "web", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := p.SyncChartForTest(context.Background(), repoDir, "web", "2.0.0"); err != nil {
		t.Fatal(err)
	}

	read := func(version string) string {
		b, err := os.ReadFile(filepath.Join(repoDir, "charts", "web", version, "Chart.yaml"))
		if err != nil {
			t.Fatalf("read %s: %v", version, err)
		}
		return string(b)
	}
	if !strings.Contains(read("1.0.0"), "version: 1.0.0") {
		t.Error("charts/web/1.0.0 should hold v1, not be overwritten")
	}
	if !strings.Contains(read("2.0.0"), "version: 2.0.0") {
		t.Error("charts/web/2.0.0 should hold v2")
	}
}
