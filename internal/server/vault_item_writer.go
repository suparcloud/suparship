package server

import (
	"context"

	"github.com/suparcloud/suparship/internal/secrets"
)

// saVaultItemWriter implements VaultItemWriter using a secrets.VaultWriter and
// a slice of env bindings. Each binding maps an environment name to a 1Password
// vault ID, providing the target vault for per-app item operations.
type saVaultItemWriter struct {
	writer   secrets.VaultWriter
	bindings []secrets.EnvBinding // provisioned env bindings from org config
}

// NewSAVaultItemWriter creates a VaultItemWriter backed by the given writer and
// bindings. Callers should pass only provisioned bindings (Provisioned == true).
func NewSAVaultItemWriter(writer secrets.VaultWriter, bindings []secrets.EnvBinding) VaultItemWriter {
	return &saVaultItemWriter{writer: writer, bindings: bindings}
}

func (w *saVaultItemWriter) bindingFor(env string) (secrets.EnvBinding, bool) {
	for _, b := range w.bindings {
		if b.Env == env {
			return b, true
		}
	}
	return secrets.EnvBinding{}, false
}

// UpsertAppItem creates or updates the per-app skeleton vault item for the given
// env. The item is created with a single "_placeholder" field so it exists and
// is ready for operators to populate with real values. Returns nil when no
// binding exists for the env (unbound env — deferred until env is bound).
func (w *saVaultItemWriter) UpsertAppItem(ctx context.Context, org, project, app, env string) error {
	binding, ok := w.bindingFor(env)
	if !ok || binding.VaultID == "" {
		return nil // unbound env — skip, will be backfilled on bind
	}
	scope := secrets.Scope{
		Level:   secrets.LevelAppEnv,
		Org:     org,
		Env:     env,
		Project: project,
		App:     app,
	}
	// Create a skeleton item with a placeholder so the item exists in the vault.
	// Operators replace the placeholder with real keys via the 1Password UI or CLI.
	_, err := w.writer.Upsert(ctx, binding, scope, "", map[string][]byte{
		"_placeholder": []byte("replace-with-real-value"),
	})
	return err
}

// DeleteAppItem removes the per-app vault item for the given env. No-op when
// the item does not exist or no binding is found for the env.
func (w *saVaultItemWriter) DeleteAppItem(ctx context.Context, org, project, app, env string) error {
	binding, ok := w.bindingFor(env)
	if !ok || binding.VaultID == "" {
		return nil
	}
	scope := secrets.Scope{
		Level:   secrets.LevelAppEnv,
		Org:     org,
		Env:     env,
		Project: project,
		App:     app,
	}
	return w.writer.DeleteItem(ctx, binding, scope)
}
