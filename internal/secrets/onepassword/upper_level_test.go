package onepassword

import (
	"context"
	"testing"

	"github.com/suparcloud/suparship/internal/secrets"
)

// upperWriterFixture builds a fake SA client with a platform vault and one
// staging env vault, and returns a writer wired to both.
func upperWriterFixture(t *testing.T) (*FakeClient, *SAUpperLevelWriter, secrets.EnvBinding) {
	t.Helper()
	client := NewFakeClient()
	ctx := context.Background()

	platform, err := client.CreateVault(ctx, "suparship-default-platform", "")
	if err != nil {
		t.Fatalf("create platform vault: %v", err)
	}
	staging, err := client.CreateVault(ctx, "suparship-default-staging", "")
	if err != nil {
		t.Fatalf("create staging vault: %v", err)
	}

	binding := secrets.EnvBinding{
		Env:         "staging",
		VaultID:     staging.ID,
		VaultName:   staging.Title,
		Provisioned: true,
	}

	w := NewSAUpperLevelWriter(SAUpperLevelWriterConfig{
		Client:          client,
		PlatformVaultID: platform.ID,
		Bindings:        []secrets.EnvBinding{binding},
		OrgName:         "default",
		EnvForCluster: func(cluster string) string {
			if cluster == "kind-staging" {
				return "staging"
			}
			return ""
		},
	})
	return client, w, binding
}

func TestSAUpperLevelWriter_OrgRoutesToPlatformVault(t *testing.T) {
	client, w, _ := upperWriterFixture(t)
	ctx := context.Background()

	if err := w.WriteOrgSecrets(ctx, map[string][]byte{"GLOBAL": []byte("v")}); err != nil {
		t.Fatalf("WriteOrgSecrets: %v", err)
	}

	// Expect item to live in the platform vault, not the env vault.
	platformItems, _ := client.ListItems(ctx, w.platformVaultID)
	if len(platformItems) != 1 || platformItems[0].Title != "org" {
		t.Fatalf("platform vault items = %+v, want one item titled 'org'", platformItems)
	}

	keys, err := w.ReadOrgSecretKeys(ctx)
	if err != nil {
		t.Fatalf("ReadOrgSecretKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].Key != "GLOBAL" {
		t.Errorf("unexpected keys: %v", keys)
	}

	if err := w.DeleteOrgSecretKey(ctx, "GLOBAL"); err != nil {
		t.Fatalf("DeleteOrgSecretKey: %v", err)
	}
	keys, _ = w.ReadOrgSecretKeys(ctx)
	if len(keys) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(keys))
	}
}

func TestSAUpperLevelWriter_ProjectRoutesToPlatformVault(t *testing.T) {
	client, w, binding := upperWriterFixture(t)
	ctx := context.Background()

	if err := w.WriteProjectSecrets(ctx, "demo", map[string][]byte{"PROJ": []byte("v")}); err != nil {
		t.Fatalf("WriteProjectSecrets: %v", err)
	}

	platformItems, _ := client.ListItems(ctx, w.platformVaultID)
	if len(platformItems) != 1 || platformItems[0].Title != "demo" {
		t.Fatalf("platform vault items = %+v, want one item titled 'demo'", platformItems)
	}

	// Env vault must remain empty (project values are not env-scoped).
	envItems, _ := client.ListItems(ctx, binding.VaultID)
	if len(envItems) != 0 {
		t.Errorf("env vault should be empty, got %+v", envItems)
	}
}

func TestSAUpperLevelWriter_EnvTypeRoutesToEnvVault(t *testing.T) {
	client, w, binding := upperWriterFixture(t)
	ctx := context.Background()

	if err := w.WriteEnvTypeSecrets(ctx, "staging", map[string][]byte{"DB_URL": []byte("v")}); err != nil {
		t.Fatalf("WriteEnvTypeSecrets: %v", err)
	}

	envItems, _ := client.ListItems(ctx, binding.VaultID)
	if len(envItems) != 1 || envItems[0].Title != "env-staging" {
		t.Fatalf("env vault items = %+v, want one item titled 'env-staging'", envItems)
	}

	// Platform vault must not pick this up.
	platformItems, _ := client.ListItems(ctx, w.platformVaultID)
	if len(platformItems) != 0 {
		t.Errorf("platform vault should be empty for env-type writes, got %+v", platformItems)
	}
}

func TestSAUpperLevelWriter_EnvTypeMissingBindingErrors(t *testing.T) {
	_, w, _ := upperWriterFixture(t)
	ctx := context.Background()

	err := w.WriteEnvTypeSecrets(ctx, "prod", map[string][]byte{"K": []byte("v")})
	if err == nil {
		t.Fatal("expected error for unbound env, got nil")
	}
}

