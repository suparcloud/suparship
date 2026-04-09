package gitops

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/helmvalues"
	"gopkg.in/yaml.v3"
)

// PublisherConfig holds the configuration for the GitOps publisher.
type PublisherConfig struct {
	// RepoURL is the Git repository URL for cloning and pushing (host-accessible URL).
	RepoURL string
	// RepoUser is the Git username for HTTP authentication.
	RepoUser string
	// RepoPassword is the Git password or token for HTTP authentication.
	RepoPassword string
	// ArgoCDRepoURL is the URL ArgoCD uses to sync from the gitops repo.
	// This may be an internal cluster URL (e.g. http://gitea-http.gitea.svc.cluster.local:3000/...).
	// Falls back to RepoURL when empty.
	ArgoCDRepoURL string
	// Branch is the Git branch to commit to. Defaults to "main".
	Branch string
}

// Publisher writes ArgoCD Application manifests to the GitOps repository and
// commits + pushes them so that ArgoCD can pick them up automatically.
//
// The expected repository layout after publishing an app is:
//
//	gitops-output/<project>/<app>/<env>/argocd-app.yaml
//
// Each argocd-app.yaml is an ArgoCD Application CRD that references the Helm
// chart at charts/<template-name>/ with inline Helm values specific to that
// environment.
type Publisher struct {
	cfg PublisherConfig
}

// NewPublisher creates a Publisher from cfg.
// Returns an error when RepoURL is empty (required).
func NewPublisher(cfg PublisherConfig) (*Publisher, error) {
	if cfg.RepoURL == "" {
		return nil, fmt.Errorf("gitops RepoURL is required")
	}
	if cfg.ArgoCDRepoURL == "" {
		cfg.ArgoCDRepoURL = cfg.RepoURL
	}
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	return &Publisher{cfg: cfg}, nil
}

