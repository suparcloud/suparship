package gitops

import (
	"context"

	"github.com/suparcloud/suparship/internal/domain"
)

// SyncChartForTest exposes syncChart's local-disk + cluster-bundle resolution
// to white-box tests that don't want to spin up a real git clone.
func (p *Publisher) SyncChartForTest(ctx context.Context, repoDir, templateName, version string) error {
	return p.syncChart(ctx, repoDir, templateName, version)
}

// SyncAddonChartsForTest exposes syncAddonCharts so tests can assert that an
// app's addon wrapper charts are materialised under charts/<chart>/latest/.
func (p *Publisher) SyncAddonChartsForTest(ctx context.Context, repoDir string, app *domain.App, envs []AppPublishEnv) error {
	return p.syncAddonCharts(ctx, repoDir, app, envs)
}

// RemoveAppEnvFilesForTest exposes removeAppEnvFiles for white-box unit testing.
func (p *Publisher) RemoveAppEnvFilesForTest(repoDir, projectName, appName, envName string) (bool, error) {
	return p.removeAppEnvFiles(repoDir, projectName, appName, envName)
}

// CleanupKargoCRsForTest exposes cleanupKargoCRs for white-box unit testing.
func (p *Publisher) CleanupKargoCRsForTest(repoDir string, app *domain.App) error {
	return p.cleanupKargoCRs(repoDir, app)
}

// PublishKargoCRsForTest exposes publishKargoCRs for white-box unit testing.
// It writes Kargo Namespace, Warehouse, and Stage files into repoDir/gitops-output/_infra/kargo/
// without any git operations so tests can assert on the generated YAML without
// needing a real git repository.
func (p *Publisher) PublishKargoCRsForTest(repoDir string, app *domain.App, envs []AppPublishEnv) error {
	return p.publishKargoCRs(repoDir, app, envs)
}

// PublishAppFilesForTest exposes the per-env file writing loop of PublishApp
// for white-box unit testing without git operations.
func (p *Publisher) PublishAppFilesForTest(repoDir string, app *domain.App, envs []AppPublishEnv) error {
	return p.publishAppFiles(repoDir, app, envs)
}

// WriteComposedAppTreeForTest exposes writeComposedAppTree — the composed-app
// rendering path (per-component values + rendered multi-source Application +
// per-env App-of-Apps) — to white-box tests without git operations.
func (p *Publisher) WriteComposedAppTreeForTest(ctx context.Context, repoDir string, app *domain.App, envs []AppPublishEnv) error {
	return p.writeComposedAppTree(ctx, repoDir, app, envs)
}

// WriteAppTreeForTest exposes writeAppTree — the mode-branching entry that picks
// single vs composed and prunes the stale-mode tree on a transition — to tests.
func (p *Publisher) WriteAppTreeForTest(ctx context.Context, repoDir string, app *domain.App, envs []AppPublishEnv) error {
	return p.writeAppTree(ctx, repoDir, app, envs)
}

// PublishAppEnvForTest exposes PublishAppEnv's inner publishAppFiles call for
// white-box unit testing without git operations.
func (p *Publisher) PublishAppEnvForTest(repoDir string, app *domain.App, env AppPublishEnv) error {
	return p.publishAppFiles(repoDir, app, []AppPublishEnv{env})
}

// PublishPreviewForTest exposes PublishPreview's inner file generation for
// white-box unit testing without git operations.
func (p *Publisher) PublishPreviewForTest(repoDir string, app *domain.App, preview PreviewPublishSpec) error {
	return p.publishPreviewFiles(repoDir, app, preview)
}

// DeletePreviewForTest exposes DeletePreview's inner file removal for white-box
// unit testing without git operations. Returns whether anything was removed.
func (p *Publisher) DeletePreviewForTest(repoDir, projectName, previewName, appName, baseEnv string) (bool, error) {
	return p.deletePreviewFiles(repoDir, projectName, previewName, appName, baseEnv)
}

// FirstDeployEnvsForTest exposes firstDeployEnvs for unit testing.
func FirstDeployEnvsForTest(envs []AppPublishEnv) []AppPublishEnv {
	return firstDeployEnvs(envs)
}

// EnabledDeployEnvsForTest exposes enabledDeployEnvs for unit testing.
func EnabledDeployEnvsForTest(app *domain.App, envs []AppPublishEnv) []AppPublishEnv {
	return enabledDeployEnvs(app, envs)
}

// ChartVersionDirForTest / ChartPathForTest expose the version-scoped chart
// path helpers to white-box tests.
func ChartVersionDirForTest(version string) string     { return chartVersionDir(version) }
func ChartPathForTest(template, version string) string { return chartPathFor(template, version) }
