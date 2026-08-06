package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/secrets/hcvault"
	"github.com/suparcloud/suparship/internal/secrets/onepassword"
)

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage the secrets backend",
}

var secretsBackendCmd = &cobra.Command{
	Use:   "backend",
	Short: "Configure the secret storage backend",
}

var secretsBackendSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set the secret backend type",
	Long: `Switches which backend the org stores secrets in. Takes effect without a
restart.

Every backend's configuration is retained across switches, so selecting a
backend you used before reloads its settings rather than starting blank. Switching
does NOT move existing values — use ` + "`suparship secrets migrate`" + ` for that.

The Vault backend needs a server address before it can be activated; set it under
Settings → Secrets Backend (or PUT /api/v1/org/secret-backend), then switch.`,
	Example: `  suparship secrets backend set --type=onepassword
  suparship secrets backend set --type=vault
  suparship secrets backend set --type=k8s`,
	RunE: runSecretsBackendSet,
}

var secretsSATokenCmd = &cobra.Command{
	Use:   "sa-token",
	Short: "Import the 1Password Service Account token",
	Long: `Import the 1Password Service Account token used by suparship for all
administrative operations (vault creation, Connect token issuance, secret
writes). The token is stored in K8s Secret suparship-op-sa-token in
suparship-system. The plaintext is never logged.`,
	Example: `  suparship secrets sa-token --from-file=sa-token.txt`,
	RunE:    runSecretsSAToken,
}

var secretsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show secrets backend status and per-env bindings",
	RunE:  runSecretsStatus,
}

var secretsBindCmd = &cobra.Command{
	Use:   "bind",
	Short: "Bind an environment to a 1Password vault",
	Long: `Add or rotate a binding for an environment. You provide the vault ID;
suparship saves the binding in org config. Connect tokens are pasted per
cluster (one token per cluster, covering every vault it reads) via the UI or
the cluster connect-token API — not per vault.`,
	Example: `  suparship secrets bind --env=staging --vault-id=abc-123
  suparship secrets bind --env=prod --vault-id=def-456`,
	RunE: runSecretsBind,
}

var secretsUnbindCmd = &cobra.Command{
	Use:     "unbind",
	Short:   "Remove the binding for an environment",
	Long:    `Removes the binding for the specified environment. The 1Password vault is kept.`,
	Example: `  suparship secrets unbind --env=staging`,
	RunE:    runSecretsUnbind,
}

var secretsPruneLegacyItemsCmd = &cobra.Command{
	Use:   "prune-legacy-items",
	Short: "Delete legacy unqualified app-tier secret items after the rename migration",
	Long: `Deletes the legacy-named app-tier secret vault items ({app}-{scope}) that the
project-qualified rename ({project}-{app}-{scope}) migration replaced. Run this
ONLY after the new binary has booted (which copies items to the new names and
republishes every app's ExternalSecret to reference them) and you have verified
the ExternalSecrets are Ready. This is destructive; use --dry-run first to list
what would be removed.`,
	Example: `  suparship secrets prune-legacy-items --dry-run
  suparship secrets prune-legacy-items`,
	RunE: runSecretsPruneLegacyItems,
}

var secretsMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Copy all secrets from one backend into another",
	Long: `Copies every secret item the org owns — shared and app tier, across the
global, env, cluster, project, stack and preview-band scopes — from one
backend's storage into another's. Use it to move off the deprecated k8s
backend, or between 1Password and Vault.

Additive and idempotent: the source is never modified, the destination is
merged into (a key that exists on both sides takes the source's value), and a
re-run converges. Values stay inside this process; only item names and counts
are printed or logged. Per-PR preview overrides are not migrated — they are
transient CI-written values, re-created on the next push.

Typical rollout: --dry-run to review, run for real, switch the org backend
(suparship secrets backend set / Settings → Secrets Backend), verify apps
resolve, and only then clean up the source backend by hand.`,
	Example: `  suparship secrets migrate --from k8s --to vault --dry-run
  suparship secrets migrate --from k8s --to vault
  suparship secrets migrate --from onepassword --to vault --scope env`,
	RunE: runSecretsMigrate,
}

