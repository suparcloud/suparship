package registrysync

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/chartimport"
	"github.com/suparcloud/suparship/internal/tpl/fetcher"
)

// chartMuseumFetcher implements fetcher.Fetcher for classic Helm HTTP
// repositories (ChartMuseum, GitHub Pages-served repos, JFrog, etc.).
//
// Convention: charts are exposed at "<repoURL>/<chart>-<version>.tgz" —
// the URL pattern Helm itself uses when resolving an entry from
// index.yaml. We don't fetch index.yaml at all (no listing requirement
// today), so the operator must know the chart name + version up front,
// same as the OCI flow.
//
// Auth: HTTP Basic, plumbed from the same K8s Secret shape git+OCI use
// (data["token"] mapped to "x-access-token", or username + password).
type chartMuseumFetcher struct {
	client     kubernetes.Interface
	logger     *slog.Logger
	httpClient *http.Client
}

func newChartMuseumFetcher(client kubernetes.Interface, logger *slog.Logger) *chartMuseumFetcher {
	return &chartMuseumFetcher{
		client: client,
		logger: logger,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Fetch satisfies fetcher.Fetcher. Source must be tpl.ExternalTemplateRepo
// with Type="chartmuseum".
func (f *chartMuseumFetcher) Fetch(ctx context.Context, source any) (fetcher.FetchResult, error) {
	repo, ok := source.(tpl.ExternalTemplateRepo)
	if !ok {
		return fetcher.FetchResult{}, fmt.Errorf("chartMuseumFetcher: expected tpl.ExternalTemplateRepo, got %T", source)
	}
	if repo.EffectiveType() != tpl.SourceTypeChartMuseum {
		return fetcher.FetchResult{}, fmt.Errorf("chartMuseumFetcher: expected source type %q, got %q", tpl.SourceTypeChartMuseum, repo.EffectiveType())
	}
	if err := repo.Validate(); err != nil {
		return fetcher.FetchResult{}, err
	}

	user, pass, err := readOCIAuth(ctx, f.client, repo.ExistingSecret)
	if err != nil {
		return fetcher.FetchResult{}, fmt.Errorf("read auth: %w", err)
	}

	tgzURL, err := chartTgzURL(repo.RepoURL, repo.Chart, repo.Version)
	if err != nil {
		return fetcher.FetchResult{}, err
	}
	bundle, err := f.httpFetch(ctx, tgzURL, user, pass)
	if err != nil {
		return fetcher.FetchResult{}, fmt.Errorf("fetch %s: %w", tgzURL, err)
	}

	rt, err := resolvePulledChart(bundle)
	if err != nil {
		return fetcher.FetchResult{
			PartialErrs: []fetcher.PartialError{{Name: repo.Chart, Err: err}},
		}, nil
	}
	return fetcher.FetchResult{Templates: []fetcher.ResolvedTemplate{rt}}, nil
}

// httpFetch GETs url with optional HTTP Basic auth and returns the body.
// Caps the read at maxArchiveSize via a LimitReader so a malicious or
// misconfigured server can't OOM the sync goroutine.
func (f *chartMuseumFetcher) httpFetch(ctx context.Context, url, user, pass string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := f.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Read a tiny preview of the body so the error message gives
		// the operator something actionable (e.g. "404 not found",
		// "401 unauthorized") without spewing the full server response.
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, chartmuseumMaxArchiveSize))
}

// chartmuseumMaxArchiveSize caps the chart .tgz at 32 MiB. ChartMuseum
// itself has no size limit, but suparship's downstream ConfigMap
// storage is bounded by Kubernetes binaryData (1 MiB after gzip-encoding
// loss; effectively a few MiB for well-built charts). 32 MiB leaves
// headroom for kustomize-style bundles while still stopping a hostile
// server from streaming megabytes of garbage.
const chartmuseumMaxArchiveSize = 32 * 1024 * 1024

// chartTgzURL builds the canonical "<repoURL>/<chart>-<version>.tgz"
// download URL for a Helm HTTP repo. Validates the inputs as a
// well-formed URL so a misconfigured RepoURL produces a clean error
// rather than a confusing GET failure.
func chartTgzURL(repoURL, chart, version string) (string, error) {
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") {
		return "", fmt.Errorf("chartmuseum repoURL must start with http:// or https://, got %q", repoURL)
	}
	base, err := url.Parse(strings.TrimSuffix(repoURL, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid repoURL %q: %w", repoURL, err)
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/" + fmt.Sprintf("%s-%s.tgz", chart, version)
	return base.String(), nil
}

// resolvePulledChart hands a pulled .tgz to the existing chartimport
// pipeline. Shared between the OCI and ChartMuseum fetchers — both
// produce a chart .tgz; chartimport.ParseArchive picks up a bundled
// template.yaml when present and falls back to inferred metadata.
func resolvePulledChart(bundle []byte) (fetcher.ResolvedTemplate, error) {
	arc, err := chartimport.ParseArchive(bundle)
	if err != nil {
		return fetcher.ResolvedTemplate{}, fmt.Errorf("parse: %w", err)
	}
	tmpl, err := chartimport.ToTemplate(arc)
	if err != nil {
		return fetcher.ResolvedTemplate{}, fmt.Errorf("to template: %w", err)
	}
	return fetcher.ResolvedTemplate{Template: tmpl, ChartBytes: bundle}, nil
}
