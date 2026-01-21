// Package main is the entry point for the suparship CLI.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

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
		"suparship %s (commit: %s, built: %s)\n",
		version.Version, version.Commit, version.Date,
	))
}
