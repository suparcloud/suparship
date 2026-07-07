// Package registrysync clones external template repositories, packages each
// chart it finds, and persists the result through the same import path used
// by the BYO-Helm-chart wizard (chartimport.ParseArchive → kube.SaveTemplate).
//
// Why Git-only for now: most public Helm chart libraries are Git-hosted
// (ArtifactHub indexes Git repos, GitHub Pages-served Helm repos are
// Git-backed, internal mono-repos are Git). OCI is a sensible follow-up but
// brings in another auth path; keeping the MVP narrow makes the engine
// trivially testable with a local --bare repo.
package registrysync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/chartimport"
	"github.com/suparcloud/suparship/internal/tpl/fetcher"
)

// systemNamespace is where suparship reads its own ConfigMaps and Secrets
// (auth secrets for private template repos).
const systemNamespace = "suparship-system"

// SyncResult captures the per-source outcome of one sync pass. Callers
// record these on the TemplateRegistry so the UI can show status.
type SyncResult struct {
	// SourceName is the ExternalTemplateRepo.Name that was synced.
	SourceName string
	// Templates lists the template names imported on this pass. Always
	// populated even on partial success.
	Templates []string
	// SyncedAt is the wall-clock time the sync finished.
	SyncedAt time.Time
	// Err is non-nil when sync failed. Partial successes (some templates
	// imported, some skipped due to a malformed Chart.yaml) carry both
	// Templates and Err so the UI can show "3 of 4 imported, 1 failed".
	Err error
}

// Engine drives sync passes. The zero value is unusable — Client and Logger
// must be set. Engine is safe for concurrent use across goroutines because
// every sync pass clones into a fresh tempdir.
type Engine struct {
	// Client is the in-cluster client used to read auth Secrets and write
	// imported template ConfigMaps.
	Client kubernetes.Interface
	// Logger receives debug + warn entries during sync. When nil, slog's
	// default is used.
	Logger *slog.Logger
	// CloneDepth caps the git clone depth to keep transfers small. Zero
	// means full history (only useful for branches that move quickly).
	CloneDepth int
}

// SyncOne pulls one external repo at its pinned Ref, packages every chart
// found under repo.Path, and persists each as a cluster template ConfigMap.
//
// Returns SyncResult.Err only for catastrophic failures (clone failed, no
// directory at Path, etc.). Per-chart failures are logged and the loop
// continues — partial syncs are better than all-or-nothing because one
// broken Chart.yaml in a multi-chart repo shouldn't block the rest.
func (e *Engine) SyncOne(ctx context.Context, repo tpl.ExternalTemplateRepo) SyncResult {
	res := SyncResult{SourceName: repo.Name, SyncedAt: time.Now().UTC()}
	logger := e.logger()

	// Phase 1 — fetch: route to the right fetcher based on source type.
	// Per-template parse failures land in result.PartialErrs;
	// catastrophic failures (clone, registry unreachable) come back as
	// fetchErr.
	f, fetcherErr := e.fetcherForType(repo.EffectiveType(), logger)
	if fetcherErr != nil {
		res.Err = fetcherErr
		return res
	}
	result, fetchErr := f.Fetch(ctx, repo)
	if fetchErr != nil {
		res.Err = fetchErr
		return res
	}
	for _, pe := range result.PartialErrs {
		logger.Warn("registrysync: chart import failed; skipping",
			"source", repo.Name,
			"chart", pe.Name,
			"err", pe.Err,
		)
		// Record the most-recent error but keep going so successes
		// alongside still surface to the operator.
		res.Err = pe.Err
	}

	// Phase 2 — persist: write each resolved template to the cluster.
	// kube.SaveTemplate accepts nil chartTGZ for templates whose chart
	// is shipped out-of-band (today: never; future: registry-ref mode).
	for _, rt := range result.Templates {
		if err := kube.SaveTemplate(ctx, e.Client, rt.Template, rt.ChartBytes); err != nil {
			logger.Warn("registrysync: persist template failed; skipping",
				"source", repo.Name,
				"template", rt.Template.Metadata.Name,
				"err", err,
			)
			res.Err = err
			continue
		}
		res.Templates = append(res.Templates, rt.Template.Metadata.Name)
		logger.Info("registrysync: imported chart", "source", repo.Name, "template", rt.Template.Metadata.Name)
	}
	return res
}

