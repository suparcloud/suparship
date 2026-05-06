package gitops_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suparcloud/suparship/internal/gitops"
)

// fakeChartFetcher returns canned chart bytes for one template name. Other
// names produce a "not found" (nil bytes) result so tests can assert the
// publisher treats absent bundles as a no-op.
type fakeChartFetcher struct {
	templateName string
	data         []byte
	err          error
}

func (f *fakeChartFetcher) LoadChartBundle(_ context.Context, name, _ string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if name == f.templateName {
		return f.data, nil
	}
	return nil, nil
}

// buildPackagedChart synthesises a tarball matching `helm package` layout:
// every file lives under "<chartName>/...".
func buildPackagedChart(t *testing.T, chartName string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for path, content := range files {
		hdr := &tar.Header{
			Name:     chartName + "/" + path,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("Write: %v", err)
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

func TestSyncChart_LocalDiskTakesPrecedence(t *testing.T) {
	repoDir := t.TempDir()
	templatesDir := t.TempDir()
	// Local chart: charts/{name}/ should mirror this directory.
	chartDir := filepath.Join(templatesDir, "demo", "chart")
	if err := os.MkdirAll(chartDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Fetcher returns DIFFERENT bytes — if the local path is preferred we
	// should never see these in the result.
	fetcher := &fakeChartFetcher{
		templateName: "demo",
		data:         buildPackagedChart(t, "demo", map[string]string{"Chart.yaml": "from: cluster\n"}),
	}

	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL: "http://localhost/fake.git",
		TemplatesDir: templatesDir,
		ChartFetcher: fetcher,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if err := p.SyncChartForTest(context.Background(), repoDir, "demo", ""); err != nil {
		t.Fatalf("SyncChart: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(repoDir, "charts", "demo", "Chart.yaml"))
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(got) != "name: demo\n" {
		t.Errorf("expected local-disk content, got: %q", got)
	}
}

func TestSyncChart_FallsBackToClusterBundle(t *testing.T) {
	repoDir := t.TempDir()

	tgz := buildPackagedChart(t, "demo", map[string]string{
		"Chart.yaml":             "apiVersion: v2\nname: demo\nversion: 1.0.0\n",
		"values.yaml":            "k: v\n",
		"templates/deploy.yaml":  "apiVersion: apps/v1\nkind: Deployment\n",
	})
	fetcher := &fakeChartFetcher{templateName: "demo", data: tgz}

	// No TemplatesDir on disk — the fetcher path is the only resolution.
	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:      "http://localhost/fake.git",
		ChartFetcher: fetcher,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if err := p.SyncChartForTest(context.Background(), repoDir, "demo", ""); err != nil {
		t.Fatalf("SyncChart: %v", err)
	}

	for _, want := range []string{
		"charts/demo/Chart.yaml",
		"charts/demo/values.yaml",
		"charts/demo/templates/deploy.yaml",
	} {
		if _, err := os.Stat(filepath.Join(repoDir, want)); err != nil {
			t.Errorf("expected %s in repo, got: %v", want, err)
		}
	}
}

func TestSyncChart_NoBundleNoError(t *testing.T) {
	repoDir := t.TempDir()
	fetcher := &fakeChartFetcher{templateName: "other", data: nil}

	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:      "http://localhost/fake.git",
		ChartFetcher: fetcher,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	// Asking for a template the fetcher doesn't know about must be a no-op,
	// not an error — preserves the prior "skip silently" contract for charts
	// that ship out-of-band.
	if err := p.SyncChartForTest(context.Background(), repoDir, "demo", ""); err != nil {
		t.Fatalf("SyncChart returned error for missing bundle: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "charts", "demo")); !os.IsNotExist(err) {
		t.Error("expected no chart dir to be created")
	}
}

func TestSyncChart_FetcherErrorPropagates(t *testing.T) {
	repoDir := t.TempDir()
	wantErr := errors.New("backend down")
	fetcher := &fakeChartFetcher{err: wantErr}

	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:      "http://localhost/fake.git",
		ChartFetcher: fetcher,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if err := p.SyncChartForTest(context.Background(), repoDir, "demo", ""); err == nil {
		t.Fatal("expected fetcher error to propagate")
	}
}

// versionAwareFetcher returns different bytes per version so a test can
// verify the publisher passed the right version through to the fetcher.
type versionAwareFetcher struct {
	byVersion map[string][]byte
	calls     []string
}

func (f *versionAwareFetcher) LoadChartBundle(_ context.Context, name, version string) ([]byte, error) {
	f.calls = append(f.calls, name+"@"+version)
	return f.byVersion[version], nil
}

// TestSyncChart_HonoursPinnedVersion guards against the regression where
// a templates-registry bump silently re-versions every running app's
// chart bytes. Apps pin Template.Version at create time; the publisher
// must pass that pin through to ChartFetcher so an app on v1.0.0 keeps
// getting v1.0.0 even after the alias moves on.
func TestSyncChart_HonoursPinnedVersion(t *testing.T) {
	repoDir := t.TempDir()

	v1 := buildPackagedChart(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.0.0\n",
	})
	v2 := buildPackagedChart(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 2.0.0\n",
	})
	fetcher := &versionAwareFetcher{byVersion: map[string][]byte{
		"1.0.0": v1,
		"2.0.0": v2,
		// "" simulates the alias — should NOT be hit when an app has
		// a pinned version.
	}}

	p, err := gitops.NewPublisher(gitops.PublisherConfig{
		RepoURL:      "http://localhost/fake.git",
		ChartFetcher: fetcher,
	})
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if err := p.SyncChartForTest(context.Background(), repoDir, "demo", "1.0.0"); err != nil {
		t.Fatalf("SyncChart: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(repoDir, "charts", "demo", "Chart.yaml"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "version: 1.0.0") {
		t.Errorf("expected v1.0.0 chart, got:\n%s", got)
	}
	if len(fetcher.calls) != 1 || fetcher.calls[0] != "demo@1.0.0" {
		t.Errorf("fetcher should be called with explicit version; calls=%v", fetcher.calls)
	}
}
