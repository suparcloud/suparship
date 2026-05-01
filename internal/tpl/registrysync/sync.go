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

	tmp, err := os.MkdirTemp("", "suparship-tplsync-*")
	if err != nil {
		res.Err = fmt.Errorf("mktemp: %w", err)
		return res
	}
	defer os.RemoveAll(tmp)

	// Auth (optional): username/password live in a K8s Secret keyed by
	// ExistingSecret. Convention mirrors the gitops repo Secret format
	// (data["username"], data["password"]).
	user, pass, err := e.readAuth(ctx, repo.ExistingSecret)
	if err != nil {
		res.Err = fmt.Errorf("read auth: %w", err)
		return res
	}

	cloneURL := embedCredentials(repo.RepoURL, user, pass)
	repoDir := filepath.Join(tmp, "repo")
	args := []string{"clone"}
	if e.CloneDepth > 0 {
		args = append(args, "--depth", fmt.Sprint(e.CloneDepth))
	}
	args = append(args, "--branch", refOrDefault(repo.Ref), cloneURL, repoDir)
	if err := runGit(ctx, tmp, args...); err != nil {
		res.Err = fmt.Errorf("clone %s: %w", repo.RepoURL, err)
		return res
	}

	chartDir := filepath.Join(repoDir, strings.TrimPrefix(repo.Path, "/"))
	if info, err := os.Stat(chartDir); err != nil || !info.IsDir() {
		res.Err = fmt.Errorf("path %q not found in repo", repo.Path)
		return res
	}

	chartPaths, err := findChartDirs(chartDir)
	if err != nil {
		res.Err = fmt.Errorf("walk %s: %w", chartDir, err)
		return res
	}
	if len(chartPaths) == 0 {
		// Empty path is technically valid (operator may not have added
		// charts yet) — return success with no templates rather than an
		// error, so the source's last-synced timestamp still updates.
		logger.Info("registrysync: no charts found", "source", repo.Name, "path", repo.Path)
		return res
	}

	for _, dir := range chartPaths {
		name, err := e.importOne(ctx, dir)
		if err != nil {
			logger.Warn("registrysync: chart import failed; skipping",
				"source", repo.Name,
				"chart", filepath.Base(dir),
				"err", err,
			)
			// Record the most-recent error but keep going so we collect a
			// full set of successes alongside.
			res.Err = err
			continue
		}
		res.Templates = append(res.Templates, name)
		logger.Info("registrysync: imported chart", "source", repo.Name, "template", name)
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

// importOne packages a single chart directory and persists it via the
// existing chartimport pipeline. Returns the template name on success.
func (e *Engine) importOne(ctx context.Context, chartDir string) (string, error) {
	bundle, err := packageChart(chartDir)
	if err != nil {
		return "", fmt.Errorf("package: %w", err)
	}
	arc, err := chartimport.ParseArchive(bundle)
	if err != nil {
		return "", fmt.Errorf("parse: %w", err)
	}
	tmpl, err := chartimport.ToTemplate(arc)
	if err != nil {
		return "", fmt.Errorf("to template: %w", err)
	}
	if err := kube.SaveTemplate(ctx, e.Client, tmpl, bundle); err != nil {
		return "", fmt.Errorf("save: %w", err)
	}
	return tmpl.Metadata.Name, nil
}

// readAuth reads credentials from a K8s Secret. Returns empty strings
// (no error) when the secret name is empty so public repos work without
// configuration.
//
// Two key shapes are supported:
//   - data["token"]                 — a PAT (GitHub/GitLab/Gitea). Used as
//     the password with "x-access-token" as the username; this is the form
//     all three providers accept for HTTPS Basic auth.
//   - data["username"] + data["password"] — generic Basic auth (Bitbucket,
//     self-hosted Git, anything else).
//
// "token" wins when both are present so an operator can rotate to a PAT
// without first deleting the old keys.
func (e *Engine) readAuth(ctx context.Context, secretName string) (string, string, error) {
	if secretName == "" {
		return "", "", nil
	}
	if e.Client == nil {
		return "", "", fmt.Errorf("auth secret %q referenced but no cluster client configured", secretName)
	}
	sec, err := e.Client.CoreV1().Secrets(systemNamespace).Get(ctx, secretName, metav1.GetOptions{})
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

func (e *Engine) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.Default()
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
