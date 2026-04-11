package gitops

import "github.com/suparcloud/suparship/internal/domain"

// PublishKargoCRsForTest exposes publishKargoCRs for white-box unit testing.
// It writes Kargo Namespace, Warehouse, and Stage files into repoDir/gitops-output/_infra/kargo/
// without any git operations so tests can assert on the generated YAML without
// needing a real git repository.
func (p *Publisher) PublishKargoCRsForTest(repoDir string, app *domain.App, envs []AppPublishEnv) error {
	return p.publishKargoCRs(repoDir, app, envs)
}
