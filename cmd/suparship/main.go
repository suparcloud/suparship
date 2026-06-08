// Package main is the entry point for the suparship CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/version"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "suparship",
	Short: "GitOps-native platform runtime for Kubernetes",
	Long: `suparship is a CLI-first tool for bootstrapping environments, services,
and preview environments using ArgoCD and Kargo on Kubernetes.

It provides template-driven manifest generation with deterministic,
auditable Git output.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Version:       version.Version,
}

func init() {
	rootCmd.SetVersionTemplate(fmt.Sprintf(
		"suparship %s (commit: %s, built: %s, config-schema: v%s, generator: %s)\n",
		version.Version, version.Commit, version.Date, version.Schema, version.Generator,
	))

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version and contract information",
		Long: `Print the binary version plus the contract versions this build understands:
  config-schema — the org-config format (compared on startup; see docs/upgrading.md)
  generator     — the GitOps manifest/label contract stamped on emitted files`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(),
				"suparship %s\n  commit:        %s\n  built:         %s\n  config-schema: v%s\n  generator:     %s\n",
				version.Version, version.Commit, version.Date, version.Schema, version.Generator)
			return nil
		},
	})

	rootCmd.PersistentFlags().String("kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	rootCmd.PersistentFlags().String("context", "", "kubernetes context to use (default: current-context)")

	rootCmd.PersistentFlags().String("admin-secret-name",
		envOr("SUPARSHIP_ADMIN_SECRET_NAME", auth.DefaultSecretName),
		"name of the Kubernetes Secret holding admin credentials")
	rootCmd.PersistentFlags().String("admin-secret-namespace",
		envOr("SUPARSHIP_ADMIN_SECRET_NAMESPACE", auth.DefaultSecretNamespace),
		"namespace of the admin credentials Secret")
	rootCmd.PersistentFlags().String("admin-secret-username-key",
		envOr("SUPARSHIP_ADMIN_SECRET_USERNAME_KEY", auth.DefaultSecretKeyUser),
		"key within the Secret that holds the admin username")
	rootCmd.PersistentFlags().String("admin-secret-password-hash-key",
		envOr("SUPARSHIP_ADMIN_SECRET_PASSWORD_HASH_KEY", auth.DefaultSecretKeyPasswordHash),
		"key within the Secret that holds the bcrypt password hash")
}

// adminSecretRefFromFlags reads the admin-secret-* persistent flags into a
// SecretRef. Empty flag values fall back to defaults via WithDefaults.
func adminSecretRefFromFlags(cmd *cobra.Command) auth.SecretRef {
	root := cmd.Root().PersistentFlags()
	name, _ := root.GetString("admin-secret-name")
	namespace, _ := root.GetString("admin-secret-namespace")
	userKey, _ := root.GetString("admin-secret-username-key")
	hashKey, _ := root.GetString("admin-secret-password-hash-key")
	return auth.SecretRef{
		Namespace:       namespace,
		Name:            name,
		UsernameKey:     userKey,
		PasswordHashKey: hashKey,
	}.WithDefaults()
}
