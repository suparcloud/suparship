package gitops

import (
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/helmvalues"
	"gopkg.in/yaml.v3"
	"path/filepath"
)

const ValidationNamePattern = "{projectApp}-{env}-{cluster}"

func validationPattern(app *domain.App, env, fallback string) string {
	if app.Spec.CD.ManagedValidation() && env == domain.ManagedValidationEnv {
		return ValidationNamePattern
	}
	return fallback
}

// ValidationApp inherits configuration only: pins, suspension and deployment
// controls remain independent. Copies keep base/stored settings immutable.
func ValidationApp(app *domain.App, base string) *domain.App {
	if !app.Spec.CD.ManagedValidation() || base == "" {
		return app
	}
	out := *app
	out.Spec.EnvironmentDefaults = make(map[string]domain.EnvironmentOverride, len(app.Spec.EnvironmentDefaults))
	for k, v := range app.Spec.EnvironmentDefaults {
		out.Spec.EnvironmentDefaults[k] = v
	}
	source, target := app.Spec.EnvironmentDefaults[base], app.Spec.EnvironmentDefaults[domain.ManagedValidationEnv]
	target.RawValues = helmvalues.DeepMerge(helmvalues.DeepCopyMap(source.RawValues), helmvalues.DeepCopyMap(target.RawValues))
	target.Values = helmvalues.DeepMerge(helmvalues.DeepCopyMap(source.Values), helmvalues.DeepCopyMap(target.Values))
	components := map[string]map[string]any{}
	for k, v := range source.ComponentValues {
		components[k] = helmvalues.DeepCopyMap(v)
	}
	for k, v := range target.ComponentValues {
		components[k] = helmvalues.DeepMerge(components[k], helmvalues.DeepCopyMap(v))
	}
	target.ComponentValues = components
	versions := map[string]string{}
	for k, v := range source.TemplateVersions {
		versions[k] = v
	}
	for k, v := range target.TemplateVersions {
		versions[k] = v
	}
	target.TemplateVersions = versions
	componentVars := map[string]domain.ComponentEnvOverride{}
	for k, v := range source.ComponentEnvVars {
		componentVars[k] = v
	}
	for k, v := range target.ComponentEnvVars {
		componentVars[k] = v
	}
	target.ComponentEnvVars = componentVars
	clusters := map[string]domain.ClusterValueOverride{}
	for k, v := range source.ClusterOverrides {
		clusters[k] = v
	}
	for k, v := range target.ClusterOverrides {
		clusters[k] = v
	}
	target.ClusterOverrides = clusters
	out.Spec.EnvironmentDefaults[domain.ManagedValidationEnv] = target
	return &out
}

func secretSourceEnv(env AppPublishEnv) string {
	if env.BaseEnv != "" {
		return env.BaseEnv
	}
	return env.EnvName
}

func (p *Publisher) platformResourcesDir(repo string, app *domain.App, env AppPublishEnv) string {
	base := "_app-resources"
	if env.BaseEnv != "" {
		base = "_validation-resources"
	}
	return p.outputDir(repo, base, env.EnvName, app.ProjectName, app.Name)
}
func (p *Publisher) writeValidationPlatformApplications(repo string, app *domain.App, env AppPublishEnv) error {
	for _, target := range env.Clusters {
		name := RenderArgoAppName(ValidationNamePattern, app.ProjectName, app.Name, env.EnvName, target.Name) + "-platform"
		manifest := Application{APIVersion: "argoproj.io/v1alpha1", Kind: "Application", Metadata: ObjectMeta{Name: name, Namespace: defaultArgoCDNS, Finalizers: []string{ArgoCDResourcesFinalizer}, Labels: map[string]string{labelApp: app.Name, labelProject: app.ProjectName, labelEnv: env.EnvName, labelCluster: target.Name}}, Spec: ApplicationSpec{Project: app.ProjectName, Source: ApplicationSource{RepoURL: p.cfg.ArgoCDRepoURL, TargetRevision: defaultTargetRevision, Path: joinSubPath(p.cfg.SubPath, "_validation-resources", env.EnvName, app.ProjectName, app.Name), Directory: &DirectorySource{Include: platformResourcesInclude}}, Destination: ApplicationDestination{Server: target.Server, Namespace: env.Namespace}, SyncPolicy: platformSyncPolicy(AppSetOptions{SyncAutomated: p.cfg.SyncAutomated})}}
		data, err := yaml.Marshal(manifest)
		if err != nil {
			return err
		}
		if err := p.writeFile(filepath.Join(p.outputDir(repo, "_infra"), name+".yaml"), data); err != nil {
			return err
		}
	}
	return nil
}
