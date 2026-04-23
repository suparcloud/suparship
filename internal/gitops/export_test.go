package gitops

import "github.com/suparcloud/suparship/internal/domain"

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
