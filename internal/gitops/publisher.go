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
	// SyncAutomated enables automated sync (prune + selfHeal) on generated Applications.
	SyncAutomated bool
}

// Publisher writes GitOps manifests to the GitOps repository and commits +
// pushes them so ArgoCD can pick them up automatically.
//
// # Repository layout (Model B — env/cluster-centric)
//
//	gitops-output/
//	  {envName}/
//	    appset.yaml                  ← ApplicationSet for this env's cluster
//	    {project}/
//	      appproject.yaml            ← ArgoCD AppProject
//	      {app}/
//	        app.yaml                 ← Git File generator parameters
//	        values.yaml              ← Helm values for this app/env
//	  previews/
//	    appset.yaml                  ← Preview ApplicationSet
//	    {project}/
//	      {previewName}/
//	        app.yaml                 ← Preview parameters (includes clusterServer + namespace)
//	        values.yaml              ← Helm values for this preview
//
// Each environment directory corresponds to one cluster. The ApplicationSet's
// Git File generator discovers all app.yaml files and deploys one Application
// per file to the cluster configured in appset.yaml.
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

// PublishEnvInfra writes the per-environment appset.yaml and per-project
// appproject.yaml for a set of environments. Call this when a cluster is
// registered or an environment's cluster mapping changes.
//
// Written files:
//   - gitops-output/{envName}/appset.yaml         — ApplicationSet for the cluster
//   - gitops-output/{envName}/{project}/appproject.yaml — AppProject with allowed destinations
//
// PublishEnvInfra is idempotent; it only creates a commit when content changes.
func (p *Publisher) PublishEnvInfra(ctx context.Context, projectName string, envs []AppSetEnv) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		for _, env := range envs {
			// Write appset.yaml for this env.
			appSet := BuildArgoAppSet(env, p.cfg.ArgoCDRepoURL, AppSetOptions{
				SyncAutomated: p.cfg.SyncAutomated,
			})
			appSetBytes, err := yaml.Marshal(appSet)
			if err != nil {
				return fmt.Errorf("marshal appset for env %s: %w", env.EnvName, err)
			}
			appSetPath := filepath.Join(repoDir, "gitops-output", env.EnvName, "appset.yaml")
			if err := p.writeFile(appSetPath, appSetBytes); err != nil {
				return err
			}
			slog.Debug("gitops: wrote appset.yaml", "path", appSetPath)

			// Write appproject.yaml for this project/env.
			appProject := BuildArgoAppProject(projectName, AppProjectOptions{
				Description:           "suparShip project: " + projectName,
				AllowClusterResources: true,
				Destinations: []AppProjectDestination{
					{Server: env.ClusterServer, Namespace: "*"},
				},
			})
			appProjectBytes, err := yaml.Marshal(appProject)
			if err != nil {
				return fmt.Errorf("marshal appproject for env %s: %w", env.EnvName, err)
			}
			appProjectPath := filepath.Join(repoDir, "gitops-output", env.EnvName, projectName, "appproject.yaml")
			if err := p.writeFile(appProjectPath, appProjectBytes); err != nil {
				return err
			}
			slog.Debug("gitops: wrote appproject.yaml", "path", appProjectPath)
		}

		// Write preview AppSet (idempotent; only changes if not present).
		previewAppSet := BuildArgoPreviewAppSet(p.cfg.ArgoCDRepoURL, AppSetOptions{
			SyncAutomated: p.cfg.SyncAutomated,
		})
		previewAppSetBytes, err := yaml.Marshal(previewAppSet)
		if err != nil {
			return fmt.Errorf("marshal previews appset: %w", err)
		}
		previewAppSetPath := filepath.Join(repoDir, "gitops-output", "previews", "appset.yaml")
		if err := p.writeFile(previewAppSetPath, previewAppSetBytes); err != nil {
			return err
		}

		return p.commitAndPush(ctx, repoDir, "feat(infra): update appsets and appprojects for "+projectName)
	})
}

// PublishApp writes the per-app app.yaml and values.yaml for each environment.
//
// Written files (per env):
//   - gitops-output/{envName}/{project}/{app}/app.yaml
//   - gitops-output/{envName}/{project}/{app}/values.yaml
//
// PublishApp is idempotent; it only creates a commit when content changes.
func (p *Publisher) PublishApp(ctx context.Context, app *domain.App, envs []AppPublishEnv) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		for _, env := range envs {
			// Write app.yaml — Git File generator parameters.
			appMeta := AppMetadata{
				Name:     app.Name,
				Project:  app.ProjectName,
				Template: app.Spec.Template.Name,
			}
			appMetaBytes, err := yaml.Marshal(appMeta)
			if err != nil {
				return fmt.Errorf("marshal app.yaml for env %s: %w", env.EnvName, err)
			}
			appMetaPath := filepath.Join(repoDir, "gitops-output", env.EnvName, app.ProjectName, app.Name, "app.yaml")
			if err := p.writeFile(appMetaPath, appMetaBytes); err != nil {
				return err
			}

			// Write values.yaml — Helm values with env-specific baseDomain.
			hv := helmvalues.MapToHelmValuesWithDomain(app, env.EnvName, env.EnvType, env.BaseDomain)
			hvBytes, err := yaml.Marshal(hv)
			if err != nil {
				return fmt.Errorf("marshal values.yaml for env %s: %w", env.EnvName, err)
			}
			valuesPath := filepath.Join(repoDir, "gitops-output", env.EnvName, app.ProjectName, app.Name, "values.yaml")
			if err := p.writeFile(valuesPath, hvBytes); err != nil {
				return err
			}
			slog.Debug("gitops: wrote app files", "env", env.EnvName, "app", app.Name)
		}

		commitMsg := fmt.Sprintf("feat(apps): publish %s/%s\n\nCreated by suparShip.", app.ProjectName, app.Name)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// AppPublishEnv carries per-environment publish context for PublishApp.