// PublishApp clones the gitops repository, writes one argocd-app.yaml per
// environment under gitops-output/<project>/<app>/<env>/, commits, and pushes.
//
// The ArgoCD Applications are built with SyncAutomated=true and reference the
// Helm chart at charts/<template-name>/ inside the gitops repo. Inline Helm
// values are derived from helmvalues.MapToHelmValues for each environment.
//
// If the generated files are identical to what is already in the repo (no
// diff after git add), the function returns nil without creating an empty
// commit.
func (p *Publisher) PublishApp(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) error {
	tmpDir, err := os.MkdirTemp("", "suparship-gitops-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	repoDir := filepath.Join(tmpDir, "gitops")

	cloneURL := p.embedCredentials(p.cfg.RepoURL)
	slog.Debug("gitops: cloning repo", "repo", p.cfg.RepoURL, "branch", p.cfg.Branch, "dir", repoDir)
	if err := p.git(ctx, tmpDir, "clone", "--depth=1", "--branch="+p.cfg.Branch, cloneURL, repoDir); err != nil {
		return fmt.Errorf("clone gitops repo: %w", err)
	}
	slog.Debug("gitops: repo cloned")

	if err := p.git(ctx, repoDir, "config", "user.email", "suparship@suparcloud.io"); err != nil {
		return err
	}
	if err := p.git(ctx, repoDir, "config", "user.name", "suparShip"); err != nil {
		return err
	}

	// Ensure an AppProject exists for the suparship project so that ArgoCD can
	// accept the Application CRDs that reference spec.project: <projectName>.
	//
	// The AppProject is written to gitops-output/<project>/appproject.yaml and
	// carries sync-wave: -1 so ArgoCD creates it before any child Application
	// in the same sync.  Writing it on every publish is idempotent — git will
	// only commit when the content changes.
	appProject := BuildArgoAppProject(app.ProjectName, AppProjectOptions{
		Description:           "suparShip project: " + app.ProjectName,
		AllowClusterResources: true,
	})
	appProjectBytes, err := yaml.Marshal(appProject)
	if err != nil {
		return fmt.Errorf("marshal appproject for project %s: %w", app.ProjectName, err)
	}
	appProjectPath := filepath.Join(repoDir, "gitops-output", app.ProjectName, "appproject.yaml")
	if err := os.MkdirAll(filepath.Dir(appProjectPath), 0o755); err != nil {
		return fmt.Errorf("create appproject output dir: %w", err)
	}
	if err := os.WriteFile(appProjectPath, appProjectBytes, 0o644); err != nil {
		return fmt.Errorf("write appproject.yaml: %w", err)
	}
	slog.Debug("gitops: wrote appproject.yaml", "path", appProjectPath)

	templateName := app.Spec.Template.Name

	for _, env := range envs {
		hv := helmvalues.MapToHelmValues(app, env.EnvName, env.EnvType)
		inlineValues, err := marshalHelmValues(hv)
		if err != nil {
			return fmt.Errorf("marshal helm values for env %s: %w", env.EnvName, err)
		}

		argoApp := BuildArgoApplication(app, *env, BuildOptions{
			RepoURL:       p.cfg.ArgoCDRepoURL,
			RepoPath:      "charts/" + templateName,
			SyncAutomated: true,
			InlineValues:  inlineValues,
		})

		yamlBytes, err := yaml.Marshal(argoApp)
		if err != nil {
			return fmt.Errorf("marshal argocd app for env %s: %w", env.EnvName, err)
		}

		outPath := filepath.Join(repoDir,
			"gitops-output", app.ProjectName, app.Name, env.EnvName,
			"argocd-app.yaml",
		)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return fmt.Errorf("create output dir for env %s: %w", env.EnvName, err)
		}
		if err := os.WriteFile(outPath, yamlBytes, 0o644); err != nil {
			return fmt.Errorf("write argocd-app.yaml for env %s: %w", env.EnvName, err)
		}
		slog.Debug("gitops: wrote argocd-app.yaml", "path", outPath)
	}

	if err := p.git(ctx, repoDir, "add", "."); err != nil {
		return err
	}

	// Skip commit and push when there is nothing new to commit (idempotent).
	if empty, err := p.stagedIsEmpty(ctx, repoDir); err != nil {
		return err
	} else if empty {
		slog.Debug("gitops: nothing to commit — argocd-app.yaml already up to date")
		return nil
	}

	commitMsg := fmt.Sprintf("feat(apps): add %s/%s\n\nCreated by suparShip.", app.ProjectName, app.Name)
	slog.Debug("gitops: committing")
	if err := p.git(ctx, repoDir, "commit", "-m", commitMsg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	slog.Debug("gitops: pushing to origin", "branch", p.cfg.Branch)
	if err := p.git(ctx, repoDir, "push", "origin", p.cfg.Branch); err != nil {
		return fmt.Errorf("push to gitops repo: %w", err)
	}
	slog.Debug("gitops: push complete")

	return nil
}

// stagedIsEmpty returns true when there are no staged changes (nothing to commit).
func (p *Publisher) stagedIsEmpty(ctx context.Context, repoDir string) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return len(strings.TrimSpace(string(out))) == 0, nil
}

// embedCredentials injects user:password into an HTTP/HTTPS URL so that git
// does not prompt interactively during clone/push.
func (p *Publisher) embedCredentials(repoURL string) string {
	if p.cfg.RepoUser == "" {
		return repoURL
	}
	for _, scheme := range []string{"https://", "http://"} {
		if strings.HasPrefix(repoURL, scheme) {
			return scheme + p.cfg.RepoUser + ":" + p.cfg.RepoPassword + "@" + repoURL[len(scheme):]
		}
	}
	return repoURL
}

// git runs a git subcommand inside dir and returns a combined-output error on failure.
func (p *Publisher) git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// marshalHelmValues serializes a HelmValues struct to a YAML string suitable
// for use as inline Helm values inside an ArgoCD Application spec.
func marshalHelmValues(hv helmvalues.HelmValues) (string, error) {
	b, err := yaml.Marshal(hv)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
