package registrysync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/chartimport"
	"github.com/suparcloud/suparship/internal/tpl/fetcher"
)

// chartPuller pulls a chart .tgz by registry ref. Injectable so tests
// can substitute a fake puller without standing up a real OCI registry.
//
// Implementations must produce the chart bytes (a Helm-packaged .tgz)
// and an outer chart name (the directory name inside the .tgz). The
// outer name lets ParseArchive find Chart.yaml without knowing it
// out-of-band — the pulled file is always named "<chart>-<version>.tgz".
type chartPuller func(ctx context.Context, repoURL, chart, version string, auth basicAuth) ([]byte, error)

// basicAuth carries credentials for a Helm registry. Both fields empty
// means "anonymous". For OCI, helm authenticates via Docker-style
// auths in registry/config.json; for HTTP repos it's just HTTP Basic.
type basicAuth struct {
	username string
	password string
}

// ociFetcher implements fetcher.Fetcher for OCI chart-registry sources.
// One source = one chart pinned at one version; the resulting template
// is registered with the chart bytes attached, so the publisher's
// existing cache+extract path serves it the same way today's BYO-uploaded
// charts are served.
type ociFetcher struct {
	client kubernetes.Interface
	logger *slog.Logger
	pull   chartPuller
}

func newOCIFetcher(client kubernetes.Interface, logger *slog.Logger) *ociFetcher {
	return &ociFetcher{client: client, logger: logger, pull: helmPullChart}
}

// Fetch satisfies fetcher.Fetcher. Source must be tpl.ExternalTemplateRepo
// with Type="oci".
func (f *ociFetcher) Fetch(ctx context.Context, source any) (fetcher.FetchResult, error) {
	repo, ok := source.(tpl.ExternalTemplateRepo)
	if !ok {
		return fetcher.FetchResult{}, fmt.Errorf("ociFetcher: expected tpl.ExternalTemplateRepo, got %T", source)
	}
	if repo.EffectiveType() != tpl.SourceTypeOCI {
		return fetcher.FetchResult{}, fmt.Errorf("ociFetcher: expected source type %q, got %q", tpl.SourceTypeOCI, repo.EffectiveType())
	}
	if err := repo.Validate(); err != nil {
		return fetcher.FetchResult{}, err
	}

	user, pass, err := readOCIAuth(ctx, f.client, repo.ExistingSecret)
	if err != nil {
		return fetcher.FetchResult{}, fmt.Errorf("read auth: %w", err)
	}

	bundle, err := f.pull(ctx, repo.RepoURL, repo.Chart, repo.Version, basicAuth{user, pass})
	if err != nil {
		return fetcher.FetchResult{}, fmt.Errorf("pull %s/%s@%s: %w", repo.RepoURL, repo.Chart, repo.Version, err)
	}

	rt, err := f.resolvePulledChart(bundle)
	if err != nil {
		// Per-template error rather than top-level: one source = one
		// chart, but surfacing it as PartialError keeps the result
		// shape consistent with gitFetcher (multi-template) and lets
		// callers render "0 imported, 1 failed" the same way.
		return fetcher.FetchResult{
			PartialErrs: []fetcher.PartialError{{Name: repo.Chart, Err: err}},
		}, nil
	}
	return fetcher.FetchResult{Templates: []fetcher.ResolvedTemplate{rt}}, nil
}