type AppPublishEnv struct {
	// EnvName is the logical environment name, e.g. "staging".
	EnvName string
	// EnvType classifies the environment for Helm values mapping.
	EnvType domain.AppEnvironmentType
	// BaseDomain is used to derive routing.host in values.yaml.
	// When empty, "localhost" is used.
	BaseDomain string
}

// PublishPreview writes a preview app.yaml and values.yaml so ArgoCD
// deploys the preview via the previews ApplicationSet.
//
// Written files:
//   - gitops-output/previews/{project}/{previewName}/app.yaml
//   - gitops-output/previews/{project}/{previewName}/values.yaml
//
// PublishPreview is idempotent; it only creates a commit when content changes.
func (p *Publisher) PublishPreview(ctx context.Context, app *domain.App, preview PreviewPublishSpec) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		previewMeta := PreviewAppMetadata{
			AppName:       app.Name,
			PreviewName:   preview.PreviewName,
			Project:       app.ProjectName,
			Template:      app.Spec.Template.Name,
			ClusterServer: preview.ClusterServer,
			Namespace:     preview.Namespace,
		}
		metaBytes, err := yaml.Marshal(previewMeta)
		if err != nil {
			return fmt.Errorf("marshal preview app.yaml: %w", err)
		}
		metaPath := filepath.Join(repoDir, "gitops-output", "previews", app.ProjectName, preview.PreviewName, "app.yaml")
		if err := p.writeFile(metaPath, metaBytes); err != nil {
			return err
		}

		hv := helmvalues.MapToHelmValuesWithDomain(app, preview.PreviewName, domain.AppEnvPreview, preview.BaseDomain)
		hvBytes, err := yaml.Marshal(hv)
		if err != nil {
			return fmt.Errorf("marshal preview values.yaml: %w", err)
		}
		valuesPath := filepath.Join(repoDir, "gitops-output", "previews", app.ProjectName, preview.PreviewName, "values.yaml")
		if err := p.writeFile(valuesPath, hvBytes); err != nil {
			return err
		}

		commitMsg := fmt.Sprintf("feat(previews): create preview %s/%s\n\nCreated by suparShip.", app.ProjectName, preview.PreviewName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// PreviewPublishSpec carries the parameters for publishing a preview environment.
type PreviewPublishSpec struct {
	// PreviewName is the sanitized preview identifier (e.g. "pr-42").
	PreviewName string
	// ClusterServer is the API server URL for the cluster where this preview runs.
	ClusterServer string
	// Namespace is the Kubernetes namespace for this preview.
	Namespace string
	// BaseDomain is used to derive routing.host in values.yaml.
	// When empty, "localhost" is used.
	BaseDomain string
}

// DeletePreview removes the preview directory from the GitOps repo and commits.
// It is a no-op (without error) if the preview directory does not exist.
func (p *Publisher) DeletePreview(ctx context.Context, projectName, previewName string) error {
	return p.withClonedRepo(ctx, func(repoDir string) error {
		previewDir := filepath.Join(repoDir, "gitops-output", "previews", projectName, previewName)
		if _, err := os.Stat(previewDir); os.IsNotExist(err) {
			slog.Debug("gitops: preview directory not found, nothing to delete", "preview", previewName)
			return nil
		}
		if err := os.RemoveAll(previewDir); err != nil {
			return fmt.Errorf("removing preview dir: %w", err)
		}
		commitMsg := fmt.Sprintf("feat(previews): delete preview %s/%s\n\nDeleted by suparShip.", projectName, previewName)
		return p.commitAndPush(ctx, repoDir, commitMsg)
	})
}

// ── internal helpers ──────────────────────────────────────────────────────────

// withClonedRepo clones the gitops repository into a temp directory, runs fn
// with the repo path, and removes the temp directory when done.
func (p *Publisher) withClonedRepo(ctx context.Context, fn func(repoDir string) error) error {
	tmpDir, err := os.MkdirTemp("", "suparship-gitops-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	repoDir := filepath.Join(tmpDir, "gitops")
	cloneURL := p.embedCredentials(p.cfg.RepoURL)

	slog.Debug("gitops: cloning repo", "repo", p.cfg.RepoURL, "branch", p.cfg.Branch)
	if err := p.git(ctx, tmpDir, "clone", "--depth=1", "--branch="+p.cfg.Branch, cloneURL, repoDir); err != nil {
		return fmt.Errorf("clone gitops repo: %w", err)
	}

	if err := p.git(ctx, repoDir, "config", "user.email", "suparship@suparcloud.io"); err != nil {
		return err
	}
	if err := p.git(ctx, repoDir, "config", "user.name", "suparShip"); err != nil {
		return err
	}

	return fn(repoDir)
}

// writeFile creates parent directories and writes data to path.
func (p *Publisher) writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dirs for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// commitAndPush stages all changes, commits with msg (when there is something
// to commit), and pushes to origin. It is a no-op if the working tree is clean.
func (p *Publisher) commitAndPush(ctx context.Context, repoDir, msg string) error {
	if err := p.git(ctx, repoDir, "add", "."); err != nil {
		return err
	}
	if empty, err := p.stagedIsEmpty(ctx, repoDir); err != nil {
		return err
	} else if empty {
		slog.Debug("gitops: nothing to commit — already up to date")
		return nil
	}
	slog.Debug("gitops: committing", "msg", msg)
	if err := p.git(ctx, repoDir, "commit", "-m", msg); err != nil {
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
// Kept for backward compatibility with tests that use BuildArgoApplication directly.
func marshalHelmValues(hv helmvalues.HelmValues) (string, error) {
	b, err := yaml.Marshal(hv)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
