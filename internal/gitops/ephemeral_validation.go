package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/suparcloud/suparship/internal/domain"
	"gopkg.in/yaml.v3"
)

func validationStatePath(sub, project, app, env string) string {
	return joinSubPath(sub, ".suparship", "validation", project, app, env+".yaml")
}

// Git is authoritative for dormancy, so a config republish or process restart
// cannot resurrect a completed run. Missing state starts an ephemeral env idle.
func (p *Publisher) validationDormant(repo string, app *domain.App, env AppPublishEnv) (bool, error) {
	if !app.Spec.CD.EphemeralValidation() || env.EnvName != app.Spec.CD.ReleaseValidationEnvironment {
		return false, nil
	}
	data, err := os.ReadFile(filepath.Join(repo, validationStatePath(p.cfg.SubPath, app.ProjectName, app.Name, env.EnvName)))
	if os.IsNotExist(err) {
		// Switching an existing persistent deployment must first hand scheduling
		// to the controller. Do not prune it during the settings publication.
		for _, base := range []string{"envs", "envs-external", composedAppsDir} {
			if _, statErr := os.Stat(p.outputDir(repo, base, env.EnvName, app.ProjectName, app.Name, "_targets")); statErr == nil {
				return false, nil
			} else if !os.IsNotExist(statErr) {
				return false, statErr
			}
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var state struct {
		Dormant bool `yaml:"dormant"`
	}
	if err = yaml.Unmarshal(data, &state); err != nil {
		return false, err
	}
	return state.Dormant, nil
}

// SetValidationWorkloads changes only this app's workload resources. Namespace,
// release history, Argo Applications and Kargo Stage survive cleanup. Only
// resources in the explicitly disposable validation environment are pruned.
func (p *Publisher) SetValidationWorkloads(ctx context.Context, app *domain.App, env AppPublishEnv, active bool) error {
	if !app.Spec.CD.EphemeralValidation() || env.EnvName != app.Spec.CD.ReleaseValidationEnvironment {
		return fmt.Errorf("not an ephemeral validation environment")
	}
	return p.withClonedRepo(ctx, func(repo string) error {
		state := []byte(fmt.Sprintf("dormant: %t\n", !active))
		if err := p.writeFile(filepath.Join(repo, validationStatePath(p.cfg.SubPath, app.ProjectName, app.Name, env.EnvName)), state); err != nil {
			return err
		}
		if active {
			// Always prepare inertly. Kargo activates the selected Freight, never a seed.
			if err := p.writeFile(filepath.Join(repo, releaseStatePath(p.cfg.SubPath, app.ProjectName, app.Name, env.EnvName)), []byte("pending: true\n")); err != nil {
				return err
			}
			env.PrepareOnly = true
			if err := p.publishEnvFiles(ctx, repo, app, env); err != nil {
				return err
			}
		} else {
			if !app.Spec.IsComposed() {
				// Older cleanup deleted the ApplicationSet input. Re-render it inside
				// this transaction, then park it before committing any active sources.
				env.PrepareOnly = true
				if err := p.publishEnvFiles(ctx, repo, app, env); err != nil {
					return err
				}
			}
			if err := p.removeValidationWorkloads(repo, app, env); err != nil {
				return err
			}
		}
		return p.commitAndPush(ctx, repo, fmt.Sprintf("chore: set validation workloads active=%t for %s/%s", active, app.ProjectName, app.Name))
	})
}

const validationIdleAnnotation = "suparship.io/validation-idle"

func (p *Publisher) removeValidationWorkloads(repo string, app *domain.App, env AppPublishEnv) error {
	if app.Spec.CD.EphemeralValidation() && env.EnvName == app.Spec.CD.ReleaseValidationEnvironment {
		return p.parkValidationApplications(repo, app, env)
	}
	return p.deleteValidationTargets(repo, app, env)
}

func (p *Publisher) deleteValidationTargets(repo string, app *domain.App, env AppPublishEnv) error {
	// Keep values and platform config, remove only Application inputs. This avoids
	// pruning shared environment infrastructure or deleting a namespace.
	for _, base := range []string{"envs", "envs-external", composedAppsDir} {
		dir := p.outputDir(repo, base, env.EnvName, app.ProjectName, app.Name, "_targets")
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return p.ensureValidationCleanupRoot(repo, env.EnvName)
}

// An idle validation environment is intentionally empty. Keep its directory
// tracked and allow the composed parent to prune its last child Applications.
func (p *Publisher) ensureValidationCleanupRoot(repo, envName string) error {
	if err := p.writeFile(p.outputDir(repo, composedAppsDir, envName, ".gitkeep"), nil); err != nil {
		return err
	}
	path := p.outputDir(repo, "_infra", envName+"-composed-appset.yaml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var root Application
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	if root.Spec.SyncPolicy == nil || root.Spec.SyncPolicy.Automated == nil {
		return nil
	}
	root.Spec.SyncPolicy.Automated.AllowEmpty = true
	data, err = yaml.Marshal(root)
	if err != nil {
		return err
	}
	return p.writeFile(path, data)
}

// Keep the Application identity for Kargo's continuous health checks, while
// Argo prunes the disposable workloads using an empty Git source.
func (p *Publisher) parkValidationApplications(repo string, app *domain.App, env AppPublishEnv) error {
	composed := app.Spec.IsComposed()
	app = domain.AppForReleaseValidation(app, env.EnvName)
	emptyPath := joinSubPath(p.cfg.SubPath, ".suparship", "validation-empty")
	if err := p.writeFile(filepath.Join(repo, emptyPath, ".gitkeep"), nil); err != nil {
		return err
	}
	if !composed {
		for _, tree := range []string{"envs", "envs-external"} {
			root := p.outputDir(repo, tree, env.EnvName, app.ProjectName, app.Name, "_targets")
			err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if os.IsNotExist(err) {
					return nil
				}
				if err != nil {
					return err
				}
				if entry.IsDir() || entry.Name() != "app.yaml" {
					return nil
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				var meta AppMetadata
				if err := yaml.Unmarshal(data, &meta); err != nil {
					return err
				}
				meta.ValidationIdle = "true"
				meta.ValidationEmptyPath = emptyPath
				data, err = yaml.Marshal(meta)
				if err != nil {
					return err
				}
				return p.writeFile(path, data)
			})
			if err != nil {
				return err
			}
		}
		return p.ensureValidationCleanupRoot(repo, env.EnvName)
	}
	saved := filepath.Join(repo, activeApplicationPath(p.cfg.SubPath, app.ProjectName, app.Name, env.EnvName, ""))
	targets := env.Clusters
	if len(targets) == 0 {
		targets = []ClusterTarget{{Name: env.ClusterRef}}
	}
	expected := map[string]bool{}
	for _, target := range targets {
		cluster := target.Name
		if cluster == "" {
			cluster = fallbackClusterName
		}
		files := []string{}
		if len(app.Spec.ComposedComponents()) > len(app.Spec.StatefulComponents()) {
			files = append(files, "application.yaml")
		}
		for _, c := range app.Spec.StatefulComponents() {
			files = append(files, c.Name+"-application.yaml")
		}
		for _, filename := range files {
			expected[p.outputDir(repo, composedAppsDir, env.EnvName, app.ProjectName, app.Name, "_targets", cluster, filename)] = true
			data, err := os.ReadFile(filepath.Join(saved, cluster+"-"+filename))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			var manifest Application
			if err := yaml.Unmarshal(data, &manifest); err != nil {
				return err
			}
			if manifest.Metadata.Annotations == nil {
				manifest.Metadata.Annotations = map[string]string{}
			}
			manifest.Metadata.Annotations[validationIdleAnnotation] = "true"
			manifest.Spec.Sources = nil
			manifest.Spec.Source = ApplicationSource{RepoURL: p.cfg.ArgoCDRepoURL, TargetRevision: p.cfg.Branch, Path: emptyPath}
			manifest.Spec.SyncPolicy = &SyncPolicy{Automated: &AutomatedSyncPolicy{Prune: true, SelfHeal: true, AllowEmpty: true}}
			data, err = yaml.Marshal(manifest)
			if err != nil {
				return err
			}
			if err := p.writeFile(p.outputDir(repo, composedAppsDir, env.EnvName, app.ProjectName, app.Name, "_targets", cluster, filename), data); err != nil {
				return err
			}
		}
	}

	// Selection changes while idle must not leave excluded Application shells.
	root := p.outputDir(repo, composedAppsDir, env.EnvName, app.ProjectName, app.Name, "_targets")
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !entry.IsDir() && !expected[path] {
			return os.Remove(path)
		}
		return nil
	}); err != nil {
		return err
	}

	return p.ensureValidationCleanupRoot(repo, env.EnvName)
}
