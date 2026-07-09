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

	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/secrets"
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
	Example: `  suparship secrets backend set --type=onepassword
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

func init() {
	secretsBackendSetCmd.Flags().String("type", "", "backend type: k8s, onepassword")

	secretsSATokenCmd.Flags().String("from-file", "", "path to file containing the SA token (required)")

	secretsBindCmd.Flags().String("env", "", "environment to bind (required)")
	secretsBindCmd.Flags().String("vault-id", "", "1Password vault UUID (required)")
	secretsBindCmd.Flags().String("vault-name", "", "human-readable vault name (optional)")
	secretsUnbindCmd.Flags().String("env", "", "environment to unbind (required)")

	secretsPruneLegacyItemsCmd.Flags().Bool("dry-run", false, "list the legacy items that would be deleted without deleting them")

	secretsBackendCmd.AddCommand(secretsBackendSetCmd)
	secretsCmd.AddCommand(secretsBackendCmd)
	secretsCmd.AddCommand(secretsSATokenCmd)
	secretsCmd.AddCommand(secretsStatusCmd)
	secretsCmd.AddCommand(secretsBindCmd)
	secretsCmd.AddCommand(secretsUnbindCmd)
	secretsCmd.AddCommand(secretsPruneLegacyItemsCmd)
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
		return fmt.Errorf("unsupported backend type: %s (use k8s or onepassword)", backendType)
	}

	org.SecretBackend.Type = bt
	if bt == secrets.Backend1Password && org.SecretBackend.OnePassword == nil {
		org.SecretBackend.OnePassword = &secrets.OnePasswordConfig{
			GroupName: secrets.DefaultOnePasswordGroup,
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

// buildLegacyItemMigrator constructs the concrete vault store for the org's
// backend, asserted to secrets.LegacyItemMigrator. Mirrors the server's vault
// wiring: k8s Secrets by default, or the 1Password SA store when that backend is
// active and its SA token is present.
func buildLegacyItemMigrator(ctx context.Context, client kubernetes.Interface, org *rbac.Org) (secrets.LegacyItemMigrator, error) {
	if org.SecretBackend.Effective() == secrets.Backend1Password {
		sec, err := client.CoreV1().Secrets(secrets.SystemNamespace).Get(ctx, secrets.SATokenSecretName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("reading SA token %s: %w", secrets.SATokenSecretName, err)
		}
		token := strings.TrimSpace(string(sec.Data[secrets.SATokenSecretKey]))
		if token == "" {
			return nil, fmt.Errorf("SA token secret %s is empty", secrets.SATokenSecretName)
		}
		saClient, err := onepassword.NewSDKClient(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("init 1Password SA client: %w", err)
		}
		resolver := func(scope secrets.Scope) (string, error) {
			return org.SecretBackend.VaultIDForScope(scope)
		}
		return onepassword.NewSAVaultStore(saClient, resolver), nil
	}
	return secrets.NewK8sVaultStore(client), nil
}
