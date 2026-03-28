package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/server"
	"github.com/suparcloud/suparship/internal/tpl"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the suparship API server",
	Long: `Start the suparship HTTP API server.

The server exposes health-check endpoints (/healthz, /readyz) and a
version metadata endpoint (/api/v1/meta).

When a Kubernetes cluster is reachable, auth endpoints are enabled
automatically (/api/v1/auth/*).

Environment variables:
  SUPARSHIP_ADDR           listen address (default ":8080")
  SUPARSHIP_UI_DIR         path to frontend dist directory
  SUPARSHIP_CORS_ORIGINS   comma-separated allowed origins
  SUPARSHIP_TEMPLATES_DIR  path to templates directory
  SUPARSHIP_COOKIE_SECURE  set to "true" for HTTPS deployments`,
	RunE: runServer,
}

func init() {
	serverCmd.Flags().String("addr", envOr("SUPARSHIP_ADDR", ":8080"), "listen address (host:port)")
	serverCmd.Flags().String("ui-dir", envOr("SUPARSHIP_UI_DIR", ""), "path to frontend static files")
	serverCmd.Flags().String("cors-origins", envOr("SUPARSHIP_CORS_ORIGINS", ""), "comma-separated allowed CORS origins")
	serverCmd.Flags().String("templates-dir", envOr("SUPARSHIP_TEMPLATES_DIR", ""), "path to templates directory")
	serverCmd.Flags().Bool("cookie-secure", envOr("SUPARSHIP_COOKIE_SECURE", "false") == "true", "set Secure flag on session cookies (enable behind HTTPS)")
	rootCmd.AddCommand(serverCmd)
}

func runServer(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	uiDir, _ := cmd.Flags().GetString("ui-dir")
	corsRaw, _ := cmd.Flags().GetString("cors-origins")
	templatesDir, _ := cmd.Flags().GetString("templates-dir")
	cookieSecure, _ := cmd.Flags().GetBool("cookie-secure")
	kubeconfig, _ := cmd.Root().PersistentFlags().GetString("kubeconfig")
	kubecontext, _ := cmd.Root().PersistentFlags().GetString("context")

	var origins []string
	if corsRaw != "" {
		for _, o := range strings.Split(corsRaw, ",") {
			if trimmed := strings.TrimSpace(o); trimmed != "" {
				origins = append(origins, trimmed)
			}
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	var authenticator auth.Authenticator
	var orgProvider rbac.OrgProvider
	client, err := k8s.NewClientset(kubeconfig, kubecontext)
	if err != nil {
		logger.Warn("kubernetes not available, auth and RBAC endpoints disabled", "error", err)
	} else {
		authenticator = auth.NewK8sAuthenticator(client)

		adminUser := "admin"
		creds, err := auth.GetAdminSecret(context.Background(), client)
		if err == nil && creds != nil {
			adminUser = creds.Username
		}

		fallbackOrg := rbac.NewDefaultOrg("default", "Default Organization", adminUser)
		orgProvider = rbac.NewK8sOrgProvider(client, fallbackOrg)
	}

	var templates []*tpl.Template
	if templatesDir != "" {
		loaded, err := tpl.LoadDir(templatesDir)
		if err != nil {
			return fmt.Errorf("loading templates from %s: %w", templatesDir, err)
		}
		templates = loaded
		logger.Info("templates loaded", "dir", templatesDir, "count", len(templates))
	}

	var projectStore project.Store
	var previewStore preview.Store
	var runtimeProvider runtime.Provider
	var logsProvider runtime.LogsProvider
	if client != nil {
		projectStore = project.NewK8sStore(client)
		previewStore = preview.NewK8sStore(client)
		runtimeProvider = runtime.NewK8sProvider(client)
		logsProvider = runtime.NewK8sLogsProvider(client)
	}

	srv := server.New(server.Config{
		Addr:            addr,
		UIDir:           uiDir,
		CORSOrigins:     origins,
		Authenticator:   authenticator,
		OrgProvider:     orgProvider,
		Templates:       templates,
		ProjectStore:    projectStore,
		RuntimeProvider: runtimeProvider,
		LogsProvider:    logsProvider,
		PreviewStore:    previewStore,
		CookieSecure:    cookieSecure,
		Logger:          logger,
	})

	return srv.Run(cmd.Context())
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