func TestSAUpperLevelWriter_ClusterRoutesToBoundEnvVault(t *testing.T) {
	client, w, binding := upperWriterFixture(t)
	ctx := context.Background()

	if err := w.WriteClusterSecrets(ctx, "kind-staging", map[string][]byte{"FLAG": []byte("off")}); err != nil {
		t.Fatalf("WriteClusterSecrets: %v", err)
	}

	envItems, _ := client.ListItems(ctx, binding.VaultID)
	if len(envItems) != 1 || envItems[0].Title != "cluster-kind-staging" {
		t.Fatalf("env vault items = %+v, want one item titled 'cluster-kind-staging'", envItems)
	}
}

func TestSAUpperLevelWriter_ClusterUnknownIsNoOpForReads(t *testing.T) {
	_, w, _ := upperWriterFixture(t)
	ctx := context.Background()

	keys, err := w.ReadClusterSecretKeys(ctx, "ghost-cluster")
	if err != nil {
		t.Fatalf("ReadClusterSecretKeys: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected empty keys for unknown cluster, got %v", keys)
	}
}

func TestSAUpperLevelWriter_OrgWithoutPlatformVaultErrors(t *testing.T) {
	client := NewFakeClient()
	w := NewSAUpperLevelWriter(SAUpperLevelWriterConfig{
		Client:  client,
		OrgName: "default",
		// No PlatformVaultID — emulates an org that paste-d the SA token but
		// did not (or could not) provision the platform vault.
	})

	err := w.WriteOrgSecrets(context.Background(), map[string][]byte{"K": []byte("v")})
	if err == nil {
		t.Fatal("expected error when platform vault is missing, got nil")
	}
}

func TestSAUpperLevelWriter_AppRoutesToPlatformVault(t *testing.T) {
	client, w, binding := upperWriterFixture(t)
	ctx := context.Background()

	if err := w.WriteAppSecrets(ctx, "demo", "nginx", map[string][]byte{"APP_KEY": []byte("v")}); err != nil {
		t.Fatalf("WriteAppSecrets: %v", err)
	}

	// App-level item lives in platform vault (shared across envs).
	platformItems, _ := client.ListItems(ctx, w.platformVaultID)
	if len(platformItems) != 1 || platformItems[0].Title != "demo-nginx" {
		t.Fatalf("platform vault items = %+v, want one item titled 'demo-nginx'", platformItems)
	}
	envItems, _ := client.ListItems(ctx, binding.VaultID)
	if len(envItems) != 0 {
		t.Errorf("env vault should be empty for app-level writes, got %+v", envItems)
	}
}

func TestSAUpperLevelWriter_AppEnvRoutesToEnvVault(t *testing.T) {
	client, w, binding := upperWriterFixture(t)
	ctx := context.Background()

	if err := w.WriteAppEnvSecrets(ctx, "demo", "nginx", "staging", "irrelevant-namespace", map[string][]byte{"DB": []byte("v")}); err != nil {
		t.Fatalf("WriteAppEnvSecrets: %v", err)
	}

	// App-env item lives in the env vault.
	envItems, _ := client.ListItems(ctx, binding.VaultID)
	if len(envItems) != 1 || envItems[0].Title != "demo-nginx-staging" {
		t.Fatalf("env vault items = %+v, want one item titled 'demo-nginx-staging'", envItems)
	}
	platformItems, _ := client.ListItems(ctx, w.platformVaultID)
	if len(platformItems) != 0 {
		t.Errorf("platform vault should be empty for app-env writes, got %+v", platformItems)
	}
}

func TestSAUpperLevelWriter_AppEnvUnboundEnvErrors(t *testing.T) {
	_, w, _ := upperWriterFixture(t)
	ctx := context.Background()

	err := w.WriteAppEnvSecrets(ctx, "demo", "nginx", "prod", "", map[string][]byte{"K": []byte("v")})
	if err == nil {
		t.Fatal("expected error for unbound env, got nil")
	}
}

func TestSAUpperLevelWriter_AppWithoutPlatformVaultErrors(t *testing.T) {
	client := NewFakeClient()
	w := NewSAUpperLevelWriter(SAUpperLevelWriterConfig{
		Client:  client,
		OrgName: "default",
	})

	err := w.WriteAppSecrets(context.Background(), "demo", "nginx", map[string][]byte{"K": []byte("v")})
	if err == nil {
		t.Fatal("expected error when platform vault is missing, got nil")
	}
}

func TestSAUpperLevelWriter_MergesExistingFields(t *testing.T) {
	_, w, _ := upperWriterFixture(t)
	ctx := context.Background()

	if err := w.WriteOrgSecrets(ctx, map[string][]byte{"A": []byte("a")}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := w.WriteOrgSecrets(ctx, map[string][]byte{"B": []byte("b")}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	keys, _ := w.ReadOrgSecretKeys(ctx)
	if len(keys) != 2 {
		t.Errorf("expected 2 keys after merge, got %d (%v)", len(keys), keys)
	}
}
