package main

import (
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/suparcloud/suparship/internal/server"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the suparship API server",
	Long: `Start the suparship HTTP API server.

The server exposes health-check endpoints (/healthz, /readyz) and a
version metadata endpoint (/api/v1/meta).`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		addr, err := cmd.Flags().GetString("addr")
		if err != nil {
			return err
		}

		logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
		srv := server.New(addr, logger)
		return srv.Run(cmd.Context())
	},
}

func init() {
	serverCmd.Flags().String("addr", ":8080", "listen address (host:port)")
	rootCmd.AddCommand(serverCmd)
}
