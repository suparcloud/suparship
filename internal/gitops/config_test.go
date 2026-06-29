package gitops

import "testing"

func TestRepoConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     RepoConfig
		wantErr error
	}{
		{
			name:    "missing repoURL",
			cfg:     RepoConfig{Provider: "github"},
			wantErr: ErrMissingRepoURL,
		},
		{
			name:    "invalid provider",
			cfg:     RepoConfig{RepoURL: "https://github.com/org/repo", Provider: "svn"},
			wantErr: ErrInvalidProvider,
		},
		{
			name: "valid github",
			cfg:  RepoConfig{RepoURL: "https://github.com/org/repo", Provider: "github"},
		},
		{
			name: "valid generic",
			cfg:  RepoConfig{RepoURL: "https://git.example.com/repo.git", Provider: "generic"},
		},
		{
			name: "valid empty provider",
			cfg:  RepoConfig{RepoURL: "https://git.example.com/repo.git"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Errorf("got err %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRepoConfig_ToPublisherConfig(t *testing.T) {
	cfg := RepoConfig{
		RepoURL:           "https://github.com/org/repo",
		Branch:            "develop",
		ArgoCDRepoURL:     "http://internal:3000/org/repo",
		KargoGitRepoURL:   "https://kargo.example.com/org/repo",
		CommitAuthorName:  "Platform Bot",
		CommitAuthorEmail: "bot@example.com",
	}

	pc := cfg.ToPublisherConfig()

	if pc.RepoURL != cfg.RepoURL {
		t.Errorf("RepoURL = %q, want %q", pc.RepoURL, cfg.RepoURL)
	}
	if pc.CommitAuthorName != "Platform Bot" || pc.CommitAuthorEmail != "bot@example.com" {
		t.Errorf("commit author = %q <%q>, want Platform Bot <bot@example.com>", pc.CommitAuthorName, pc.CommitAuthorEmail)
	}
	if pc.Branch != "develop" {
		t.Errorf("Branch = %q, want develop", pc.Branch)
	}
	if pc.ArgoCDRepoURL != cfg.ArgoCDRepoURL {
		t.Errorf("ArgoCDRepoURL = %q, want %q", pc.ArgoCDRepoURL, cfg.ArgoCDRepoURL)
	}
	if pc.KargoGitRepoURL != cfg.KargoGitRepoURL {
		t.Errorf("KargoGitRepoURL = %q, want %q", pc.KargoGitRepoURL, cfg.KargoGitRepoURL)
	}
}

func TestRepoConfig_ToPublisherConfig_Defaults(t *testing.T) {
	cfg := RepoConfig{
		RepoURL: "https://github.com/org/repo",
	}

	pc := cfg.ToPublisherConfig()

	if pc.Branch != "main" {
		t.Errorf("Branch = %q, want main (default)", pc.Branch)
	}
	if pc.ArgoCDRepoURL != cfg.RepoURL {
		t.Errorf("ArgoCDRepoURL = %q, want %q (fallback to RepoURL)", pc.ArgoCDRepoURL, cfg.RepoURL)
	}
	if pc.KargoGitRepoURL != cfg.RepoURL {
		t.Errorf("KargoGitRepoURL = %q, want %q (fallback)", pc.KargoGitRepoURL, cfg.RepoURL)
	}
}
