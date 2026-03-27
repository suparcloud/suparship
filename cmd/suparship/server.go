package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/suparcloud/suparship/internal/server"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the suparship API server",
	Long: `Start the suparship HTTP API server.

The server exposes health-check endpoints (/healthz, /readyz) and a
version metadata endpoint (/api/v1/meta).

When --ui-dir is set, the server also serves the built frontend assets
and falls back to index.html for client-side routing.

Environment variables:
  SUPARSHIP_ADDR           listen address (default ":8080")
  SUPARSHIP_UI_DIR         path to frontend dist directory
  SUPARSHIP_CORS_ORIGINS   comma-separated allowed origins`,
	RunE: runServer,
}

func init() {
	serverCmd.Flags().String("addr", envOr("SUPARSHIP_ADDR", ":8080"), "listen address (host:port)")
	serverCmd.Flags().String("ui-dir", envOr("SUPARSHIP_UI_DIR", ""), "path to frontend static files")
	serverCmd.Flags().String("cors-origins", envOr("SUPARSHIP_CORS_ORIGINS", ""), "comma-separated allowed CORS origins")
	rootCmd.AddCommand(serverCmd)
}

func runServer(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	uiDir, _ := cmd.Flags().GetString("ui-dir")
	corsRaw, _ := cmd.Flags().GetString("cors-origins")

	var origins []string
	if corsRaw != "" {
		for _, o := range strings.Split(corsRaw, ",") {
			if trimmed := strings.TrimSpace(o); trimmed != "" {
				origins = append(origins, trimmed)
			}
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	srv := server.New(server.Config{
		Addr:        addr,
		UIDir:       uiDir,
		CORSOrigins: origins,
		Logger:      logger,
	})

	return srv.Run(cmd.Context())
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
