package gitops

import "errors"

var (
	ErrMissingRepoURL = errors.New("gitops: repoURL is required")
	ErrInvalidProvider = errors.New("gitops: provider must be one of github, gitlab, bitbucket, gitea, generic")
	ErrConfigNotFound  = errors.New("gitops: configuration not found")
)
