package registrysync_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/registrysync"
)

// buildFakeChartTGZ produces a minimal Helm chart .tgz suitable for
// chartimport.ParseArchive. Mirrors the helper in oci_test.go but lives
// in the external test package so the gittgz e2e test (which uses the
// existing registrysync_test fixtures) can reach it.
func buildFakeChartTGZ(t *testing.T, chartName, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	chartYAML := []byte("apiVersion: v2\nname: " + chartName + "\nversion: " + version + "\n")
	hdr := &tar.Header{
		Name:     chartName + "/Chart.yaml",
		Mode:     0o644,
		Size:     int64(len(chartYAML)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(chartYAML); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

// TestSyncOne_GitTgzPullsAndPersists verifies the gittgz fetcher: a
// chart .tgz checked into a git repo at a known path, cloned and
// fed through the same chartimport pipeline as OCI/ChartMuseum.
func TestSyncOne_GitTgzPullsAndPersists(t *testing.T) {
	requireGit(t)

	// Build a real chart .tgz, then commit it to a fresh git repo.
	const chartName = "web-service"
	const version = "1.2.0"
	tgz := buildFakeChartTGZ(t, chartName, version)

	repoDir := t.TempDir()
	gitInit(t, repoDir)
	tgzPath := filepath.Join(repoDir, "charts", chartName+"-"+version+".tgz")
	if err := os.MkdirAll(filepath.Dir(tgzPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(tgzPath, tgz, 0o644); err != nil {
		t.Fatalf("write tgz: %v", err)
	}
	gitCommit(t, repoDir, "add chart tgz")

	client := k8sfake.NewClientset()
	eng := &registrysync.Engine{Client: client}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "demo",
		Type:    tpl.SourceTypeGitTgz,
		RepoURL: repoDir,
		Ref:     "main",
		Path:    "charts/" + chartName + "-" + version + ".tgz",
	}, nil)
	if res.Err != nil {
		t.Fatalf("SyncOne: %v", res.Err)
	}
	if len(res.Templates) != 1 || res.Templates[0] != chartName {
		t.Fatalf("expected [%s], got %v", chartName, res.Templates)
	}

	// ConfigMap exists with chart bytes attached (gittgz is not
	// external-mode — the publisher serves it via the inline path).
	cm, err := client.CoreV1().ConfigMaps("suparship-system").Get(
		context.Background(), "suparship-template-"+chartName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get configmap: %v", err)
	}
	if len(cm.BinaryData["chart.tgz"]) == 0 {
		t.Error("ConfigMap missing chart.tgz")
	}
}

func TestSyncOne_GitTgz_RejectsMissingPath(t *testing.T) {
	// Validation should fire before any clone, so we don't actually
	// need a real repo on disk for this case.
	eng := &registrysync.Engine{Client: k8sfake.NewClientset()}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "demo",
		Type:    tpl.SourceTypeGitTgz,
		RepoURL: "https://example.com/repo.git",
		// Path intentionally empty.
	}, nil)
	if res.Err == nil {
		t.Fatal("expected validation error for missing Path")
	}
}
