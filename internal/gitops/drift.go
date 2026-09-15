package gitops

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/suparcloud/suparship/internal/domain"
)

// DetectAppDrift reports the files under the app's gitops tree that would
// change if the app were published right now from suparship's stored state —
// i.e. where the repo has drifted from what suparship believes (someone
// edited or reverted the repo directly). It renders into the cached clone
// exactly as PublishApp does, reads `git status`, and discards the render, so
// nothing is committed. Paths are repo-relative and sorted.
func (p *Publisher) DetectAppDrift(ctx context.Context, app *domain.App, envs []AppPublishEnv) ([]string, error) {
	var files []string
	err := p.withClonedRepo(ctx, func(repoDir string) error {
		var derr error
		files, derr = p.DetectAppDriftInRepo(ctx, repoDir, app, envs)
		return derr
	})
	return files, err
}

// DetectAppDriftInRepo is DetectAppDrift against an already-checked-out repo
// (white-box testable). The working tree is restored to HEAD before returning,
// success or failure, so a later publish in the same clone starts clean.
func (p *Publisher) DetectAppDriftInRepo(ctx context.Context, repoDir string, app *domain.App, envs []AppPublishEnv) (files []string, err error) {
	defer func() {
		if rerr := p.git(ctx, repoDir, "reset", "--hard", "HEAD"); rerr != nil && err == nil {
			err = fmt.Errorf("discard drift render: %w", rerr)
		}
		if cerr := p.git(ctx, repoDir, "clean", "-fd"); cerr != nil && err == nil {
			err = fmt.Errorf("discard drift render: %w", cerr)
		}
	}()
	if err := p.writeAppTree(ctx, repoDir, app, envs); err != nil {
		return nil, fmt.Errorf("render app for drift check: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		// Renames show as "old -> new"; the rendered path is the new one.
		if i := strings.LastIndex(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		files = append(files, strings.Trim(path, `"`))
	}
	sort.Strings(files)
	return files, nil
}