func init() {
	secretsBackendSetCmd.Flags().String("type", "",
		"backend type: "+strings.Join(secrets.BackendTypeNames(), ", "))

	secretsSATokenCmd.Flags().String("from-file", "", "path to file containing the SA token (required)")

	secretsBindCmd.Flags().String("env", "", "environment to bind (required)")
	secretsBindCmd.Flags().String("vault-id", "", "1Password vault UUID (required)")
	secretsBindCmd.Flags().String("vault-name", "", "human-readable vault name (optional)")
	secretsUnbindCmd.Flags().String("env", "", "environment to unbind (required)")

	secretsPruneLegacyItemsCmd.Flags().Bool("dry-run", false, "list the legacy items that would be deleted without deleting them")

	secretsMigrateCmd.Flags().String("from", "", "source backend: k8s, onepassword, vault (required)")
	secretsMigrateCmd.Flags().String("to", "", "destination backend: k8s, onepassword, vault (required)")
	secretsMigrateCmd.Flags().Bool("dry-run", false, "report what would be migrated without writing anything")
	secretsMigrateCmd.Flags().String("scope", "all", "restrict to one band: global (org/project/stack items), env (env/cluster/preview items), or all")

	secretsBackendCmd.AddCommand(secretsBackendSetCmd)
	secretsCmd.AddCommand(secretsBackendCmd)
	secretsCmd.AddCommand(secretsSATokenCmd)
	secretsCmd.AddCommand(secretsStatusCmd)
	secretsCmd.AddCommand(secretsBindCmd)
	secretsCmd.AddCommand(secretsUnbindCmd)
	secretsCmd.AddCommand(secretsPruneLegacyItemsCmd)
	secretsCmd.AddCommand(secretsMigrateCmd)
	rootCmd.AddCommand(secretsCmd)
}

func loadOrgStore(cmd *cobra.Command) (rbac.OrgStore, error) {
	kubeconfig, _ := cmd.Root().PersistentFlags().GetString("kubeconfig")
	kubecontext, _ := cmd.Root().PersistentFlags().GetString("context")

	client, err := k8s.NewClientset(kubeconfig, kubecontext)
	if err != nil {
		return nil, fmt.Errorf("connecting to cluster: %w", err)
	}

	return rbac.NewK8sOrgProvider(client, nil), nil
}