// SyncAll fans out across every ExternalTemplateRepo in the registry and
// returns one SyncResult per source. Callers are expected to fold the
// results back into reg.Sources via ApplyResult before persisting.
func (e *Engine) SyncAll(ctx context.Context, reg *tpl.TemplateRegistry) []SyncResult {
	if reg == nil {
		return nil
	}
	out := make([]SyncResult, 0, len(reg.External))
	for _, repo := range reg.External {
		out = append(out, e.SyncOne(ctx, repo))
	}
	return out
}

// ApplyResult rewrites reg.Sources for one external repo: drops the repo's
// existing entries and re-adds one TemplateSource per imported template
// with a fresh SyncedAt. We rebuild rather than merge so a chart removed
// upstream eventually disappears from suparship's view.
//
// Lives in registrysync (not the server handler) so the periodic sync
// goroutine and the manual-trigger handler share the same logic.
func ApplyResult(reg *tpl.TemplateRegistry, repo tpl.ExternalTemplateRepo, result SyncResult) {
	if reg == nil {
		return
	}
	syncedAt := result.SyncedAt.UTC().Format(time.RFC3339)

	fresh := make([]tpl.TemplateSource, 0, len(reg.Sources))
	for _, s := range reg.Sources {
		if s.ExternalRepo != repo.Name {
			fresh = append(fresh, s)
		}
	}
	for _, name := range result.Templates {
		fresh = append(fresh, tpl.TemplateSource{
			Name:         name,
			Origin:       "external",
			ExternalRepo: repo.Name,
			ExternalRef:  repo.Ref,
			ExternalPath: repo.Path,
			SyncedAt:     syncedAt,
		})
	}
	reg.Sources = fresh
}

// gitFetcher resolves a templates-repo source declaration into
// ResolvedTemplate pairs. Implements fetcher.Fetcher (via Fetch); the
// concrete fetchRepo method is exposed so SyncOne can pass through a
// strongly typed ExternalTemplateRepo without going through the any-typed
// Fetcher interface.
type gitFetcher struct {
	client     kubernetes.Interface
	logger     *slog.Logger
	cloneDepth int
	// chartsRepo selects the plain-Helm-charts-monorepo behavior: the scan
	// path defaults to "charts/", only inline charts are discovered (no
	// external-mode template.yaml scan), and charts without a bundled
	// template.yaml import as passthrough/BYO.
	chartsRepo bool
}

func newGitFetcher(client kubernetes.Interface, logger *slog.Logger, cloneDepth int) *gitFetcher {
	return &gitFetcher{client: client, logger: logger, cloneDepth: cloneDepth}
}

// newGitChartsFetcher builds a gitFetcher in charts-repo mode (SourceTypeGitCharts).
func newGitChartsFetcher(client kubernetes.Interface, logger *slog.Logger, cloneDepth int) *gitFetcher {
	return &gitFetcher{client: client, logger: logger, cloneDepth: cloneDepth, chartsRepo: true}
}

// defaultChartsPath is the subpath gitcharts sources scan when Path is empty.
const defaultChartsPath = "charts"

// Fetch satisfies fetcher.Fetcher. Source must be tpl.ExternalTemplateRepo.
func (f *gitFetcher) Fetch(ctx context.Context, source any) (fetcher.FetchResult, error) {
	repo, ok := source.(tpl.ExternalTemplateRepo)
	if !ok {
		return fetcher.FetchResult{}, fmt.Errorf("gitFetcher: expected tpl.ExternalTemplateRepo, got %T", source)
	}
	return f.fetchRepo(ctx, repo)
}