// resolvePulledChart hands the pulled .tgz to the existing chartimport
// pipeline. If the chart ships a bundled template.yaml at <chart>/template.yaml
// inside the archive, ParseArchive picks it up; otherwise ToTemplate
// generates an inferred template from Chart.yaml + values.schema.json.
func (f *ociFetcher) resolvePulledChart(bundle []byte) (fetcher.ResolvedTemplate, error) {
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

// readOCIAuth reads username/password from the same K8s Secret shape
// gitFetcher uses (data["token"] mapped to "x-access-token", or
// data["username"]+data["password"]). Helm registry auth uses the same
// shape across OCI and HTTP repos via Docker-style auths.
func readOCIAuth(ctx context.Context, client kubernetes.Interface, secretName string) (string, string, error) {
	if secretName == "" {
		return "", "", nil
	}
	if client == nil {
		return "", "", fmt.Errorf("auth secret %q referenced but no cluster client configured", secretName)
	}
	sec, err := client.CoreV1().Secrets(systemNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", "", fmt.Errorf("auth secret %q not found in %s", secretName, systemNamespace)
	}
	if err != nil {
		return "", "", err
	}
	if tok := string(sec.Data["token"]); tok != "" {
		return "x-access-token", tok, nil
	}
	return string(sec.Data["username"]), string(sec.Data["password"]), nil
}

// helmPullChart is the default chartPuller — shells out to `helm pull`
// the same way gitFetcher shells out to `git`. Auth is plumbed via a
// per-call HELM_REGISTRY_CONFIG with the registry's credentials, so
// concurrent pulls against different registries don't collide.
//
// The pulled .tgz is written into a temp dir and read back; helm pull
// has no "to stdout" mode that preserves the .tgz exactly, hence the
// disk round-trip.
func helmPullChart(ctx context.Context, repoURL, chart, version string, auth basicAuth) ([]byte, error) {
	if _, err := exec.LookPath("helm"); err != nil {
		return nil, fmt.Errorf("helm not found on PATH: %w", err)
	}

	tmp, err := os.MkdirTemp("", "suparship-helm-pull-*")
	if err != nil {
		return nil, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmp)

	env := os.Environ()
	if auth.username != "" || auth.password != "" {
		cfgPath, err := writeHelmRegistryConfig(tmp, repoURL, auth)
		if err != nil {
			return nil, fmt.Errorf("write helm registry config: %w", err)
		}
		env = append(env, "HELM_REGISTRY_CONFIG="+cfgPath)
	}

	// Build "<repoURL>/<chart>" — helm pull for OCI takes the full ref.
	// repoURL is "oci://host/path"; we append chart as the last segment.
	ref := strings.TrimSuffix(repoURL, "/") + "/" + chart

	args := []string{"pull", ref, "--version", version, "--destination", tmp}
	cmd := exec.CommandContext(ctx, "helm", args...) //nolint:gosec // controlled args
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("helm pull: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	// helm pull writes "<chart>-<version>.tgz" into the destination.
	expected := filepath.Join(tmp, fmt.Sprintf("%s-%s.tgz", chart, version))
	bytes, err := os.ReadFile(expected)
	if err != nil {
		return nil, fmt.Errorf("read pulled chart at %s: %w (helm output: %s)", expected, err, strings.TrimSpace(string(out)))
	}
	return bytes, nil
}

// writeHelmRegistryConfig produces a Docker-format auths file scoped to
// repoURL's host. Helm reads it via HELM_REGISTRY_CONFIG to
// authenticate the pull. Returns the file path.
func writeHelmRegistryConfig(dir, repoURL string, auth basicAuth) (string, error) {
	host := registryHostFromURL(repoURL)
	if host == "" {
		return "", fmt.Errorf("could not extract registry host from %q", repoURL)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(auth.username + ":" + auth.password))
	cfg := map[string]any{
		"auths": map[string]any{
			host: map[string]any{
				"auth": encoded,
			},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "registry-config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// registryHostFromURL extracts the registry host (e.g. "ghcr.io") from
// an OCI or HTTPS URL. Strips the scheme, then drops everything after
// the first "/" so subdirectories under the registry don't pollute the
// auth-config key.
func registryHostFromURL(repoURL string) string {
	for _, scheme := range []string{"oci://", "https://", "http://"} {
		if strings.HasPrefix(repoURL, scheme) {
			rest := repoURL[len(scheme):]
			if idx := strings.Index(rest, "/"); idx >= 0 {
				return rest[:idx]
			}
			return rest
		}
	}
	// Already a bare host, or a malformed URL — return as-is.
	if idx := strings.Index(repoURL, "/"); idx >= 0 {
		return repoURL[:idx]
	}
	return repoURL
}
