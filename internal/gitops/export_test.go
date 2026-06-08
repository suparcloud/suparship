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

// PublishAppEnvForTest exposes PublishAppEnv's inner publishAppFiles call for
// white-box unit testing without git operations.
func (p *Publisher) PublishAppEnvForTest(repoDir string, app *domain.App, env AppPublishEnv) error {
	return p.publishAppFiles(repoDir, app, []AppPublishEnv{env})
}

// FirstDeployEnvsForTest exposes firstDeployEnvs for unit testing.
func FirstDeployEnvsForTest(envs []AppPublishEnv) []AppPublishEnv {
	return firstDeployEnvs(envs)
}

// ChartVersionDirForTest / ChartPathForTest expose the version-scoped chart
// path helpers to white-box tests.
func ChartVersionDirForTest(version string) string    { return chartVersionDir(version) }
func ChartPathForTest(template, version string) string { return chartPathFor(template, version) }
