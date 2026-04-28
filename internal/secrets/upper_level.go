package secrets

import "context"

// UpperLevelWriter abstracts write/read/delete for every scope above the raw
// per-namespace K8s Secret. Implementations route writes to the right backing
// store: the K8s implementation writes to suparship-system with Replicator
// annotations; the 1Password implementation writes vault items; the in-memory
// implementation stores in-process.
//
// The interface covers all six scopes — org, env-type, project, app, app-env,
// cluster — so the handler can call the active writer without knowing which
// backend the org chose.
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

	// App scope: shared across every env of one app. Routes to the
	// platform-shared 1Password vault on 1Password backend, or to a K8s
	// Secret in suparship-system replicated by Stakater on K8s.
	WriteAppSecrets(ctx context.Context, project, app string, data map[string][]byte) error
	ReadAppSecretKeys(ctx context.Context, project, app string) ([]SecretEntry, error)
	DeleteAppSecretKey(ctx context.Context, project, app, key string) error

	// App-env scope: one env of one app. Routes to the env's 1Password vault
	// on 1Password backend, or to a K8s Secret in suparship-system with
	// replicate-to matching the env namespace by name on K8s. The namespace
	// argument is the resolved env-namespace name — used as the replicator
	// target by the K8s impl, ignored by 1Password and Mem.
	WriteAppEnvSecrets(ctx context.Context, project, app, env, namespace string, data map[string][]byte) error
	ReadAppEnvSecretKeys(ctx context.Context, project, app, env string) ([]SecretEntry, error)
	DeleteAppEnvSecretKey(ctx context.Context, project, app, env, key string) error
}
