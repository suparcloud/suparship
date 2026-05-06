package registrysync

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/fetcher"
)

// gitTgzFetcher implements fetcher.Fetcher for chart .tgz files checked
// into a git repo. One source = one chart at a known path inside the
// repo. Useful when an org keeps charts in git but doesn't want to run
// a Helm registry — the .tgz is a static artifact under version control.
//
// Auth is the same Secret shape gitFetcher uses (token / username+password).
// Reuses the runGit helper so the clone behaviour matches gitFetcher's
// (shallow when CloneDepth > 0, branch=Ref).
type gitTgzFetcher struct {
	client     kubernetes.Interface
	logger     *slog.Logger
	cloneDepth int
}

func newGitTgzFetcher(client kubernetes.Interface, logger *slog.Logger, cloneDepth int) *gitTgzFetcher {
	return &gitTgzFetcher{client: client, logger: logger, cloneDepth: cloneDepth}
}

// Fetch satisfies fetcher.Fetcher. Source must be tpl.ExternalTemplateRepo
// with Type="gittgz". Path points at the .tgz file relative to repo root.
func (f *gitTgzFetcher) Fetch(ctx context.Context, source any) (fetcher.FetchResult, error) {
	repo, ok := source.(tpl.ExternalTemplateRepo)
	if !ok {
		return fetcher.FetchResult{}, fmt.Errorf("gitTgzFetcher: expected tpl.ExternalTemplateRepo, got %T", source)
	}
	if repo.EffectiveType() != tpl.SourceTypeGitTgz {
		return fetcher.FetchResult{}, fmt.Errorf("gitTgzFetcher: expected source type %q, got %q", tpl.SourceTypeGitTgz, repo.EffectiveType())
	}
	if err := repo.Validate(); err != nil {
		return fetcher.FetchResult{}, err
	}

	tmp, err := os.MkdirTemp("", "suparship-tgzsync-*")
	if err != nil {
		return fetcher.FetchResult{}, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmp)

	user, pass, err := readOCIAuth(ctx, f.client, repo.ExistingSecret)
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

	tgzPath := filepath.Join(repoDir, strings.TrimPrefix(repo.Path, "/"))
	bundle, err := os.ReadFile(tgzPath)
	if err != nil {
		return fetcher.FetchResult{}, fmt.Errorf("read .tgz at %q: %w", repo.Path, err)
	}

	rt, err := resolvePulledChart(bundle)
	if err != nil {
		return fetcher.FetchResult{
			PartialErrs: []fetcher.PartialError{{Name: filepath.Base(repo.Path), Err: err}},
		}, nil
	}
	return fetcher.FetchResult{Templates: []fetcher.ResolvedTemplate{rt}}, nil
}