// fetchRepo clones the repo, walks for charts, and parses each into a
// ResolvedTemplate. The chart bytes are packaged into a .tgz so the
// downstream persistence path is identical regardless of which fetcher
// produced them.
//
// Catastrophic failures (clone, auth, missing path) come back as the
// error return; per-chart failures land in FetchResult.PartialErrs so
// callers can surface "3 imported, 1 failed" without losing either signal.
func (f *gitFetcher) fetchRepo(ctx context.Context, repo tpl.ExternalTemplateRepo) (fetcher.FetchResult, error) {
	tmp, err := os.MkdirTemp("", "suparship-tplsync-*")
	if err != nil {
		return fetcher.FetchResult{}, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmp)

	user, pass, err := f.readAuth(ctx, repo.ExistingSecret)
	if err != nil {
		return fetcher.FetchResult{}, fmt.Errorf("read auth: %w", err)
	}

	cloneURL := embedCredentials(repo.RepoURL, user, pass)
	repoDir := filepath.Join(tmp, "repo")
	args := []string{"clone"}
	if f.cloneDepth > 0 {
		args = append(args, "--depth", fmt.Sprint(f.cloneDepth))
	}
	args = append(args, "--branch", refOrDefault(repo.Ref), cloneURL, repoDir)
	if err := runGit(ctx, tmp, args...); err != nil {
		return fetcher.FetchResult{}, fmt.Errorf("clone %s: %w", repo.RepoURL, err)
	}

	// gitcharts mode scans "charts/" by default; the templates-repo mode
	// defaults to the repo root.
	scanPath := repo.Path
	if scanPath == "" && f.chartsRepo {
		scanPath = defaultChartsPath
	}
	chartDir := filepath.Join(repoDir, strings.TrimPrefix(scanPath, "/"))
	if info, err := os.Stat(chartDir); err != nil || !info.IsDir() {
		return fetcher.FetchResult{}, fmt.Errorf("path %q not found in repo", scanPath)
	}

	chartPaths, err := findChartDirs(chartDir)
	if err != nil {
		return fetcher.FetchResult{}, fmt.Errorf("walk %s: %w", chartDir, err)
	}
	// External-mode template discovery only applies to suparship templates
	// repos. A plain charts repo has none, and a chart shipping its own
	// template.yaml would otherwise be double-processed / mis-flagged.
	var externalTemplates []string
	if !f.chartsRepo {
		externalTemplates, err = findExternalTemplates(chartDir)
		if err != nil {
			return fetcher.FetchResult{}, fmt.Errorf("walk for external templates %s: %w", chartDir, err)
		}
	}
	if len(chartPaths) == 0 && len(externalTemplates) == 0 {
		// Empty path is technically valid (operator may not have added
		// templates yet) — return success with no templates so the
		// source's last-synced timestamp still updates.
		f.logger.Info("registrysync: no templates found", "source", repo.Name, "path", scanPath)
		return fetcher.FetchResult{}, nil
	}

	out := fetcher.FetchResult{}
	// Inline-mode templates: a Chart.yaml is present. The existing
	// chartimport pipeline handles bundled-template-yaml pickup and
	// inferred-template fallback; charts-repo mode additionally folds a
	// template-less chart into passthrough/BYO.
	for _, dir := range chartPaths {
		rt, err := f.resolveChart(dir)
		if err != nil {
			out.PartialErrs = append(out.PartialErrs, fetcher.PartialError{
				Name: filepath.Base(dir),
				Err:  err,
			})
			continue
		}
		out.Templates = append(out.Templates, rt)
	}
	// External-mode templates: <name>/template.yaml present, no
	// sibling chart/. Engine.chart must be a registry ref since there
	// are no local chart bytes to package.
	for _, path := range externalTemplates {
		rt, err := f.resolveExternalTemplate(path)
		if err != nil {
			out.PartialErrs = append(out.PartialErrs, fetcher.PartialError{
				Name: filepath.Base(filepath.Dir(path)),
				Err:  err,
			})
			continue
		}
		out.Templates = append(out.Templates, rt)
	}
	return out, nil
}

// resolveChart packages a single chart directory and runs it through the
// existing chartimport pipeline. Returns a ResolvedTemplate ready for
// persistence — the chart bytes are the packaged .tgz.
func (f *gitFetcher) resolveChart(chartDir string) (fetcher.ResolvedTemplate, error) {
	bundle, err := packageChart(chartDir)
	if err != nil {
		return fetcher.ResolvedTemplate{}, fmt.Errorf("package: %w", err)
	}
	arc, err := chartimport.ParseArchive(bundle)
	if err != nil {
		return fetcher.ResolvedTemplate{}, fmt.Errorf("parse: %w", err)
	}
	tmpl, err := chartimport.ToTemplate(arc)
	if err != nil {
		return fetcher.ResolvedTemplate{}, fmt.Errorf("to template: %w", err)
	}
	// In charts-repo mode, a chart that didn't ship its own template.yaml
	// got best-effort inferred metadata assuming the suparship-common
	// schema. That's wrong for an arbitrary Helm chart — import it as
	// passthrough/BYO so its own values.yaml is the base. A chart that DID
	// ship a template.yaml (arc.TemplateYAML != nil) is honored as-authored.
	if f.chartsRepo && arc.TemplateYAML == nil {
		makePassthrough(tmpl)
		if err := tmpl.Validate(); err != nil {
			return fetcher.ResolvedTemplate{}, fmt.Errorf("passthrough template invalid: %w", err)
		}
	}
	return fetcher.ResolvedTemplate{Template: tmpl, ChartBytes: bundle}, nil
}

