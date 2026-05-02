package registrysync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"testing"

	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/tpl"
)

// fakeChartTGZ produces a minimal Helm-shaped chart .tgz for tests so
// the ociFetcher's chartimport pipeline has something well-formed to
// process. Bundles a Chart.yaml and an optional template.yaml at the
// expected positions.
func fakeChartTGZ(t *testing.T, chartName, version string, bundledTemplateYAML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	chartYAML := []byte("apiVersion: v2\nname: " + chartName + "\nversion: " + version + "\n")
	if err := writeTarFile(tw, chartName+"/Chart.yaml", chartYAML); err != nil {
		t.Fatalf("write Chart.yaml: %v", err)
	}
	if bundledTemplateYAML != "" {
		if err := writeTarFile(tw, chartName+"/template.yaml", []byte(bundledTemplateYAML)); err != nil {
			t.Fatalf("write template.yaml: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

func writeTarFile(tw *tar.Writer, name string, content []byte) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}

const bundledTemplateYAMLForChart = `apiVersion: suparship.io/v1alpha1
kind: Template
metadata:
  name: web-service
  version: "1.2.0"
spec:
  title: Web Service (bundled)
  description: Shipped inside the chart .tgz.
  category: web
  engine:
    type: helm
    # No chart field — bundled mode.
  inputs: []
`

// TestOCIFetcher_PullsAndExtractsBundledTemplate is the happy path: a
// chart that ships its own template.yaml inside the .tgz. The
// chartimport pipeline picks up the bundled template; the persisted
// ConfigMap carries chart.tgz bytes for the publisher.
func TestOCIFetcher_PullsAndExtractsBundledTemplate(t *testing.T) {
	const chartName = "web-service"
	const version = "1.2.0"
	tgz := fakeChartTGZ(t, chartName, version, bundledTemplateYAMLForChart)

	client := k8sfake.NewClientset()
	f := &ociFetcher{
		client: client,
		logger: nil, // tolerated by gitFetcher.logger()-style fallback (slog.Default)
		pull: func(_ context.Context, repoURL, name, ver string, _ basicAuth) ([]byte, error) {
			if repoURL != "oci://ghcr.io/myorg/charts" || name != chartName || ver != version {
				t.Errorf("puller called with wrong args: repo=%q name=%q ver=%q", repoURL, name, ver)
			}
			return tgz, nil
		},
	}

	repo := tpl.ExternalTemplateRepo{
		Name:    "demo",
		Type:    tpl.SourceTypeOCI,
		RepoURL: "oci://ghcr.io/myorg/charts",
		Chart:   chartName,
		Version: version,
	}
	res, err := f.Fetch(context.Background(), repo)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.Templates) != 1 {
		t.Fatalf("expected 1 template, got %d (partial errs=%v)", len(res.Templates), res.PartialErrs)
	}
	rt := res.Templates[0]
	if rt.Template.Metadata.Name != chartName {
		t.Errorf("template name = %q, want %q", rt.Template.Metadata.Name, chartName)
	}
	if len(rt.ChartBytes) == 0 {
		t.Error("ResolvedTemplate.ChartBytes is empty; OCI-pulled charts must carry bytes for the publisher")
	}
	// Bundled-template-yaml mode: engine.chart should be omitted (zero
	// ChartLocator) since the chart IS the artifact.
	if !rt.Template.Spec.Engine.Chart.IsBundled() {
		t.Errorf("bundled template's engine.chart should be IsBundled(); got %+v", rt.Template.Spec.Engine.Chart)
	}
}

func TestOCIFetcher_RejectsWrongSourceType(t *testing.T) {
	f := &ociFetcher{client: k8sfake.NewClientset()}
	_, err := f.Fetch(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "demo",
		Type:    tpl.SourceTypeGit, // wrong type
		RepoURL: "https://example.com/repo.git",
		Path:    "templates",
	})
	if err == nil {
		t.Fatal("expected error when ociFetcher receives a non-oci source")
	}
}

func TestOCIFetcher_ValidationErrors(t *testing.T) {
	f := &ociFetcher{client: k8sfake.NewClientset()}
	cases := []struct {
		name string
		repo tpl.ExternalTemplateRepo
	}{
		{
			name: "missing chart",
			repo: tpl.ExternalTemplateRepo{Name: "x", Type: tpl.SourceTypeOCI, RepoURL: "oci://r", Version: "1"},
		},
		{
			name: "missing version",
			repo: tpl.ExternalTemplateRepo{Name: "x", Type: tpl.SourceTypeOCI, RepoURL: "oci://r", Chart: "c"},
		},
		{
			name: "missing repoURL",
			repo: tpl.ExternalTemplateRepo{Name: "x", Type: tpl.SourceTypeOCI, Chart: "c", Version: "1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.Fetch(context.Background(), tc.repo)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
		})
	}
}

func TestEngine_DispatchesByType(t *testing.T) {
	// chartmuseum is reserved but not implemented — dispatch should
	// surface ErrUnsupportedSourceType rather than panicking.
	eng := &Engine{Client: k8sfake.NewClientset()}
	res := eng.SyncOne(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "x",
		Type:    tpl.SourceTypeChartMuseum,
		RepoURL: "https://charts.example.com",
		Chart:   "c",
		Version: "1.0.0",
	})
	if res.Err == nil {
		t.Fatal("expected dispatch error for unsupported source type")
	}
}

func TestRegistryHostFromURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"oci://ghcr.io/myorg/charts", "ghcr.io"},
		{"oci://ghcr.io", "ghcr.io"},
		{"https://charts.acme.io/v2", "charts.acme.io"},
		{"http://localhost:5000/r", "localhost:5000"},
		{"ghcr.io/myorg", "ghcr.io"},
		{"plainhost", "plainhost"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := registryHostFromURL(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
