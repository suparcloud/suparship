package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/suparcloud/suparship/internal/backup"
	"github.com/suparcloud/suparship/internal/k8s"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Back up suparShip control-plane state to a file",
	Long: `Capture all suparShip control-plane state — the ConfigMaps and Secrets
in the suparship-system namespace (org config, gitops/registry config,
project/app/cluster/preview records, env-var layers, admin credential,
1Password SA token and per-cluster Connect-token stashes + kubeconfigs) — into
a single YAML archive.

The archive contains Secrets in plaintext (base64). Treat it as sensitive:
store it encrypted and restrict access.

ArgoCD-namespace secrets (cluster registrations, repo creds) are NOT included —
suparShip re-derives them from the kubeconfig secrets + gitops config on
reconcile. SealedSecret template credentials are out of scope.`,
	Example: `  # Write a backup to a file
  suparship backup --output suparship-backup.yaml

  # Pipe to stdout (e.g. into age/sops)
  suparship backup | age -r age1... > backup.yaml.age`,
	RunE: runBackup,
}

var restoreCmd = &cobra.Command{
	Use:   "restore",
	Short: "Restore suparShip control-plane state from a backup file",
	Long: `Apply a backup archive into the cluster. Restore is additive: it creates
missing ConfigMaps/Secrets and updates existing ones in place. It never deletes
resources that aren't in the archive.

After restoring onto a fresh cluster, restart the suparShip server so it
reconciles derived state (ArgoCD AppProject, root app, per-cluster secret
stores).`,
	Example: `  suparship restore --input suparship-backup.yaml`,
	RunE:    runRestore,
}

func init() {
	backupCmd.Flags().StringP("output", "o", "-", "output file path (\"-\" for stdout)")
	backupCmd.Flags().String("namespace", backup.SystemNamespace, "namespace holding suparShip state")

	restoreCmd.Flags().StringP("input", "i", "", "backup file path (required)")
	restoreCmd.Flags().String("namespace", "", "override the target namespace (default: the archive's namespace)")
	_ = restoreCmd.MarkFlagRequired("input")

	rootCmd.AddCommand(backupCmd)
	rootCmd.AddCommand(restoreCmd)
}

func runBackup(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	kubeconfig, _ := cmd.Root().PersistentFlags().GetString("kubeconfig")
	kubecontext, _ := cmd.Root().PersistentFlags().GetString("context")
	namespace, _ := cmd.Flags().GetString("namespace")
	output, _ := cmd.Flags().GetString("output")

	client, err := k8s.NewClientset(kubeconfig, kubecontext)
	if err != nil {
		return fmt.Errorf("connecting to cluster: %w", err)
	}

	// Include the configured admin secret name in case it was renamed off the
	// default "suparship-" prefix via the admin-secret-name flag.
	adminName := adminSecretRefFromFlags(cmd).Name

	arch, err := backup.Create(ctx, client, namespace, []string{adminName}, time.Now())
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(arch)
	if err != nil {
		return fmt.Errorf("marshaling archive: %w", err)
	}

	if output == "-" || output == "" {
		_, err = cmd.OutOrStdout().Write(data)
	} else {
		err = os.WriteFile(output, data, 0o600)
	}
	if err != nil {
		return fmt.Errorf("writing archive: %w", err)
	}

	fmt.Fprintf(cmd.ErrOrStderr(),
		"Backed up %d resource(s) from namespace %q.\nWARNING: this archive contains Secrets in plaintext — store it encrypted.\n",
		len(arch.Resources), arch.Namespace)
	return nil
}

func runRestore(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	kubeconfig, _ := cmd.Root().PersistentFlags().GetString("kubeconfig")
	kubecontext, _ := cmd.Root().PersistentFlags().GetString("context")
	input, _ := cmd.Flags().GetString("input")
	nsOverride, _ := cmd.Flags().GetString("namespace")

	data, err := os.ReadFile(input)
	if err != nil {
		return fmt.Errorf("reading backup file: %w", err)
	}
	var arch backup.Archive
	if err := yaml.Unmarshal(data, &arch); err != nil {
		return fmt.Errorf("parsing backup file: %w", err)
	}

	client, err := k8s.NewClientset(kubeconfig, kubecontext)
	if err != nil {
		return fmt.Errorf("connecting to cluster: %w", err)
	}

	ns := nsOverride
	if ns == "" {
		ns = arch.Namespace
	}
	if ns == "" {
		ns = backup.SystemNamespace
	}
	if err := k8s.EnsureNamespace(ctx, client, ns); err != nil {
		return err
	}

	res, err := backup.Restore(ctx, client, &arch, nsOverride)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"Restored into %q: %d created, %d updated.\nRestart the suparShip server to reconcile derived state.\n",
		ns, len(res.Created), len(res.Updated))
	return nil
}
