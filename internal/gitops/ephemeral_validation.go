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
// release history and Kargo Stage survive cleanup. Stateful components are rejected
// by the API; chart-managed resources in the selected environment are disposable.
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
			if err := p.removeValidationWorkloads(repo, app, env); err != nil {
				return err
			}
		}
		return p.commitAndPush(ctx, repo, fmt.Sprintf("chore: set validation workloads active=%t for %s/%s", active, app.ProjectName, app.Name))
	})
}

func (p *Publisher) removeValidationWorkloads(repo string, app *domain.App, env AppPublishEnv) error {
	// Keep values and platform config, remove only Application inputs. This avoids
	// pruning shared environment infrastructure or deleting a namespace.
	for _, base := range []string{"envs", "envs-external", composedAppsDir} {
		dir := p.outputDir(repo, base, env.EnvName, app.ProjectName, app.Name, "_targets")
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}