func runSecretsBackendSet(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	backendType, _ := cmd.Flags().GetString("type")

	if backendType == "" {
		return fmt.Errorf("--type is required")
	}

	store, err := loadOrgStore(cmd)
	if err != nil {
		return err
	}

	org, err := store.GetOrg(ctx)
	if err != nil {
		return fmt.Errorf("loading org config: %w", err)
	}

	bt := secrets.BackendType(backendType)
	if !secrets.ValidBackendTypes[bt] {
		return fmt.Errorf("unsupported backend type: %s (use %s)",
			backendType, strings.Join(secrets.BackendTypeNames(), ", "))
	}

	org.SecretBackend.Type = bt
	if bt == secrets.Backend1Password && org.SecretBackend.OnePassword == nil {
		org.SecretBackend.OnePassword = &secrets.OnePasswordConfig{
			GroupName: secrets.DefaultOnePasswordGroup,
		}
	}
	// Vault has no CLI-settable equivalent of the 1Password group default: it
	// needs a server address, which is entered via the UI/API. Say so here rather
	// than letting Validate return a bare "requires a server address" that gives
	// the operator nowhere to go. Same pointer the migrate path uses.
	if bt == secrets.BackendVault {
		if v := org.SecretBackend.Vault; v == nil || strings.TrimSpace(v.Address) == "" {
			return fmt.Errorf("vault backend has no server address configured — set it under " +
				"Settings → Secrets Backend (or PUT /api/v1/org/secret-backend), then switch")
		}
	}

	if err := org.SecretBackend.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	if err := store.SaveOrg(ctx, org); err != nil {
		return fmt.Errorf("saving org config: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Secret backend set to %s\n", org.SecretBackend.Effective())
	return nil
}

func runSecretsSAToken(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	fromFile, _ := cmd.Flags().GetString("from-file")
	if fromFile == "" {
		return fmt.Errorf("--from-file is required")
	}
	tokenBytes, err := os.ReadFile(fromFile)
	if err != nil {
		return fmt.Errorf("reading token file: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return fmt.Errorf("token file is empty")
	}

	kubeconfig, _ := cmd.Root().PersistentFlags().GetString("kubeconfig")
	kubecontext, _ := cmd.Root().PersistentFlags().GetString("context")
	client, err := k8s.NewClientset(kubeconfig, kubecontext)
	if err != nil {
		return fmt.Errorf("connecting to cluster: %w", err)
	}

	if err := k8s.UpsertSecretData(ctx, client, "suparship-system", secrets.SATokenSecretName, map[string][]byte{
		secrets.SATokenSecretKey: []byte(token),
	}); err != nil {
		return fmt.Errorf("writing SA token: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "SA token imported into K8s Secret suparship-system/%s\n",
		secrets.SATokenSecretName)
	return nil
}

func runSecretsStatus(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	store, err := loadOrgStore(cmd)
	if err != nil {
		return err
	}
	org, err := store.GetOrg(ctx)
	if err != nil {
		return fmt.Errorf("loading org config: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Backend: %s\n", org.SecretBackend.Effective())

	if org.SecretBackend.OnePassword == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "No 1Password configuration.")
		return nil
	}

	op := org.SecretBackend.OnePassword
	fmt.Fprintf(cmd.OutOrStdout(), "Group: %s\n", op.GroupName)
	fmt.Fprintf(cmd.OutOrStdout(), "Connect: installed=%v healthy=%v endpoint=%s\n",
		op.Connect.Installed, op.Connect.Healthy, op.Connect.Endpoint)

	printVault := func(scope, key string, ref secrets.VaultRef) {
		bound := "no"
		if ref.Provisioned {
			bound = "yes"
		}
		lastErr := "-"
		if ref.LastError != "" {
			lastErr = ref.LastError
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-10s  %-12s  %-40s  %-7s  %s\n", scope, key, ref.VaultID, bound, lastErr)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\n%-10s  %-12s  %-40s  %-7s  %s\n", "SCOPE", "KEY", "VAULT ID", "BOUND", "LAST ERROR")
	if op.GlobalVault.VaultID != "" {
		printVault("global", "-", op.GlobalVault)
	}
	for _, ref := range op.EnvVaults {
		printVault("env", ref.Key, ref)
	}

	// One Connect token per cluster, covering every vault the cluster reads.
	if len(op.ClusterTokens) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\n%-20s  %-7s  %s\n", "CLUSTER TOKEN", "SEALED", "LAST ERROR")
		for _, t := range op.ClusterTokens {
			sealed := "no"
			if t.Sealed {
				sealed = "yes"
			}
			lastErr := "-"
			if t.LastError != "" {
				lastErr = t.LastError
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-20s  %-7s  %s\n", t.Cluster, sealed, lastErr)
		}
	}
	return nil
}

func runSecretsBind(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	env, _ := cmd.Flags().GetString("env")
	vaultID, _ := cmd.Flags().GetString("vault-id")
	vaultName, _ := cmd.Flags().GetString("vault-name")

	if env == "" {
		return fmt.Errorf("--env is required")
	}
	if vaultID == "" {
		return fmt.Errorf("--vault-id is required")
	}

	store, err := loadOrgStore(cmd)
	if err != nil {
		return err
	}
	org, err := store.GetOrg(ctx)
	if err != nil {
		return fmt.Errorf("loading org config: %w", err)
	}

	if org.SecretBackend.Effective() != secrets.Backend1Password {
		return fmt.Errorf("bind is only available for 1Password backend (current: %s)", org.SecretBackend.Effective())
	}

	scope := secrets.EnvScope(env)
	rotated := org.SecretBackend.FindVault(scope) != nil
	if rotated {
		fmt.Fprintf(cmd.OutOrStdout(), "Environment %q already bound — updating vault...\n", env)
	}

	if vaultName == "" {
		vaultName = vaultID
	}

	org.SecretBackend.UpsertVault(scope, secrets.VaultRef{
		VaultID:         vaultID,
		VaultName:       vaultName,
		Provisioned:     true,
		LastProvisioned: time.Now(),
	})
	if err := store.SaveOrg(ctx, org); err != nil {
		return fmt.Errorf("saving org config: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Bound env %s: vault=%s\n", env, vaultName)
	fmt.Fprintln(cmd.OutOrStdout(), "Note: paste each cluster's Connect token in the UI (or via the cluster connect-token API) to seal + publish its unified store.")
	return nil
}

func runSecretsUnbind(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	env, _ := cmd.Flags().GetString("env")
	if env == "" {
		return fmt.Errorf("--env is required")
	}

	store, err := loadOrgStore(cmd)
	if err != nil {
		return err
	}
	org, err := store.GetOrg(ctx)
	if err != nil {
		return fmt.Errorf("loading org config: %w", err)
	}

	scope := secrets.EnvScope(env)
	if org.SecretBackend.FindVault(scope) == nil {
		return fmt.Errorf("no vault bound for environment %q", env)
	}

	org.SecretBackend.RemoveVault(scope)
	if err := store.SaveOrg(ctx, org); err != nil {
		return fmt.Errorf("saving org config: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Unbound environment %q. The 1Password vault is kept.\n", env)
	return nil
}

func runSecretsPruneLegacyItems(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	kubeconfig, _ := cmd.Root().PersistentFlags().GetString("kubeconfig")
	kubecontext, _ := cmd.Root().PersistentFlags().GetString("context")
	client, err := k8s.NewClientset(kubeconfig, kubecontext)
	if err != nil {
		return fmt.Errorf("connecting to cluster: %w", err)
	}
	orgProvider := rbac.NewK8sOrgProvider(client, nil)
	org, err := orgProvider.GetOrg(ctx)
	if err != nil {
		return fmt.Errorf("loading org config: %w", err)
	}

	migrator, err := buildLegacyItemMigrator(ctx, client, org)
	if err != nil {
		return err
	}

	appStore := kube.NewK8sAppStore(client)
	projectStore := project.NewK8sStore(client)
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))

	names, failures := pruneLegacyAppItems(ctx, migrator, appStore, projectStore, org, dryRun, logger)
	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Dry run — %d legacy app-tier items would be deleted:\n", len(names))
		for _, n := range names {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", n)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "\nRe-run without --dry-run to delete them.")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Pruned %d legacy app-tier items (%d failures).\n", len(names)-failures, failures)
	if failures > 0 {
		return fmt.Errorf("%d legacy items failed to delete; see logs", failures)
	}
	return nil
}

// buildBackendStore constructs the concrete vault store for a NAMED backend,
// reading its credential from the cluster. Used by the CLI paths that need a
// specific backend regardless of which one is active (migrate) or the active
// one (legacy-item prune). Mirrors the server's dynamic wiring.
func buildBackendStore(ctx context.Context, client kubernetes.Interface, org *rbac.Org, bt secrets.BackendType) (secrets.VaultStore, error) {
	readToken := func(name, key string) (string, error) {
		sec, err := client.CoreV1().Secrets(secrets.SystemNamespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("reading token secret %s: %w", name, err)
		}
		token := strings.TrimSpace(string(sec.Data[key]))
		if token == "" {
			return "", fmt.Errorf("token secret %s is empty", name)
		}
		return token, nil
	}
	switch bt {
	case secrets.Backend1Password:
		token, err := readToken(secrets.SATokenSecretName, secrets.SATokenSecretKey)
		if err != nil {
			return nil, err
		}
		saClient, err := onepassword.NewSDKClient(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("init 1Password SA client: %w", err)
		}
		resolver := func(scope secrets.Scope) (string, error) {
			return org.SecretBackend.VaultIDForScope(scope)
		}
		return onepassword.NewSAVaultStore(saClient, resolver), nil
	case secrets.BackendVault:
		vcfg := org.SecretBackend.Vault
		if vcfg == nil || vcfg.Address == "" {
			return nil, fmt.Errorf("vault backend has no server address configured (Settings → Secrets Backend)")
		}
		token, err := readToken(secrets.VaultTokenSecretName, secrets.VaultTokenSecretKey)
		if err != nil {
			return nil, err
		}
		apiClient, err := hcvault.NewAPIClient(hcvault.APIConfig{
			Address:   vcfg.Address,
			Token:     token,
			Mount:     vcfg.EffectiveMount(),
			Namespace: vcfg.Namespace,
			CACert:    vcfg.CACert,
		})
		if err != nil {
			return nil, fmt.Errorf("init vault client: %w", err)
		}
		return hcvault.NewHCVaultStore(apiClient), nil
	default:
		return secrets.NewK8sVaultStore(client), nil
	}
}

// buildLegacyItemMigrator constructs the ACTIVE backend's store asserted to
// secrets.LegacyItemMigrator.
func buildLegacyItemMigrator(ctx context.Context, client kubernetes.Interface, org *rbac.Org) (secrets.LegacyItemMigrator, error) {
	store, err := buildBackendStore(ctx, client, org, org.SecretBackend.Effective())
	if err != nil {
		return nil, err
	}
	m, ok := store.(secrets.LegacyItemMigrator)
	if !ok {
		return nil, fmt.Errorf("backend %s does not support item migration", org.SecretBackend.Effective())
	}
	return m, nil
}

func runSecretsMigrate(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	fromFlag, _ := cmd.Flags().GetString("from")
	toFlag, _ := cmd.Flags().GetString("to")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	scopeFlag, _ := cmd.Flags().GetString("scope")

	if fromFlag == "" || toFlag == "" {
		return fmt.Errorf("--from and --to are required")
	}
	if err := validateMigrateBackends(fromFlag, toFlag); err != nil {
		return err
	}
	if scopeFlag != "all" && scopeFlag != "global" && scopeFlag != "env" {
		return fmt.Errorf("--scope must be global, env, or all")
	}

	kubeconfig, _ := cmd.Root().PersistentFlags().GetString("kubeconfig")
	kubecontext, _ := cmd.Root().PersistentFlags().GetString("context")
	client, err := k8s.NewClientset(kubeconfig, kubecontext)
	if err != nil {
		return fmt.Errorf("connecting to cluster: %w", err)
	}
	org, err := rbac.NewK8sOrgProvider(client, nil).GetOrg(ctx)
	if err != nil {
		return fmt.Errorf("loading org config: %w", err)
	}

	fromStore, err := buildBackendStore(ctx, client, org, secrets.BackendType(fromFlag))
	if err != nil {
		return fmt.Errorf("source backend %s: %w", fromFlag, err)
	}
	exporter, ok := fromStore.(secrets.ItemExporter)
	if !ok {
		return fmt.Errorf("source backend %s cannot export items", fromFlag)
	}
	toStore, err := buildBackendStore(ctx, client, org, secrets.BackendType(toFlag))
	if err != nil {
		return fmt.Errorf("destination backend %s: %w", toFlag, err)
	}

	// Enumerate everything the org could own; absent items no-op.
	projectStore := project.NewK8sStore(client)
	projects, err := projectStore.List(ctx)
	if err != nil {
		return fmt.Errorf("listing projects: %w", err)
	}
	appStore := kube.NewK8sAppStore(client)
	stackStore := kube.NewK8sStackStore(client)
	appsByProject := map[string][]*domain.App{}
	stacksByProject := map[string][]*domain.Stack{}
	for _, p := range projects {
		if apps, err := appStore.ListApps(ctx, p.Metadata.Name); err == nil {
			appsByProject[p.Metadata.Name] = apps
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: listing apps in %s failed: %v\n", p.Metadata.Name, err)
		}
		if stacks, err := stackStore.ListStacks(ctx, p.Metadata.Name); err == nil {
			stacksByProject[p.Metadata.Name] = stacks
		}
	}

	targets := filterTargets(migrationTargets(org, projects, appsByProject, stacksByProject), scopeFlag)
	logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
	res := migrateItems(ctx, exporter, toStore, targets, dryRun, logger)

	out := cmd.OutOrStdout()
	if dryRun {
		fmt.Fprintf(out, "Dry run — %d items (%d keys) would migrate %s → %s", res.Migrated, res.Keys, fromFlag, toFlag)
		if res.Empty > 0 {
			fmt.Fprintf(out, ", plus %d empty items ensured", res.Empty)
		}
		fmt.Fprintln(out, ":")
		for _, l := range res.Labels {
			fmt.Fprintf(out, "  %s\n", l)
		}
		fmt.Fprintln(out, "\nRe-run without --dry-run to migrate.")
		return nil
	}
	fmt.Fprintf(out, "Migrated %d items (%d keys) %s → %s; %d empty items ensured; %d failures.\n",
		res.Migrated, res.Keys, fromFlag, toFlag, res.Empty, res.Failures)
	if res.Failures > 0 {
		return fmt.Errorf("%d items failed to migrate; see logs", res.Failures)
	}
	fmt.Fprintf(out, "\nNext: switch the org backend (`suparship secrets backend set --type=%s`\nor Settings → Secrets Backend), verify apps resolve, then clean up the\nsource backend by hand. The source was not modified.\n", toFlag)
	return nil
}
