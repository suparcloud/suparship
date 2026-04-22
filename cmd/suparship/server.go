package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/bootstrap"
	"github.com/suparcloud/suparship/internal/config"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/fake"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/registry"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/secrets"
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

Configuration model:
  GitOps, registry, and org configuration is read from Kubernetes ConfigMaps
  (the single source of truth). These ConfigMaps are created by the Helm chart
  on first install or seeded from environment variables on first boot.
  After initial creation, use the settings UI or API to modify configuration.

Environment variables (process-level, always active):
  SUPARSHIP_DEV_MODE       set to "local" to enable fake mode (recommended for contributors)
  SUPARSHIP_CLUSTER_MODE   set to "fake" to enable fake mode (alternative override)
  SUPARSHIP_ADDR           listen address (default ":8080")
  SUPARSHIP_UI_DIR         path to frontend dist directory
  SUPARSHIP_CORS_ORIGINS   comma-separated allowed origins
  SUPARSHIP_TEMPLATES_DIR  path to templates directory
  SUPARSHIP_COOKIE_SECURE  set to "true" for HTTPS deployments
  SUPARSHIP_LOG_LEVEL      log verbosity: debug, info, warn, error (default "info")

Environment variables (bootstrap-only, used to seed ConfigMaps on first boot):
  SUPARSHIP_GITOPS_REPO_URL       seed gitops ConfigMap with this repo URL
  SUPARSHIP_GITOPS_REPO_USER      seed gitops credential Secret with this username
  SUPARSHIP_GITOPS_REPO_PASSWORD  seed gitops credential Secret with this password/token
  SUPARSHIP_ARGOCD_REPO_URL       seed gitops ConfigMap with ArgoCD-specific repo URL
  SUPARSHIP_KARGO_GIT_REPO_URL    seed gitops ConfigMap with Kargo-specific repo URL
  SUPARSHIP_INSECURE_REGISTRY     seed registry ConfigMap with insecure flag ("true")`,
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
		authenticator           auth.Authenticator
		orgProvider             rbac.OrgStore
		projectStore            project.Store
		previewStore            preview.Store
		runtimeProvider         runtime.Provider
		logsProvider            runtime.LogsProvider
		appStore                domain.AppStore
		clusterStore            domain.ClusterStore
		secretBackend           secrets.Backend
		upperLevelSecretWriter  secrets.UpperLevelWriter
		templates               []*tpl.Template
		readinessProbers        []server.ReadinessProber
		kargoPromoter           server.KargoPromoter
		kargoStatusReader       server.KargoStatusReader
		kargoPipelineReader     server.KargoPipelineReader
		deploymentHistoryReader server.DeploymentHistoryReader
		kubeClient              kubernetes.Interface
		dynClient               dynamic.Interface
		gitopsConfigStore       *gitops.ConfigStore
		templateRegistryStore   *tpl.RegistryStore
		registryStore           *registry.Store
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
		memBE := secrets.NewMemBackend()
		secretBackend = memBE
		upperLevelSecretWriter = secrets.NewMemUpperLevelWriter(memBE)
		deploymentHistoryReader = &fakeHistoryAdapter{inner: &fake.FakeDeploymentHistoryReader{}}

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
		kubeClient = client

		// Build dynamic client for CRD interactions (ArgoCD, Kargo).
		var dynErr error
		dynClient, dynErr = k8s.NewDynamicClient(kubeconfig, kubecontext)
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
			kargoAdapter := &kargoPromoterAdapter{store: kargoStore}
			kargoPromoter = kargoAdapter
			kargoStatusReader = kargoAdapter
			kargoPipelineReader = kargoAdapter
			// Wire ArgoCD deployment history reader.
			argoCDReader := kube.NewArgoCDStatusReaderFromDynamic(dynClient, "")
			deploymentHistoryReader = &argoCDHistoryAdapter{reader: argoCDReader}
			logger.Info("kargo promoter enabled via dynamic client")
			logger.Info("argocd deployment history reader enabled")
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
		secretBackend = secrets.NewK8sBackend(client)
		upperLevelSecretWriter = secrets.NewUpperLevelSecretWriter(client)
		gitopsConfigStore = gitops.NewConfigStore(client)
		templateRegistryStore = tpl.NewRegistryStore(client)
		registryStore = registry.NewStore(client)

		// Bootstrap: reconcile Helm-provided ConfigMaps and log what was found.
		bootstrapResult := bootstrap.Reconcile(cmd.Context(), client, logger)
		logger.Info("bootstrap complete", "summary", bootstrap.FormatSummary(bootstrapResult))

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

	// Wire the GitOps publisher from the ConfigMap (the single source of truth).
	// In fake/local dev mode the ConfigStore is nil and the publisher is disabled.
	var gitOpsPublisher server.GitOpsPublisher
	sealPublisherHolder := server.NewSealPublisherHolder(nil)
	if gitopsConfigStore != nil {
		repoCfg, cfgErr := gitopsConfigStore.Get(cmd.Context())
		if cfgErr == nil && repoCfg.RepoURL != "" {
			username, password, _ := gitopsConfigStore.GetCredentials(cmd.Context(), repoCfg)
			pubCfg := repoCfg.ToPublisherConfig()
			pubCfg.RepoUser = username
			pubCfg.RepoPassword = password
			pubCfg.SyncAutomated = true
			pubCfg.TemplatesDir = templatesDir

			// InsecureRegistry is read from the registry ConfigMap (if configured).
			if registryStore != nil {
				if regCfg, regErr := registryStore.Get(cmd.Context()); regErr == nil {
					pubCfg.InsecureRegistry = regCfg.Insecure
				}
			}

			pub, err := gitops.NewPublisher(pubCfg)
			if err != nil {
				logger.Warn("gitops publisher disabled", "reason", err.Error())
			} else {
				gitOpsPublisher = &gitOpsPublisherAdapter{
					inner:        pub,
					orgProvider:  orgProvider,
					clusterStore: clusterStore,
				}
				sealPublisherHolder.Swap(pub)
				logger.Info("gitops publisher enabled",
					"repo", repoCfg.RepoURL,
					"argocd_repo", pubCfg.ArgoCDRepoURL,
				)
				go publishInitialEnvInfra(context.Background(), pub, orgProvider, clusterStore, projectStore, logger)

				gitopsProbeURL := pubCfg.ArgoCDRepoURL
				if gitopsProbeURL == "" {
					gitopsProbeURL = repoCfg.RepoURL
				}
				readinessProbers = append(readinessProbers, server.ReadinessProber{
					Name:  "gitops-repo",
					Check: kube.NewGitOpsRepoReadinessProbe(gitopsProbeURL),
				})
			}
		} else if cfgErr != nil && cfgErr != gitops.ErrConfigNotFound {
			logger.Warn("could not read gitops config from ConfigMap", "error", cfgErr)
		} else {
			logger.Info("gitops publisher disabled — configure via settings UI or Helm values")
		}
	} else {
		logger.Info("gitops publisher disabled — running in fake mode")
	}

	// Wrap the initial publisher in a holder so it can be swapped at runtime
	// when the user saves new GitOps config through the settings UI.
	publisherHolder := server.NewPublisherHolder(gitOpsPublisher)

	// Build the activator closure: called by gitopsHandler after config is saved.
	// It registers the repo with ArgoCD, hot-reloads the publisher, and
	// kicks off an initial env-infra publish so ArgoCD can start discovering apps.
	var gitOpsActivator server.GitOpsActivatorFunc
	if cfg.RuntimeMode != config.ModeFake && kubeClient != nil {
		gitOpsActivator = func(ctx context.Context, repoCfg *gitops.RepoConfig, username, password string) error {
			// 1. Register the repo with ArgoCD so it can clone for syncs.
			if regErr := gitops.RegisterArgoCDRepo(ctx, kubeClient, repoCfg, username, password); regErr != nil {
				// Non-fatal: ArgoCD may not be installed yet, or its namespace
				// may not exist. Log the warning and continue.
				logger.Warn("argocd repo registration failed — ArgoCD may not be installed yet",
					"error", regErr,
				)
			} else {
				logger.Info("argocd repo registered",
					"url", repoCfg.ArgoCDRepoURL,
					"secret", "argocd/suparship-gitops-repo",
				)
			}

			// 2. Ensure the root "App of Apps" Application exists so ArgoCD
			// syncs _infra/ (ApplicationSets, AppProjects) from the gitops repo.
			// Skipped when the dynamic client is unavailable (Application CRD
			// requires it) or when an externally managed root app already exists.
			if dynClient != nil {
				rootCfg := gitops.RootAppConfig{
					ArgoCDRepoURL: repoCfg.ArgoCDRepoURL,
					RepoURL:       repoCfg.RepoURL,
					Branch:        repoCfg.Branch,
				}
				if rootErr := gitops.EnsureRootApplication(ctx, dynClient, rootCfg); rootErr != nil {
					logger.Warn("root application creation failed — ArgoCD CRD may not be available",
						"error", rootErr,
					)
				} else {
					logger.Info("argocd root application ensured", "name", "suparship-apps")
				}
			}

			// 3. Rebuild the publisher from the new config.
			pub, err := gitops.NewPublisher(gitops.PublisherConfig{
				RepoURL:         repoCfg.RepoURL,
				RepoUser:        username,
				RepoPassword:    password,
				ArgoCDRepoURL:   repoCfg.ArgoCDRepoURL,
				KargoGitRepoURL: repoCfg.KargoGitRepoURL,
				Branch:          repoCfg.Branch,
				SyncAutomated:   true,
				TemplatesDir:    templatesDir,
			})
			if err != nil {
				return fmt.Errorf("rebuild gitops publisher: %w", err)
			}

			// 4. Hot-swap the live publisher so new app creates/promotes use it.
			publisherHolder.Swap(&gitOpsPublisherAdapter{
				inner:        pub,
				orgProvider:  orgProvider,
				clusterStore: clusterStore,
			})
			sealPublisherHolder.Swap(pub)
			logger.Info("gitops publisher hot-reloaded", "repo", repoCfg.RepoURL)

			// 5. Trigger initial env-infra publish in background (idempotent).
			// This ensures ArgoCD ApplicationSets and AppProjects are in Git
			// even before the first app is created.
			go publishInitialEnvInfra(context.Background(), pub, orgProvider, clusterStore, projectStore, logger)

			return nil
		}
	}

	srv := server.New(server.Config{
		Addr:                    addr,
		UIDir:                   uiDir,
		CORSOrigins:             origins,
		Authenticator:           authenticator,
		OrgProvider:             orgProvider,
		Templates:               templates,
		ProjectStore:            projectStore,
		RuntimeProvider:         runtimeProvider,
		LogsProvider:            logsProvider,
		PreviewStore:            previewStore,
		AppStore:                appStore,
		ClusterStore:            clusterStore,
		SecretBackend:           secretBackend,
		UpperLevelSecretWriter:  upperLevelSecretWriter,
		GitOpsPublisher:         publisherHolder,
		KargoPromoter:           kargoPromoter,
		KargoStatusReader:       kargoStatusReader,
		KargoPipelineReader:     kargoPipelineReader,
		DeploymentHistoryReader: deploymentHistoryReader,
		ReadinessProbers:        readinessProbers,
		CookieSecure:            cookieSecure,
		Logger:                  logger,
		KubeClient:              kubeClient,
		GitOpsConfigStore:       gitopsConfigStore,
		GitOpsActivator:         gitOpsActivator,
		SealedTokenPublisher:    sealPublisherHolder,
		TemplateRegistryStore:   templateRegistryStore,
		RegistryStore:           registryStore,
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

// GetPromotionStatus implements server.KargoStatusReader.
func (a *kargoPromoterAdapter) GetPromotionStatus(ctx context.Context, projectNS, promotionName string) (server.KargoPromotionResult, error) {
	info, err := a.store.GetPromotionStatus(ctx, projectNS, promotionName)
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

// ListAppStageStatuses implements server.KargoPipelineReader.
// It lists all Kargo Stage CRs in projectNS and filters to those belonging to
// appName (stage name starts with "{appName}-").
func (a *kargoPromoterAdapter) ListAppStageStatuses(ctx context.Context, projectNS, appName string) ([]server.KargoStageStatusResult, error) {
	all, err := a.store.ListStageStatuses(ctx, projectNS)
	if err != nil {
		return nil, err
	}

	prefix := appName + "-"
	var results []server.KargoStageStatusResult
	for stageName, s := range all {
		if len(stageName) <= len(prefix) || stageName[:len(prefix)] != prefix {
			continue
		}
		envName := stageName[len(prefix):]
		results = append(results, server.KargoStageStatusResult{
			StageName:             stageName,
			EnvName:               envName,
			Phase:                 s.Phase,
			Health:                s.Health,
			CurrentFreight:        s.CurrentFreight,
			AvailableFreightCount: s.AvailableFreightCount,
		})
	}

	slog.Debug("kargo pipeline adapter: filtered stages for app",
		"namespace", projectNS,
		"app", appName,
		"totalStages", len(all),
		"appStages", len(results),
	)

	// Order by envName for deterministic UI rendering: staging before prod.
	sort.Slice(results, func(i, j int) bool {
		return results[i].EnvName < results[j].EnvName
	})
	return results, nil
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

// argoCDHistoryAdapter bridges kube.ArgoCDStatusReader.GetAppDeploymentHistory
// to the server.DeploymentHistoryReader interface.
type argoCDHistoryAdapter struct {
	reader *kube.ArgoCDStatusReader
}

// GetAppDeploymentHistory implements server.DeploymentHistoryReader.
func (a *argoCDHistoryAdapter) GetAppDeploymentHistory(ctx context.Context, appName, envName string) ([]server.DeploymentHistoryEntry, error) {
	raw, err := a.reader.GetAppDeploymentHistory(ctx, appName, envName)
	if err != nil {
		return nil, err
	}
	out := make([]server.DeploymentHistoryEntry, 0, len(raw))
	for _, h := range raw {
		out = append(out, server.DeploymentHistoryEntry{
			ID:              h.ID,
			Revision:        h.Revision,
			DeployedAt:      h.DeployedAt,
			DeployStartedAt: h.DeployStartedAt,
			RepoURL:         h.RepoURL,
			Path:            h.Path,
			TargetRevision:  h.TargetRevision,
		})
	}
	return out, nil
}

// fakeHistoryAdapter bridges fake.FakeDeploymentHistoryReader to the
// server.DeploymentHistoryReader interface for use in fake/local dev mode.
type fakeHistoryAdapter struct {
	inner *fake.FakeDeploymentHistoryReader
}

// GetAppDeploymentHistory implements server.DeploymentHistoryReader.
func (a *fakeHistoryAdapter) GetAppDeploymentHistory(_ context.Context, appName, envName string) ([]server.DeploymentHistoryEntry, error) {
	raw := a.inner.GetFakeHistory(appName, envName)
	out := make([]server.DeploymentHistoryEntry, 0, len(raw))
	for _, h := range raw {
		out = append(out, server.DeploymentHistoryEntry{
			ID:              h.ID,
			Revision:        h.Revision,
			DeployedAt:      h.DeployedAt,
			DeployStartedAt: h.DeployStartedAt,
			RepoURL:         h.RepoURL,
			Path:            h.Path,
			TargetRevision:  h.TargetRevision,
		})
	}
	return out, nil
}

// publishInitialEnvInfra writes ArgoCD ApplicationSets and AppProjects to the
// GitOps repo for all org environments and known projects. It is called in a
// background goroutine after the GitOps publisher is first wired (or reloaded)
// so that ArgoCD can start discovering apps without waiting for the first
// app creation. The operation is idempotent — safe to call multiple times.
func publishInitialEnvInfra(
	ctx context.Context,
	pub *gitops.Publisher,
	orgProvider rbac.OrgProvider,
	clusterStore domain.ClusterStore,
	projectStore project.Store,
	logger *slog.Logger,
) {
	if orgProvider == nil {
		return
	}

	org, err := orgProvider.GetOrg(ctx)
	if err != nil || org == nil {
		logger.Warn("initial env infra: could not load org config", "error", err)
		return
	}

	appSetEnvs := make([]gitops.AppSetEnv, 0, len(org.Environments))
	for _, orgEnv := range org.Environments {
		clusterServer := "https://kubernetes.default.svc"
		if orgEnv.ClusterRef != "" && clusterStore != nil {
			if cluster, err := clusterStore.GetCluster(ctx, orgEnv.ClusterRef); err == nil && cluster.APIServer != "" {
				clusterServer = cluster.APIServer
			}
		}
		baseDomain := orgEnv.BaseDomain
		if baseDomain == "" {
			baseDomain = "localhost"
		}
		appSetEnvs = append(appSetEnvs, gitops.AppSetEnv{
			EnvName:          orgEnv.Name,
			ClusterServer:    clusterServer,
			NamespacePattern: orgEnv.NamespacePattern,
			BaseDomain:       baseDomain,
		})
	}

	if len(appSetEnvs) == 0 {
		logger.Info("initial env infra: no environments configured, skipping")
		return
	}

	// Collect project names; fall back to "default" if none exist yet.
	projectNames := []string{"default"}
	if projectStore != nil {
		if projects, err := projectStore.List(ctx); err == nil && len(projects) > 0 {
			projectNames = make([]string, 0, len(projects))
			for _, p := range projects {
				projectNames = append(projectNames, p.Metadata.Name)
			}
		}
	}

	for _, name := range projectNames {
		if err := pub.PublishEnvInfra(ctx, name, appSetEnvs); err != nil {
			logger.Warn("initial env infra: publish failed", "project", name, "error", err)
		} else {
			logger.Info("initial env infra: published", "project", name)
		}
	}
}
