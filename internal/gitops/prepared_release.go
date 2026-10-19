package gitops

import (
	"context"
	"fmt"
	"os"

	"github.com/suparcloud/suparship/internal/domain"
	"gopkg.in/yaml.v3"
)

func releaseStatePath(subPath, project, app, env string) string {
	return joinSubPath(subPath, ".suparship", "releases", project, app, env+".yaml")
}

// Preserve preparation across ordinary config republishes and process restarts.
// Kargo clears this marker atomically with the selected image updates.
func (p *Publisher) promotionPending(repo string, app *domain.App, env AppPublishEnv) (bool, error) {
	path := repo + "/" + releaseStatePath(p.cfg.SubPath, app.ProjectName, app.Name, env.EnvName)
	state := struct {
		Pending bool `yaml:"pending"`
	}{Pending: env.PrepareOnly}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = yaml.Unmarshal(data, &state); err != nil {
			return false, err
		}
	} else if !os.IsNotExist(err) {
		return false, err
	}
	data, err = yaml.Marshal(state)
	if err != nil {
		return false, err
	}
	if err = p.writeFile(path, data); err != nil {
		return false, err
	}
	return state.Pending, nil
}

// PrepareAppEnv creates an inert Application for Kargo. No seed image may sync.
func (p *Publisher) PrepareAppEnv(ctx context.Context, app *domain.App, env AppPublishEnv) error {
	if !env.Bound {
		return fmt.Errorf("target environment has no bound cluster")
	}
	env.PrepareOnly = true
	return p.PublishAppEnv(ctx, app, env)
}

func releaseEnvTree(opts KargoBuildOptions) string {
	if opts.ChartMode == AppMetadataChartTypeExternal {
		return "envs-external"
	}
	return "envs"
}

func activatePreparedRelease(steps []PromotionStep, app *domain.App, env domain.AppEnvironment, opts KargoBuildOptions) []PromotionStep {
	update := func(path, key string, value any) PromotionStep {
		return PromotionStep{Uses: "yaml-update", Config: map[string]any{"path": "./src/" + path, "updates": []map[string]any{{"key": key, "value": value}}}}
	}
	activation := []PromotionStep{update(releaseStatePath(opts.SubPath, app.ProjectName, app.Name, env.EnvName), "pending", false)}
	clusters := opts.Clusters
	if len(clusters) == 0 {
		clusters = []ClusterTarget{{Name: fallbackClusterName}}
	}
	for _, cluster := range clusters {
		name := cluster.Name
		if name == "" {
			name = fallbackClusterName
		}
		if opts.Composed {
			base := joinSubPath(opts.SubPath, composedAppsDir, env.EnvName, app.ProjectName, app.Name, "_targets", name)
			if len(app.Spec.ComposedComponents()) > len(app.Spec.StatefulComponents()) {
				activation = append(activation, activationCopy(opts, app, env, name+"-application.yaml", base+"/application.yaml"))
			}
			for _, c := range app.Spec.StatefulComponents() {
				activation = append(activation, activationCopy(opts, app, env, name+"-"+c.Name+"-application.yaml", base+"/"+c.Name+"-application.yaml"))
			}
		} else {
			activation = append(activation, update(joinSubPath(opts.SubPath, releaseEnvTree(opts), env.EnvName, app.ProjectName, app.Name, "_targets", name, "app.yaml"), "promotionPending", "false"))
		}
	}
	out := []PromotionStep{}
	for _, step := range steps {
		if step.Uses == "git-commit" {
			out = append(out, activation...)
		}
		out = append(out, step)
	}
	return out
}

// Include every workload Application in sync and health verification.
func releaseArgoUpdateApps(app *domain.App, env domain.AppEnvironment, opts KargoBuildOptions) []map[string]any {
	apps := argoUpdateApps(opts.ArgoAppNamePattern, app.ProjectName, app.Name, env.EnvName, opts.Clusters)

	if opts.Composed {
		expanded := []map[string]any{}
		for _, a := range apps {
			if len(app.Spec.ComposedComponents()) > len(app.Spec.StatefulComponents()) {
				expanded = append(expanded, a)
			}
			for _, c := range app.Spec.StatefulComponents() {
				expanded = append(expanded, map[string]any{"name": fmt.Sprint(a["name"]) + "-" + c.Name, "namespace": defaultArgoCDNS})
			}
		}
		apps = expanded
	}
	return apps
}

func disablePreparedSync(app *Application) {
	if app.Spec.SyncPolicy != nil {
		app.Spec.SyncPolicy.Automated = nil
	}
}

func activeApplicationPath(subPath, project, app, env, name string) string {
	return joinSubPath(subPath, ".suparship", "releases", project, app, env, name)
}
func (p *Publisher) saveActiveApplication(repo string, app *domain.App, env AppPublishEnv, name string, manifest *Application) error {
	if manifest.Metadata.Annotations == nil {
		manifest.Metadata.Annotations = map[string]string{}
	}
	manifest.Metadata.Annotations[validationIdleAnnotation] = "false"
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	return p.writeFile(repo+"/"+activeApplicationPath(p.cfg.SubPath, app.ProjectName, app.Name, env.EnvName, name), data)
}
func activationCopy(opts KargoBuildOptions, app *domain.App, env domain.AppEnvironment, name, out string) PromotionStep {
	return PromotionStep{Uses: "copy", Config: map[string]any{"inPath": "./src/" + activeApplicationPath(opts.SubPath, app.ProjectName, app.Name, env.EnvName, name), "outPath": "./src/" + out}}
}
