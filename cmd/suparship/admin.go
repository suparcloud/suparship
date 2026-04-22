package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/kube"
)

var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Manage admin credentials",
}

var adminBootstrapCmd = &cobra.Command{
	Use:   "bootstrap",
	Short: "Create the initial admin user",
	Long: `Create the initial admin user for suparship.

Generates a random password, hashes it with bcrypt, and stores the
credentials in a Kubernetes Secret in the suparship-system namespace.

The plaintext password is printed exactly once — save it immediately.`,
	Example: `  # Bootstrap with default username "admin"
  suparship admin bootstrap

  # Use a custom username
  suparship admin bootstrap --username ops-admin

  # Overwrite existing credentials
  suparship admin bootstrap --force`,
	RunE: runAdminBootstrap,
}

var adminResetPasswordCmd = &cobra.Command{
	Use:   "reset-password",
	Short: "Reset the admin password",
	Long: `Generate a new random password for the existing admin user.

The username is preserved. The new plaintext password is printed exactly
once — save it immediately.`,
	Example: `  suparship admin reset-password`,
	RunE:    runAdminResetPassword,
}

func init() {
	adminBootstrapCmd.Flags().String("username", "admin", "admin username")
	adminBootstrapCmd.Flags().Bool("force", false, "overwrite existing credentials")

	adminCmd.AddCommand(adminBootstrapCmd)
	adminCmd.AddCommand(adminResetPasswordCmd)
	rootCmd.AddCommand(adminCmd)
}

func runAdminBootstrap(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	username, _ := cmd.Flags().GetString("username")
	force, _ := cmd.Flags().GetBool("force")
	kubeconfig, _ := cmd.Root().PersistentFlags().GetString("kubeconfig")
	kubecontext, _ := cmd.Root().PersistentFlags().GetString("context")

	client, err := k8s.NewClientset(kubeconfig, kubecontext)
	if err != nil {
		return fmt.Errorf("connecting to cluster: %w", err)
	}

	// Ensure namespace exists.
	if err := k8s.EnsureNamespace(ctx, client, auth.SecretNamespace); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Namespace %s ready\n", auth.SecretNamespace)

	// Check for existing secret.
	existing, err := auth.GetAdminSecret(ctx, client)
	if err != nil {
		return err
	}
	if existing != nil && !force {
		fmt.Fprintln(cmd.ErrOrStderr(), "Error: admin credentials already exist")
		fmt.Fprintln(cmd.ErrOrStderr())
		fmt.Fprintln(cmd.ErrOrStderr(), "  Use --force to overwrite existing credentials.")
		fmt.Fprintln(cmd.ErrOrStderr(), "  Use 'suparship admin reset-password' to change the password only.")
		return fmt.Errorf("admin credentials already exist (use --force to overwrite)")
	}

	// Generate credentials.
	creds, plaintext, err := auth.NewBootstrapCredentials(username)
	if err != nil {
		return err
	}

	// Create or update the secret.
	if existing != nil {
		if err := auth.UpdateAdminSecret(ctx, client, creds); err != nil {
			return err
		}
	} else {
		if err := auth.CreateAdminSecret(ctx, client, creds); err != nil {
			return err
		}
	}

	printCredentials(cmd, "Admin credentials created", creds.Username, plaintext)
	fmt.Fprintln(cmd.OutOrStdout())

	// Ensure the suparship-system ArgoCD AppProject exists so the root app
	// can sync immediately after bootstrap. Non-fatal: if ArgoCD is not
	// installed the warning is printed but bootstrap still succeeds.
	dynClient, dynErr := k8s.NewDynamicClient(kubeconfig, kubecontext)
	if dynErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "  Warning: could not build dynamic client — skipping ArgoCD AppProject check (%v)\n", dynErr)
	} else {
		if err := kube.EnsureArgoCDSystemProject(cmd.Context(), dynClient, "argocd"); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  Warning: could not ensure suparship-system AppProject: %v\n", err)
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "  ArgoCD suparship-system project ready")
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "  Next steps:")
	fmt.Fprintln(cmd.OutOrStdout(), "    1. Save the password above — it will not be shown again")
	fmt.Fprintln(cmd.OutOrStdout(), "    2. Start the server: suparship server")

	return nil
}

func runAdminResetPassword(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	kubeconfig, _ := cmd.Root().PersistentFlags().GetString("kubeconfig")
	kubecontext, _ := cmd.Root().PersistentFlags().GetString("context")

	client, err := k8s.NewClientset(kubeconfig, kubecontext)
	if err != nil {
		return fmt.Errorf("connecting to cluster: %w", err)
	}

	existing, err := auth.GetAdminSecret(ctx, client)
	if err != nil {
		return err
	}
	if existing == nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "Error: no admin credentials found")
		fmt.Fprintln(cmd.ErrOrStderr())
		fmt.Fprintln(cmd.ErrOrStderr(), "  Run 'suparship admin bootstrap' first.")
		return fmt.Errorf("no admin credentials found (run 'suparship admin bootstrap' first)")
	}

	plaintext, err := auth.GeneratePassword()
	if err != nil {
		return fmt.Errorf("generating new password: %w", err)
	}

	hash, err := auth.HashPassword(plaintext)
	if err != nil {
		return fmt.Errorf("hashing new password: %w", err)
	}

	creds := &auth.Credentials{
		Username:     existing.Username,
		PasswordHash: hash,
	}

	if err := auth.UpdateAdminSecret(ctx, client, creds); err != nil {
		return err
	}

	printCredentials(cmd, fmt.Sprintf("Password reset for user %q", creds.Username), creds.Username, plaintext)
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "  The old password is no longer valid.")

	return nil
}

func printCredentials(cmd *cobra.Command, header, username, password string) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n  %s\n", header)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Credentials (save these — the password will not be shown again):")
	fmt.Fprintf(out, "    Username: %s\n", username)
	fmt.Fprintf(out, "    Password: %s\n", password)
}
