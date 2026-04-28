package secrets

import (
	"context"
	"fmt"
)

// MigrateUpperLevelInput captures the inventory of upper-level scopes to copy
// during a backend migration.
//
// Org and AppEnv keys are global. EnvTypes lists the env-type names whose
// secrets should be migrated (e.g. "staging", "prod"). Projects lists project
// names. Clusters lists registered cluster names. The caller is responsible
// for collecting these — typically by reading the org config and project
// store at migration time.
type MigrateUpperLevelInput struct {
	EnvTypes []string
	Projects []string
	Clusters []string
}

// MigrateUpperLevelResult reports how many keys were copied per scope. Useful
// for showing the operator what was moved.
type MigrateUpperLevelResult struct {
	OrgKeys     int
	EnvTypeKeys map[string]int
	ProjectKeys map[string]int
	ClusterKeys map[string]int
}

// upperLevelReader is the read-only subset of UpperLevelWriter the migration
// helper needs. Deliberately defined as a separate interface so callers can
// pass either a real K8s writer or a mock.
type upperLevelReader interface {
	ReadOrgSecretKeys(ctx context.Context) ([]SecretEntry, error)
	ReadEnvTypeSecretKeys(ctx context.Context, envType string) ([]SecretEntry, error)
	ReadProjectSecretKeys(ctx context.Context, project string) ([]SecretEntry, error)
	ReadClusterSecretKeys(ctx context.Context, cluster string) ([]SecretEntry, error)
}

// ValueReader returns the value for a single key at a given scope. Used by
// MigrateUpperLevelSecrets to read raw bytes from the source backend so the
// destination receives the full value, not just the key name. The K8s
// UpperLevelSecretWriter satisfies this via its underlying client; an adapter
// can wrap any storage that exposes per-key reads.
type ValueReader interface {
	ReadOrgSecretValue(ctx context.Context, key string) ([]byte, error)
	ReadEnvTypeSecretValue(ctx context.Context, envType, key string) ([]byte, error)
	ReadProjectSecretValue(ctx context.Context, project, key string) ([]byte, error)
	ReadClusterSecretValue(ctx context.Context, cluster, key string) ([]byte, error)
}

// MigrateUpperLevelSecrets copies all org / env-type / project / cluster
// secret values from src to dst. Idempotent: dst's Write*Secrets calls merge
// with existing fields (matching K8sUpperLevelSecretWriter and
// SAUpperLevelWriter semantics), so re-running picks up new keys without
// clobbering values already entered directly into the destination vault.
//
// The function returns partial progress on error: the result reflects what was
// successfully copied before the first failure. Callers should log the result
// and report the error.
//
// Note: app and app-env secrets are NOT migrated by this function — they live
// in env-bound K8s namespaces (not in suparship-system) and follow a different
// lifecycle. Operators rotate those via the app-env UI after the backend
// switch.
func MigrateUpperLevelSecrets(
	ctx context.Context,
	src interface {
		upperLevelReader
		ValueReader
	},
	dst UpperLevelWriter,
	input MigrateUpperLevelInput,
) (MigrateUpperLevelResult, error) {
	result := MigrateUpperLevelResult{
		EnvTypeKeys: make(map[string]int),
		ProjectKeys: make(map[string]int),
		ClusterKeys: make(map[string]int),
	}

	// Org scope.
	orgKeys, err := src.ReadOrgSecretKeys(ctx)
	if err != nil {
		return result, fmt.Errorf("read org keys: %w", err)
	}
	if len(orgKeys) > 0 {
		data := make(map[string][]byte, len(orgKeys))
		for _, e := range orgKeys {
			v, err := src.ReadOrgSecretValue(ctx, e.Key)
			if err != nil {
				return result, fmt.Errorf("read org value %q: %w", e.Key, err)
			}
			data[e.Key] = v
		}
		if err := dst.WriteOrgSecrets(ctx, data); err != nil {
			return result, fmt.Errorf("write org secrets: %w", err)
		}
		result.OrgKeys = len(data)
	}

	for _, envType := range input.EnvTypes {
		entries, err := src.ReadEnvTypeSecretKeys(ctx, envType)
		if err != nil {
			return result, fmt.Errorf("read env-type %q keys: %w", envType, err)
		}
		if len(entries) == 0 {
			continue
		}
		data := make(map[string][]byte, len(entries))
		for _, e := range entries {
			v, err := src.ReadEnvTypeSecretValue(ctx, envType, e.Key)
			if err != nil {
				return result, fmt.Errorf("read env-type %q value %q: %w", envType, e.Key, err)
			}
			data[e.Key] = v
		}
		if err := dst.WriteEnvTypeSecrets(ctx, envType, data); err != nil {
			return result, fmt.Errorf("write env-type %q secrets: %w", envType, err)
		}
		result.EnvTypeKeys[envType] = len(data)
	}

	for _, project := range input.Projects {
		entries, err := src.ReadProjectSecretKeys(ctx, project)
		if err != nil {
			return result, fmt.Errorf("read project %q keys: %w", project, err)
		}
		if len(entries) == 0 {
			continue
		}
		data := make(map[string][]byte, len(entries))
		for _, e := range entries {
			v, err := src.ReadProjectSecretValue(ctx, project, e.Key)
			if err != nil {
				return result, fmt.Errorf("read project %q value %q: %w", project, e.Key, err)
			}
			data[e.Key] = v
		}
		if err := dst.WriteProjectSecrets(ctx, project, data); err != nil {
			return result, fmt.Errorf("write project %q secrets: %w", project, err)
		}
		result.ProjectKeys[project] = len(data)
	}

	for _, cluster := range input.Clusters {
		entries, err := src.ReadClusterSecretKeys(ctx, cluster)
		if err != nil {
			return result, fmt.Errorf("read cluster %q keys: %w", cluster, err)
		}
		if len(entries) == 0 {
			continue
		}
		data := make(map[string][]byte, len(entries))
		for _, e := range entries {
			v, err := src.ReadClusterSecretValue(ctx, cluster, e.Key)
			if err != nil {
				return result, fmt.Errorf("read cluster %q value %q: %w", cluster, e.Key, err)
			}
			data[e.Key] = v
		}
		if err := dst.WriteClusterSecrets(ctx, cluster, data); err != nil {
			return result, fmt.Errorf("write cluster %q secrets: %w", cluster, err)
		}
		result.ClusterKeys[cluster] = len(data)
	}

	return result, nil
}
