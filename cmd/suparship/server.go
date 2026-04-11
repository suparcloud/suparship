package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/config"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/fake"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/kube"
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

Runtime modes:
  fake       — in-memory seed data, no Kubernetes required (default for contributors)
  kubernetes — connects to a real cluster via kubeconfig

Environment variables:
  SUPARSHIP_DEV_MODE       set to "local" to enable fake mode (recommended for contributors)
  SUPARSHIP_CLUSTER_MODE   set to "fake" to enable fake mode (alternative override)
  SUPARSHIP_ADDR           listen address (default ":8080")
  SUPARSHIP_UI_DIR         path to frontend dist directory
  SUPARSHIP_CORS_ORIGINS   comma-separated allowed origins
  SUPARSHIP_TEMPLATES_DIR  path to templates directory
  SUPARSHIP_COOKIE_SECURE  set to "true" for HTTPS deployments
  SUPARSHIP_LOG_LEVEL      log verbosity: debug, info, warn, error (default "info")`,
	RunE: runServer,
}

func init() {
	serverCmd.Flags().String("addr", envOr("SUPARSHIP_ADDR", ":8080"), "listen address (host:port)")
	serverCmd.Flags().String("ui-dir", envOr("SUPARSHIP_UI_DIR", ""), "path to frontend static files")
	serverCmd.Flags().String("cors-origins", envOr("SUPARSHIP_CORS_ORIGINS", ""), "comma-separated allowed CORS origins")
	serverCmd.Flags().String("templates-dir", envOr("SUPARSHIP_TEMPLATES_DIR", ""), "path to templates directory")
	serverCmd.Flags().Bool("cookie-secure", envOr("SUPARSHIP_COOKIE_SECURE", "false") == "true", "set Secure flag on session cookies (enable behind HTTPS)")
	serverCmd.Flags().String("log-level", envOr("SUPARSHIP_LOG_LEVEL", "info"), "log verbosity: debug, info, warn, error")
	rootCmd.AddCommand(serverCmd)
}

func runServer(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	uiDir, _ := cmd.Flags().GetString("ui-dir")
	corsRaw, _ := cmd.Flags().GetString("cors-origins")
	templatesDir, _ := cmd.Flags().GetString("templates-dir")
	cookieSecure, _ := cmd.Flags().GetBool("cookie-secure")
	logLevelStr, _ := cmd.Flags().GetString("log-level")
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

	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(logLevelStr)); err != nil {
		logLevel = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))
	// Set as the process-wide default so that slog package-level calls (e.g.
	// slog.Error, slog.Debug) anywhere in the codebase use the same handler
	// and level as the explicitly-constructed logger.
	slog.SetDefault(logger)
	logger.Debug("debug logging enabled")

	cfg := config.Load()

	var (
		authenticator    auth.Authenticator
		orgProvider      rbac.OrgStore
		projectStore     project.Store
		previewStore     preview.Store
		runtimeProvider  runtime.Provider
		logsProvider     runtime.LogsProvider
		appStore         domain.AppStore
		clusterStore     domain.ClusterStore
		templates        []*tpl.Template
		readinessProbers []server.ReadinessProber
		kargoPromoter    server.KargoPromoter
	)

	switch cfg.RuntimeMode {
	case config.ModeFake:
		// Fake mode: entirely in-memory with deterministic seed data.
		// No Kubernetes cluster is required.  This is the default mode for
		// local UI/API development.  Set SUPARSHIP_DEV_MODE=local (or
		// SUPARSHIP_CLUSTER_MODE=fake) in your .env to activate.
		//
		// WARNING: fake mode uses plain-text credentials read from
		// SUPARSHIP_ADMIN_EMAIL / SUPARSHIP_ADMIN_PASSWORD (defaults from
		// .env.example: admin@local / admin123).  Never run in production.
		deps := fake.NewDevServerDeps()
		logger.Info("runtime mode: fake — in-memory seed data, no cluster required",
			"trigger", cfg.RuntimeModeTrigger,
			"login", deps.AdminUsername,
			"password_env", "SUPARSHIP_ADMIN_PASSWORD",
		)
		authenticator = deps.Authenticator
		orgProvider = deps.OrgProvider
		projectStore = deps.ProjectStore
		previewStore = deps.PreviewStore
		runtimeProvider = deps.RuntimeProvider
		logsProvider = deps.LogsProvider
		appStore = deps.AppStore
		clusterStore = deps.ClusterStore

	default: // config.ModeKubernetes
		// Log what we will attempt before trying, so contributors see the
		// intent even if the connection fails.
		kubeconfigDesc := "auto (KUBECONFIG env → ~/.kube/config → in-cluster)"
		if kubeconfig != "" {
			kubeconfigDesc = kubeconfig
		}
		contextDesc := "current context"
		if kubecontext != "" {
			contextDesc = kubecontext
		}
		logger.Info("runtime mode: kubernetes",
			"kubeconfig", kubeconfigDesc,
			"context", contextDesc,
		)

		client, err := k8s.NewClientset(kubeconfig, kubecontext)
		if err != nil {
			return fmt.Errorf(
				"runtime mode is %q but no Kubernetes cluster is reachable: %w\n\n"+
					"To fix:\n"+
					"  • Local development (no cluster needed): set SUPARSHIP_DEV_MODE=local in .env\n"+
					"  • Cluster access: ensure KUBECONFIG points to a valid kubeconfig file\n"+
					"  • Diagnose connectivity: kubectl cluster-info",
				config.ModeKubernetes, err,
			)
		}
		logger.Info("kubernetes client ready")

		// Build dynamic client for CRD interactions (ArgoCD, Kargo).
		dynClient, dynErr := k8s.NewDynamicClient(kubeconfig, kubecontext)
		if dynErr != nil {
			logger.Warn("dynamic client unavailable — ArgoCD/Kargo features disabled", "error", dynErr)
		} else {
			// Register ArgoCD readiness probe.
			readinessProbers = append(readinessProbers, server.ReadinessProber{
				Name:  "argocd",
				Check: kube.NewArgoCDReadinessProbe(dynClient, ""),
			})
			// Wire Kargo promoter.
			kargoStore := kube.NewKargoStore(dynClient)
			kargoPromoter = &kargoPromoterAdapter{store: kargoStore}
			logger.Info("kargo promoter enabled via dynamic client")
		}

		authenticator = auth.NewK8sAuthenticator(client)

		adminUser := "admin"
		creds, err := auth.GetAdminSecret(context.Background(), client)
		if err == nil && creds != nil {
			adminUser = creds.Username
		}

		fallbackOrg := rbac.NewDefaultOrg("default", "Default Organization", adminUser)
		orgProvider = rbac.NewK8sOrgProvider(client, fallbackOrg)

		// Wire all Kubernetes-backed store and runtime implementations
		// through the consolidated kube.ServerDeps bundle.
		kubeDeps := kube.NewServerDeps(client, rbac.NewOrgEnvNamesAdapter(orgProvider))
		projectStore = kubeDeps.ProjectStore
		previewStore = kubeDeps.PreviewStore
		runtimeProvider = kubeDeps.RuntimeProvider
		logsProvider = kubeDeps.LogsProvider
		appStore = kubeDeps.AppStore
		clusterStore = kubeDeps.ClusterStore

		// When no local templates directory is provided, attempt to load
		// templates stored as ConfigMaps in the cluster (label
		// suparship.io/type=template, namespace suparship-system).
		// Failure is non-fatal: the server starts without templates so
		// existing services remain accessible and operators can diagnose
		// the issue without a hard stop.
		if templatesDir == "" {
			clusterTemplates, err := kube.LoadTemplates(cmd.Context(), client)
			if err != nil {
				logger.Warn("could not load templates from cluster, starting without",
					"error", err,
				)
			} else {
				templates = clusterTemplates
				logger.Info("templates loaded from cluster", "count", len(templates))
			}
		}
	}

	// Disk-based templates (--templates-dir / SUPARSHIP_TEMPLATES_DIR) always
	// take precedence over cluster-loaded templates, so contributors can
	// iterate on templates locally without pushing ConfigMaps to the cluster.
	if templatesDir != "" {
		loaded, err := tpl.LoadDir(templatesDir)
		if err != nil {
			return fmt.Errorf("loading templates from %s: %w", templatesDir, err)
		}
		templates = loaded
		logger.Info("templates loaded", "dir", templatesDir, "count", len(templates))
	}

	// Wire the GitOps publisher when the gitops repo URL is configured.
	// In fake/local dev mode this is optional; in cluster mode it is expected.
	var gitOpsPublisher server.GitOpsPublisher
	if cfg.GitOps.RepoURL != "" {
		pub, err := gitops.NewPublisher(gitops.PublisherConfig{
			RepoURL:       cfg.GitOps.RepoURL,
			RepoUser:      cfg.GitOps.RepoUser,
			RepoPassword:  cfg.GitOps.RepoPassword,
			ArgoCDRepoURL: cfg.GitOps.ArgoCDRepoURL,
			SyncAutomated: true,
		})
		if err != nil {
			logger.Warn("gitops publisher disabled", "reason", err.Error())
		} else {
			gitOpsPublisher = &gitOpsPublisherAdapter{
				inner:        pub,
				orgProvider:  orgProvider,
				clusterStore: clusterStore,
			}
			logger.Info("gitops publisher enabled",
				"repo", cfg.GitOps.RepoURL,
				"argocd_repo", cfg.GitOps.ArgoCDRepoURL,
			)
			// Register GitOps repo connectivity probe.
			// Use ArgoCDRepoURL when set (internal cluster URL); fall back to
			// the host-accessible RepoURL for the probe.
			gitopsProbeURL := cfg.GitOps.ArgoCDRepoURL
			if gitopsProbeURL == "" {
				gitopsProbeURL = cfg.GitOps.RepoURL
			}
			readinessProbers = append(readinessProbers, server.ReadinessProber{
				Name:  "gitops-repo",
				Check: kube.NewGitOpsRepoReadinessProbe(gitopsProbeURL),
			})
		}
	} else {
		logger.Info("gitops publisher disabled — set SUPARSHIP_GITOPS_REPO_URL to enable")
	}

	srv := server.New(server.Config{
		Addr:             addr,
		UIDir:            uiDir,
		CORSOrigins:      origins,
		Authenticator:    authenticator,
		OrgProvider:      orgProvider,
		Templates:        templates,
		ProjectStore:     projectStore,
		RuntimeProvider:  runtimeProvider,
		LogsProvider:     logsProvider,
		PreviewStore:     previewStore,
		AppStore:         appStore,
		ClusterStore:     clusterStore,
		GitOpsPublisher:  gitOpsPublisher,
		KargoPromoter:    kargoPromoter,
		ReadinessProbers: readinessProbers,
		CookieSecure:     cookieSecure,
		Logger:           logger,
	})

	if err := srv.Run(cmd.Context()); err != nil {
		logger.Error("server exited with error", "error", err)
		return err
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// gitOpsPublisherAdapter bridges the server.GitOpsPublisher interface
// (which uses []*domain.AppEnvironment) to the gitops.Publisher (which uses
// []gitops.AppPublishEnv and []gitops.AppSetEnv).
//
// On every PublishApp call the adapter:
//  1. Looks up org-level environment definitions to resolve ClusterRef,
//     BaseDomain and NamespacePattern for each environment.
//  2. Looks up the registered Cluster to resolve ClusterServer (the K8s API
//     server URL ArgoCD needs for ApplicationSet destinations).
//  3. Calls PublishEnvInfra to write appset.yaml + appproject.yaml for each
//     environment — ArgoCD discovers apps through these files.
//  4. Calls PublishApp to write app.yaml + values.yaml for each environment.
type gitOpsPublisherAdapter struct {
	inner        *gitops.Publisher
	orgProvider  rbac.OrgProvider
	clusterStore domain.ClusterStore
}

// envResolved holds the resolved cluster and domain info for one environment.
type envResolved struct {
	clusterServer    string
	baseDomain       string
	namespacePattern string
}

// resolveEnvs builds a map of envName → resolved cluster info from org config
// and the cluster store. Falls back to safe defaults when data is missing.
func (a *gitOpsPublisherAdapter) resolveEnvs(ctx context.Context) map[string]envResolved {
	result := make(map[string]envResolved)

	if a.orgProvider == nil {
		return result
	}

	org, err := a.orgProvider.GetOrg(ctx)
	if err != nil || org == nil {
		return result
	}

	for _, orgEnv := range org.Environments {
		res := envResolved{
			clusterServer:    "https://kubernetes.default.svc", // safe default
			baseDomain:       orgEnv.BaseDomain,
			namespacePattern: orgEnv.NamespacePattern,
		}
		if res.baseDomain == "" {
			res.baseDomain = "localhost"
		}

		// Resolve the cluster API server from the cluster store.
		if orgEnv.ClusterRef != "" && a.clusterStore != nil {
			if cluster, err := a.clusterStore.GetCluster(ctx, orgEnv.ClusterRef); err == nil && cluster.APIServer != "" {
				res.clusterServer = cluster.APIServer
			}
		}

		result[orgEnv.Name] = res
	}

	return result
}

// kargoPromoterAdapter bridges kube.KargoStore (which returns *kube.KargoPromotionInfo)
// to the server.KargoPromoter interface (which returns server.KargoPromotionResult).
type kargoPromoterAdapter struct {
	store *kube.KargoStore
}

func (a *kargoPromoterAdapter) CreatePromotion(ctx context.Context, projectNS, appName, fromStage, toStage string) (server.KargoPromotionResult, error) {
	info, err := a.store.CreatePromotion(ctx, projectNS, appName, fromStage, toStage)
	if err != nil {
		return server.KargoPromotionResult{}, err
	}
	return server.KargoPromotionResult{
		Name:    info.Name,
		Stage:   info.Stage,
		Freight: info.Freight,
		Phase:   info.Phase,
	}, nil
}

func (a *gitOpsPublisherAdapter) PublishApp(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) error {
	resolved := a.resolveEnvs(ctx)

	appSetEnvs := make([]gitops.AppSetEnv, 0, len(envs))
	pubEnvs := make([]gitops.AppPublishEnv, 0, len(envs))

	for _, env := range envs {
		res, ok := resolved[env.EnvName]
		if !ok {
			// Environment not in org config — use safe defaults.
			res = envResolved{
				clusterServer: "https://kubernetes.default.svc",
				baseDomain:    "localhost",
			}
		}

		appSetEnvs = append(appSetEnvs, gitops.AppSetEnv{
			EnvName:          env.EnvName,
			ClusterServer:    res.clusterServer,
			NamespacePattern: res.namespacePattern,
			BaseDomain:       res.baseDomain,
		})
		pubEnvs = append(pubEnvs, gitops.AppPublishEnv{
			EnvName:    env.EnvName,
			EnvType:    env.EnvType,
			BaseDomain: res.baseDomain,
		})
	}

	// Write appset.yaml + appproject.yaml for each environment so ArgoCD can
	// discover apps through its Git File generator. This is idempotent.
	if err := a.inner.PublishEnvInfra(ctx, app.ProjectName, appSetEnvs); err != nil {
		return fmt.Errorf("publish env infra: %w", err)
	}

	// Write app.yaml + values.yaml for each environment.
	return a.inner.PublishApp(ctx, app, pubEnvs)
}
