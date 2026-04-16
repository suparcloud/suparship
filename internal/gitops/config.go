package gitops

// RepoConfig is the persisted GitOps repository configuration.
// It corresponds to the suparship-gitops-config ConfigMap created by the
// Helm chart and can also be written via the settings API.
type RepoConfig struct {
	// Provider identifies the Git hosting service: github, gitlab, bitbucket, gitea, generic.
	Provider string `json:"provider" yaml:"provider"`
	// RepoURL is the clone URL (HTTPS or SSH).
	RepoURL string `json:"repoURL" yaml:"repoURL"`
	// Branch to commit to. Defaults to "main".
	Branch string `json:"branch" yaml:"branch"`
	// SubPath is an optional directory within the repo for gitops content.
	SubPath string `json:"subPath,omitempty" yaml:"subPath,omitempty"`
	// AuthSecretRef is the name of an existing Kubernetes Secret holding credentials.
	AuthSecretRef string `json:"authSecretRef,omitempty" yaml:"authSecretRef,omitempty"`
	// CredentialExpiresAt is an ISO 8601 timestamp for credential health warnings.
	CredentialExpiresAt string `json:"credentialExpiresAt,omitempty" yaml:"credentialExpiresAt,omitempty"`
	// ArgoCDRepoURL is the URL ArgoCD uses to sync (may be an in-cluster URL).
	ArgoCDRepoURL string `json:"argoCDRepoURL,omitempty" yaml:"argoCDRepoURL,omitempty"`
	// KargoGitRepoURL is the HTTPS URL Kargo uses for gitRepoUpdates.
	KargoGitRepoURL string `json:"kargoGitRepoURL,omitempty" yaml:"kargoGitRepoURL,omitempty"`
	// InitializeRepo causes suparship to initialize the repo structure on first boot.
	InitializeRepo bool `json:"initializeRepo" yaml:"initializeRepo"`
	// Initialized is set to true after the repo has been successfully initialized.
	Initialized bool `json:"initialized" yaml:"initialized"`
	// GitHub holds provider-specific fields for GitHub App auth.
	GitHub *GitHubConfig `json:"github,omitempty" yaml:"github,omitempty"`
	// Bitbucket holds provider-specific fields for Bitbucket.
	Bitbucket *BitbucketConfig `json:"bitbucket,omitempty" yaml:"bitbucket,omitempty"`
}

// GitHubConfig holds GitHub-specific non-secret configuration.
type GitHubConfig struct {
	AppID          string `json:"appId,omitempty" yaml:"appId,omitempty"`
	InstallationID string `json:"installationId,omitempty" yaml:"installationId,omitempty"`
}

// BitbucketConfig holds Bitbucket-specific non-secret configuration.
type BitbucketConfig struct {
	Workspace string `json:"workspace,omitempty" yaml:"workspace,omitempty"`
}

// Validate returns an error if the config is not usable.
func (c *RepoConfig) Validate() error {
	if c.RepoURL == "" {
		return ErrMissingRepoURL
	}
	switch c.Provider {
	case "github", "gitlab", "bitbucket", "gitea", "generic", "":
		// ok
	default:
		return ErrInvalidProvider
	}
	return nil
}

// ToPublisherConfig converts a persisted RepoConfig to a PublisherConfig
// suitable for the gitops.Publisher. Credential fields (user/password) must
// be resolved from the referenced Secret separately.
func (c *RepoConfig) ToPublisherConfig() PublisherConfig {
	branch := c.Branch
	if branch == "" {
		branch = "main"
	}
	argoURL := c.ArgoCDRepoURL
	if argoURL == "" {
		argoURL = c.RepoURL
	}
	kargoURL := c.KargoGitRepoURL
	if kargoURL == "" {
		kargoURL = argoURL
	}
	return PublisherConfig{
		RepoURL:         c.RepoURL,
		ArgoCDRepoURL:   argoURL,
		KargoGitRepoURL: kargoURL,
		Branch:          branch,
	}
}
