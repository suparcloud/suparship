package secrets

import "context"

// UpperLevelWriter abstracts write/read/delete for upper-level (org, env-type,
// project, cluster) secrets. The K8s implementation writes to suparship-system
// with Replicator annotations; the in-memory implementation stores in-process.
type UpperLevelWriter interface {
	WriteOrgSecrets(ctx context.Context, data map[string][]byte) error
	ReadOrgSecretKeys(ctx context.Context) ([]SecretEntry, error)
	DeleteOrgSecretKey(ctx context.Context, key string) error

	WriteEnvTypeSecrets(ctx context.Context, envType string, data map[string][]byte) error
	ReadEnvTypeSecretKeys(ctx context.Context, envType string) ([]SecretEntry, error)
	DeleteEnvTypeSecretKey(ctx context.Context, envType, key string) error

	WriteProjectSecrets(ctx context.Context, project string, data map[string][]byte) error
	ReadProjectSecretKeys(ctx context.Context, project string) ([]SecretEntry, error)
	DeleteProjectSecretKey(ctx context.Context, project, key string) error

	// Cluster-scope secrets are platform-engineering escape hatches that
	// override every other layer (including app-env). Replicated only into
	// namespaces of apps deployed onto the named cluster.
	WriteClusterSecrets(ctx context.Context, cluster string, data map[string][]byte) error
	ReadClusterSecretKeys(ctx context.Context, cluster string) ([]SecretEntry, error)
	DeleteClusterSecretKey(ctx context.Context, cluster, key string) error
}
