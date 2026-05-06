package registrysync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/suparcloud/suparship/internal/tpl"
)

func TestChartMuseumFetcher_PullsAndParses(t *testing.T) {
	const chartName = "web-service"
	const version = "1.2.0"
	tgz := fakeChartTGZ(t, chartName, version, bundledTemplateYAMLForChart)

	// Stand up a minimal HTTP server that serves the .tgz at the
	// canonical "<repoURL>/<chart>-<version>.tgz" path.
	wantPath := "/" + chartName + "-" + version + ".tgz"
	var sawAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wantPath {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if h := r.Header.Get("Authorization"); h != "" {
			sawAuth = h
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(tgz)
	}))
	defer srv.Close()

	f := newChartMuseumFetcher(k8sfake.NewClientset(), nil)
	repo := tpl.ExternalTemplateRepo{
		Name:    "demo",
		Type:    tpl.SourceTypeChartMuseum,
		RepoURL: srv.URL,
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
	if sawAuth != "" {
		t.Errorf("public repo should not send Authorization, got %q", sawAuth)
	}
	if len(res.Templates[0].ChartBytes) == 0 {
		t.Error("ResolvedTemplate.ChartBytes empty; expected pulled .tgz")
	}
}

func TestChartMuseumFetcher_HTTPErrorSurfacesAsTopLevel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such chart", http.StatusNotFound)
	}))
	defer srv.Close()

	f := newChartMuseumFetcher(k8sfake.NewClientset(), nil)
	_, err := f.Fetch(context.Background(), tpl.ExternalTemplateRepo{
		Name:    "demo",
		Type:    tpl.SourceTypeChartMuseum,
		RepoURL: srv.URL,
		Chart:   "missing",
		Version: "0.0.0",
	})
	if err == nil {
		t.Fatal("expected top-level error for HTTP 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q should mention 404", err)
	}
}

func TestChartMuseumFetcher_ValidationErrors(t *testing.T) {
	f := newChartMuseumFetcher(k8sfake.NewClientset(), nil)
	cases := []struct {
		name string
		repo tpl.ExternalTemplateRepo
	}{
		{
			name: "missing chart",
			repo: tpl.ExternalTemplateRepo{Name: "x", Type: tpl.SourceTypeChartMuseum, RepoURL: "https://r", Version: "1"},
		},
		{
			name: "missing version",
			repo: tpl.ExternalTemplateRepo{Name: "x", Type: tpl.SourceTypeChartMuseum, RepoURL: "https://r", Chart: "c"},
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

func TestChartTgzURL(t *testing.T) {
	cases := []struct {
		repoURL, chart, version, want string
		wantErr                       bool
	}{
		{"https://charts.acme.io", "web-service", "1.2.0", "https://charts.acme.io/web-service-1.2.0.tgz", false},
		{"https://charts.acme.io/", "web-service", "1.2.0", "https://charts.acme.io/web-service-1.2.0.tgz", false},
		{"https://charts.acme.io/path", "x", "0.1.0", "https://charts.acme.io/path/x-0.1.0.tgz", false},
		{"http://localhost:8080", "x", "0.1.0", "http://localhost:8080/x-0.1.0.tgz", false},
		{"oci://ghcr.io/x", "y", "1", "", true}, // wrong scheme
		{"ftp://nope", "x", "1", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.repoURL, func(t *testing.T) {
			got, err := chartTgzURL(tc.repoURL, tc.chart, tc.version)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got url=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