// makePassthrough converts an inferred (canonical) template into a passthrough/
// BYO one: the platform injects no canonical values base and exposes only
// ((platform.*))/((vars.*)) tokens, with the chart's own values.yaml as the base.
// The inferred inputs/mappings/components describe the canonical schema the
// chart doesn't use, so they're dropped. Mirrors the clearing in
// internal/server/template_metadata_handler.go.
func makePassthrough(t *tpl.Template) {
	no := false
	t.Spec.InjectCanonicalValues = &no
	t.Spec.Inputs = nil
	t.Spec.AdvancedInputs = nil
	t.Spec.Mappings = nil
	t.Spec.Components = nil
}

// resolveExternalTemplate parses a template.yaml file that has no sibling
// chart/ directory. The template's engine.chart must be a registry ref —
// the publisher resolves the chart at sync time via Argo's repo-server.
// ChartBytes is nil because there are no local bytes to package; this is
// what marks the template as external-mode for the publisher.
func (f *gitFetcher) resolveExternalTemplate(templatePath string) (fetcher.ResolvedTemplate, error) {
	data, err := os.ReadFile(templatePath)
	if err != nil {
		return fetcher.ResolvedTemplate{}, fmt.Errorf("read %s: %w", templatePath, err)
	}
	tmpl, err := tpl.Parse(data)
	if err != nil {
		return fetcher.ResolvedTemplate{}, fmt.Errorf("parse %s: %w", templatePath, err)
	}
	if !tmpl.Spec.Engine.Chart.IsExternal() {
		// A template.yaml without a sibling chart/ MUST declare a
		// registry ref; otherwise the publisher has nothing to point
		// Argo at. Surface this as a per-template error so other
		// templates in the same repo still sync.
		return fetcher.ResolvedTemplate{}, fmt.Errorf(
			"template %q has no sibling chart/ directory and engine.chart is not a registry ref — declare engine.chart: {repository, name, version} or add a chart/ sibling",
			tmpl.Metadata.Name,
		)
	}
	return fetcher.ResolvedTemplate{Template: tmpl, ChartBytes: nil}, nil
}

// readAuth reads credentials from the same K8s Secret shape Engine.readAuth
// uses (data["token"] for PAT-style, falling back to data["username"] +
// data["password"]). Duplicated here so gitFetcher is self-contained;
// callers can wire it without an Engine.
func (f *gitFetcher) readAuth(ctx context.Context, secretName string) (string, string, error) {
	if secretName == "" {
		return "", "", nil
	}
	if f.client == nil {
		return "", "", fmt.Errorf("auth secret %q referenced but no cluster client configured", secretName)
	}
	sec, err := f.client.CoreV1().Secrets(systemNamespace).Get(ctx, secretName, metav1.GetOptions{})
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

// fetcherForType selects the fetcher implementation for a given source
// type. Returns ErrUnsupportedSourceType for types whose fetchers
// haven't shipped yet (chartmuseum, gittgz) — surfaces a clean
// per-source error instead of a panic when an operator registers a
// reserved type ahead of its implementation.
func (e *Engine) fetcherForType(sourceType string, logger *slog.Logger) (fetcher.Fetcher, error) {
	switch sourceType {
	case tpl.SourceTypeGit:
		return newGitFetcher(e.Client, logger, e.CloneDepth), nil
	case tpl.SourceTypeGitCharts:
		return newGitChartsFetcher(e.Client, logger, e.CloneDepth), nil
	case tpl.SourceTypeOCI:
		return newOCIFetcher(e.Client, logger), nil
	case tpl.SourceTypeChartMuseum:
		return newChartMuseumFetcher(e.Client, logger), nil
	case tpl.SourceTypeGitTgz:
		return newGitTgzFetcher(e.Client, logger, e.CloneDepth), nil
	default:
		return nil, fmt.Errorf("%w: %q", tpl.ErrInvalidSourceType, sourceType)
	}
}

func (e *Engine) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
}

// findExternalTemplates returns every template.yaml file under root whose
// directory has no sibling chart/Chart.yaml (i.e. external-mode templates,
// where the chart lives in a Helm registry rather than next to the
// template). Templates whose template.yaml has a sibling chart/ are
// handled by findChartDirs + packageChart's bundled-template pickup.
//
// Helm dependency caches under nested "charts/" subdirs are skipped so
// vendored sub-charts don't get scanned.
func findExternalTemplates(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "charts" && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != tpl.TemplateFileName {
			return nil
		}
		// Skip when the directory also contains a chart/ — that's the
		// inline-mode layout, handled by findChartDirs.
		siblingChart := filepath.Join(filepath.Dir(path), "chart", "Chart.yaml")
		if _, err := os.Stat(siblingChart); err == nil {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out, err
}

// findChartDirs returns every immediate-or-nested directory under root that
// contains a Chart.yaml. Only one chart is recognized per directory level —
// nested charts under a parent's "charts/" sub-tree are skipped because
// Helm packages dependencies separately.
func findChartDirs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// Skip Helm dependency caches.
			if d.Name() == "charts" && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "Chart.yaml" {
			out = append(out, filepath.Dir(path))
			// Don't recurse into chart's templates/, etc.
			return filepath.SkipDir
		}
		return nil
	})
	return out, err
}

// packageChart tars + gzips a chart directory in the layout `helm package`
// produces (top-level "<chartName>/" folder). Re-implemented inline so the
// sync engine doesn't depend on the helm CLI being on PATH.
//
// suparship-flavoured template repos ship a sibling layout:
//
//	templates/<name>/
//	  template.yaml          ← suparship metadata (inputs, mappings, presets)
//	  chart/
//	    Chart.yaml
//	    ...
//
// When packageChart is invoked on `chart/`, it also looks for a
// `template.yaml` in the parent directory and, when present, includes
// it in the tarball at "<chartName>/template.yaml". chartimport.
// ParseArchive picks it up so the chart imports with the operator's
// hand-authored template instead of the best-effort inferred one.
func packageChart(chartDir string) ([]byte, error) {
	chartName := filepath.Base(chartDir)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// Pick up a sibling template.yaml if the operator shipped one.
	if data, ok := readSiblingTemplateYAML(chartDir); ok {
		hdr := &tar.Header{
			Name:     filepath.ToSlash(filepath.Join(chartName, "template.yaml")),
			Mode:     0o644,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := io.Copy(tw, bytes.NewReader(data)); err != nil {
			return nil, err
		}
	}

	err := filepath.WalkDir(chartDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(chartDir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		// Skip hidden files (.git, .DS_Store) — they have no place in a
		// packaged chart.
		base := filepath.Base(path)
		if strings.HasPrefix(base, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		archivePath := filepath.ToSlash(filepath.Join(chartName, rel))

		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if d.IsDir() {
			hdr := &tar.Header{
				Name:     archivePath + "/",
				Mode:     int64(info.Mode().Perm()),
				Typeflag: tar.TypeDir,
			}
			return tw.WriteHeader(hdr)
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		hdr := &tar.Header{
			Name:     archivePath,
			Mode:     int64(info.Mode().Perm()),
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err = io.Copy(tw, bytes.NewReader(data))
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// readSiblingTemplateYAML returns the contents of `<parent>/template.yaml`
// when chartDir matches the suparship-flavoured layout (a dir named
// "chart" with template.yaml as its sibling). Returns (nil, false) on
// any miss — wrong layout, no sibling, or read error — so callers can
// silently fall through to the inferred-template path.
//
// The "chart" name guard is important: a multi-chart library repo can
// legitimately have a template.yaml at the parent level for entirely
// unrelated reasons (Helm itself, downstream tooling). We only honor
// the sibling when the layout matches the convention documented on
// packageChart.
func readSiblingTemplateYAML(chartDir string) ([]byte, bool) {
	if filepath.Base(chartDir) != "chart" {
		return nil, false
	}
	sibling := filepath.Join(filepath.Dir(chartDir), "template.yaml")
	data, err := os.ReadFile(sibling)
	if err != nil {
		return nil, false
	}
	return data, true
}

// embedCredentials inserts user:password into HTTP/HTTPS URLs so `git clone`
// doesn't prompt interactively. Mirrors the gitops publisher's helper —
// kept private here to avoid pulling that whole package into the dep graph
// just for one function.
func embedCredentials(repoURL, user, pass string) string {
	if user == "" {
		return repoURL
	}
	for _, scheme := range []string{"https://", "http://"} {
		if strings.HasPrefix(repoURL, scheme) {
			return scheme + user + ":" + pass + "@" + repoURL[len(scheme):]
		}
	}
	return repoURL
}

// refOrDefault returns the explicit ref or "main" when empty. We prefer
// "main" over "HEAD" so the clone is always pinned to a deterministic name
// — operators tracking a moving branch should pin to that branch
// explicitly.
func refOrDefault(ref string) string {
	if ref == "" {
		return "main"
	}
	return ref
}

// runGit shells out to the system git, returning a wrapped error with the
// command's combined output on failure. We prefer exec over a Go git
// library so the engine works with any auth setup the host provides
// (SSH agent, credential helpers) without us reimplementing it.
func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}
