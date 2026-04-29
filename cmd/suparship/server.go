package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/bootstrap"
	"github.com/suparcloud/suparship/internal/config"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
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
	"github.com/suparcloud/suparship/internal/secrets/onepassword"
	"github.com/suparcloud/suparship/internal/server"
	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/registrysync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
		vaultItemWriter         server.VaultItemWriter
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
		clusterPool             *k8s.ClusterClientPool
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

		// Wire 1Password vault item writer + upper-level writer when the org
		// backend is configured for 1Password. The SA token must already be
		// stored in the K8s Secret created by the provision flow. Failure is
		// non-fatal — log and continue without it.
		if orgProvider != nil {
			if org, orgErr := orgProvider.GetOrg(cmd.Context()); orgErr == nil && org != nil {
				if org.SecretBackend.Effective() == secrets.Backend1Password && org.SecretBackend.OnePassword != nil {
					saTokenRaw, tokenErr := func() (string, error) {
						sec, err := client.CoreV1().Secrets("suparship-system").Get(
							cmd.Context(), secrets.SATokenSecretName, metav1.GetOptions{},
						)
						if err != nil {
							return "", err
						}
						return string(sec.Data[secrets.SATokenSecretKey]), nil
					}()
					if tokenErr != nil {
						logger.Warn("vault item writer: could not read SA token — 1Password vault items will not be created automatically",
							"secret", secrets.SATokenSecretName, "error", tokenErr)
					} else if saTokenRaw != "" {
						saClient, saErr := onepassword.NewSDKClient(cmd.Context(), saTokenRaw)
						if saErr != nil {
							logger.Warn("vault item writer: SA client init failed — vault item auto-create disabled",
								"error", saErr)
						} else {
							saWriter := onepassword.NewSAVaultWriter(saClient)
							vaultItemWriter = server.NewSAVaultItemWriter(saWriter, org.SecretBackend.OnePassword.Bindings)
							logger.Info("vault item writer: 1Password SA client enabled — vault items created on app create")

							// Build a cluster→env resolver from the org snapshot.
							envForCluster := buildEnvForClusterResolver(org.Environments)
							upperLevelSecretWriter = onepassword.NewSAUpperLevelWriter(onepassword.SAUpperLevelWriterConfig{
								Client:          saClient,
								PlatformVaultID: org.SecretBackend.OnePassword.PlatformVaultID,
								Bindings:        org.SecretBackend.OnePassword.Bindings,
								OrgName:         org.Name,
								Naming:          org.ResourceNaming,
								EnvForCluster:   envForCluster,
							})
							if org.SecretBackend.OnePassword.PlatformVaultID == "" {
								logger.Warn("upper-level writer: 1Password platform vault not provisioned — org/project secret writes will fail until SA token is re-pasted",
									"hint", "Settings > Secrets > paste the SA token to auto-create the platform vault")
							} else {
								logger.Info("upper-level writer: 1Password backend enabled — org/project secrets land in platform vault, env-type/cluster in env vault",
									"platformVaultID", org.SecretBackend.OnePassword.PlatformVaultID)
							}
						}
					}
				}
			}
		}
		gitopsConfigStore = gitops.NewConfigStore(client)
		templateRegistryStore = tpl.NewRegistryStore(client)
		registryStore = registry.NewStore(client)
		// Build the per-cluster client pool so sealing certs can be fetched
		// directly from each registered cluster's kubeseal controller.
		clusterPool = k8s.NewClusterClientPool(kubeDeps.ClusterStore)

		// Bootstrap: reconcile Helm-provided ConfigMaps and log what was found.
		bootstrapResult := bootstrap.Reconcile(cmd.Context(), client, logger)
		logger.Info("bootstrap complete", "summary", bootstrap.FormatSummary(bootstrapResult))

		// Ensure the suparship-system ArgoCD AppProject exists. This is a
		// self-healing step: if the Helm chart was not yet upgraded (or ArgoCD
		// was installed after suparship), this creates the project automatically
		// so the root "App of Apps" can sync without manual intervention.
		// Non-fatal: if ArgoCD is not installed or permissions are insufficient,
		// a warning is logged and the server continues.
		bootstrap.ReconcileArgoCD(cmd.Context(), dynClient, logger)

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
			// ChartFetcher resolves chart bundles for templates imported via
			// the BYO-chart flow (where the chart .tgz lives in a cluster
			// ConfigMap rather than on disk). Built-in templates that ship
			// with the binary still resolve through TemplatesDir first.
			pubCfg.ChartFetcher = chartFetcherFromClient(kubeClient)

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
					inner:           pub,
					orgProvider:     orgProvider,
					clusterStore:    clusterStore,
					upperWriter:     upperLevelSecretWriter,
					backend:         secretBackend,
					appNamespaceFor: appStoreNamespaceResolver(appStore),
					projectStore:    projectStore,
					envConfigReader: envConfigReaderFromClient(kubeClient),
				}
				sealPublisherHolder.Swap(pub)
				logger.Info("gitops publisher enabled",
					"repo", repoCfg.RepoURL,
					"argocd_repo", pubCfg.ArgoCDRepoURL,
				)
				go publishInitialEnvInfra(context.Background(), pub, orgProvider, clusterStore, projectStore, logger)

			// Ensure the suparship-apps root ArgoCD Application exists.
			// This replaces the manual `kubectl apply -f config/gitops/root-app.yaml`
			// step by deriving the manifest from the live gitops ConfigMap.
			// Non-fatal: a warning is logged when ArgoCD is not installed or the
			// application already exists (idempotent create-only).
			go func() {
				if err := kube.EnsureRootArgoApp(
					context.Background(),
					dynClient,
					pubCfg.ArgoCDRepoURL,
					pubCfg.Branch,
					"argocd",
				); err != nil {
					logger.Warn("could not ensure suparship-apps root ArgoCD Application", "error", err)
				}
			}()

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
				ChartFetcher:    chartFetcherFromClient(kubeClient),
			})
			if err != nil {
				return fmt.Errorf("rebuild gitops publisher: %w", err)
			}

			// 4. Hot-swap the live publisher so new app creates/promotes use it.
			publisherHolder.Swap(&gitOpsPublisherAdapter{
				inner:           pub,
				orgProvider:     orgProvider,
				clusterStore:    clusterStore,
				upperWriter:     upperLevelSecretWriter,
				backend:         secretBackend,
				appNamespaceFor: appStoreNamespaceResolver(appStore),
				projectStore:    projectStore,
				envConfigReader: envConfigReaderFromClient(kubeClient),
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
		ClusterTemplateLoader:   clusterTemplateLoaderFromClient(kubeClient),
		RegistrySyncEngine:      registrySyncEngine(kubeClient, logger),
		ProjectStore:            projectStore,
		RuntimeProvider:         runtimeProvider,
		LogsProvider:            logsProvider,
		PreviewStore:            previewStore,
		AppStore:                appStore,
		ClusterStore:            clusterStore,
		SecretBackend:           secretBackend,
		UpperLevelSecretWriter:  upperLevelSecretWriter,
		VaultItemWriter:         vaultItemWriter,
		GitOpsPublisher:         publisherHolder,
		KargoPromoter:           kargoPromoter,
		KargoStatusReader:       kargoStatusReader,
		KargoPipelineReader:     kargoPipelineReader,
		DeploymentHistoryReader: deploymentHistoryReader,
		ReadinessProbers:        readinessProbers,
		CookieSecure:            cookieSecure,
		Logger:                  logger,
		KubeClient:              kubeClient,
		DynClient:               dynClient,
		ClusterPool:             clusterPool,
		GitOpsConfigStore:       gitopsConfigStore,
		GitOpsActivator:         gitOpsActivator,
		SealedTokenPublisher:    sealPublisherHolder,
		TemplateRegistryStore:   templateRegistryStore,
		RegistryStore:           registryStore,
	})

	startPeriodicTemplateSync(
		cmd.Context(),
		registrySyncEngine(kubeClient, logger),
		templateRegistryStore,
		logger,
	)

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
	// upperWriter and backend are used to populate AppPublishEnv.ScopeKeys —
	// which scope levels actually have keys in the vault — so the collapsed
	// ExternalSecret omits dataFrom entries for empty scopes (ESO would
	// otherwise error trying to extract from a missing item).
	//
	// Both are optional: when nil, the publisher's collapsed builder still
	// works but every scope is included.
	upperWriter secrets.UpperLevelWriter
	backend     secrets.Backend
	// appNamespaceFor resolves the app-env K8s namespace from app + env. Used
	// to read app/app-env secret keys from the right namespace when populating
	// ScopeKeys.
	appNamespaceFor func(ctx context.Context, projectName, appName, envName string) (string, error)
	// projectStore reads the project's own EnvConfig (project-scope env vars).
	// Optional: when nil, the project layer contributes no vars.
	projectStore project.Store
	// envConfigReader reads the cluster-scope env-var ConfigMap from
	// suparship-system. Optional: when nil, the cluster layer contributes no
	// vars.
	envConfigReader *envconfig.UpperLevelEnvWriter
}

// envResolved holds the resolved cluster and domain info for one environment.
type envResolved struct {
	clusterServer    string
	baseDomain       string
	namespacePattern string
	// bound is true when the org environment has a non-empty ClusterRef that
	// maps to a known cluster. GitOps artifacts are only published for bound
	// environments; unbound envs are tracked in the store so the UI can prompt
	// the operator to assign a cluster.
	bound bool
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
		// An env is bound when it has a non-empty ClusterRef that resolves to a
		// known cluster. Unbound envs are skipped during GitOps publishing.
		if orgEnv.ClusterRef != "" && a.clusterStore != nil {
			cluster, err := a.clusterStore.GetCluster(ctx, orgEnv.ClusterRef)
			if err != nil {
				slog.Warn("resolveEnvs: cluster not found in registry, falling back to in-cluster default",
					"env", orgEnv.Name, "clusterRef", orgEnv.ClusterRef, "err", err)
			} else if cluster.APIServer == "" {
				slog.Warn("resolveEnvs: cluster has empty apiServer, falling back to in-cluster default",
					"env", orgEnv.Name, "clusterRef", orgEnv.ClusterRef)
				res.bound = true // ClusterRef is valid even if apiServer defaults
			} else {
				res.clusterServer = cluster.APIServer
				res.bound = true
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

	// Load org config once for naming and backend info.
	var org *rbac.Org
	if a.orgProvider != nil {
		org, _ = a.orgProvider.GetOrg(ctx)
	}

	appSetEnvs := make([]gitops.AppSetEnv, 0, len(envs))
	pubEnvs := make([]gitops.AppPublishEnv, 0, len(envs))

	for _, env := range envs {
		res, ok := resolved[env.EnvName]
		if !ok {
			// Environment not in org config — treat as unbound so we don't
			// accidentally publish to a stale or undefined cluster.
			res = envResolved{
				clusterServer: "https://kubernetes.default.svc",
				baseDomain:    "localhost",
				bound:         false,
			}
		}

		// Only include bound environments in the ApplicationSet so ArgoCD
		// doesn't try to connect to a cluster that hasn't been registered.
		if res.bound {
			appSetEnvs = append(appSetEnvs, gitops.AppSetEnv{
				EnvName:       env.EnvName,
				ClusterServer: res.clusterServer,
				BaseDomain:    res.baseDomain,
			})
		}

		pub := gitops.AppPublishEnv{
			EnvName:    env.EnvName,
			EnvType:    env.EnvType,
			Order:      env.Order,
			Bound:      res.bound,
			BaseDomain: res.baseDomain,
			Namespace:  env.Namespace,
		}

		// Populate secret-store info from org backend config.
		if org != nil {
			a.enrichPubEnvWithSecrets(ctx, org, app, env.EnvName, &pub)
		}

		// Resolve and merge env vars from all six scopes into a single map.
		// The publisher writes this verbatim as the per-app ConfigMap so the
		// committed YAML in the GitOps repo is the audit-trail for what the
		// pod will see — no chart-side multi-source merging.
		pub.EnvVars = a.mergeAllEnvVars(ctx, app, env.EnvName, pub.ClusterRef, org)

		pubEnvs = append(pubEnvs, pub)
	}

	// Write appset.yaml + appproject.yaml for each bound environment so ArgoCD
	// can discover apps through its Git File generator. This is idempotent.
	if err := a.inner.PublishEnvInfra(ctx, app.ProjectName, appSetEnvs); err != nil {
		return fmt.Errorf("publish env infra: %w", err)
	}

	// Write app.yaml + values.yaml for each bound environment.
	return a.inner.PublishApp(ctx, app, pubEnvs)
}

// enrichPubEnvWithSecrets adds StoreName, VaultItemTitle, ClusterRef, and
// ScopeKeys to pub based on the org's secret backend config and naming
// patterns. ScopeKeys reflects which scope levels actually have keys in the
// vault so the collapsed ExternalSecret skips dataFrom entries for empty
// scopes — ESO would otherwise error trying to extract from a missing item.
func (a *gitOpsPublisherAdapter) enrichPubEnvWithSecrets(ctx context.Context, org *rbac.Org, app *domain.App, envName string, pub *gitops.AppPublishEnv) {
	naming := org.ResourceNaming
	orgName := org.Name
	if orgName == "" {
		orgName = "default"
	}

	np := secrets.NamingParams{
		Org:      orgName,
		Env:      envName,
		Project:  app.ProjectName,
		App:      app.Name,
		Provider: string(org.SecretBackend.Effective()),
	}

	pub.StoreName = naming.RenderClusterSecretStore(np)
	pub.VaultItemTitle = naming.RenderVaultItem(secrets.LevelAppEnv, np)

	// Resolve the cluster bound to this env from the org config so the
	// publisher can render the cluster-scope item title.
	for _, e := range org.Environments {
		if e.Name == envName {
			pub.ClusterRef = e.ClusterRef
			break
		}
	}

	pub.ScopeKeys = a.collectScopeKeys(ctx, app, envName, pub.ClusterRef)
}

// collectScopeKeys probes each scope level and reports which ones currently
// have at least one key in the vault. Errors are swallowed (treated as "no
// keys") because the resolved view is best-effort during a publish — partial
// information is better than failing the entire commit.
func (a *gitOpsPublisherAdapter) collectScopeKeys(ctx context.Context, app *domain.App, envName, clusterRef string) map[string]bool {
	out := make(map[string]bool, 6)

	if a.upperWriter != nil {
		if entries, err := a.upperWriter.ReadOrgSecretKeys(ctx); err == nil && len(entries) > 0 {
			out[secrets.LevelOrg] = true
		}
		if entries, err := a.upperWriter.ReadEnvTypeSecretKeys(ctx, envName); err == nil && len(entries) > 0 {
			out[secrets.LevelEnvironment] = true
		}
		if entries, err := a.upperWriter.ReadProjectSecretKeys(ctx, app.ProjectName); err == nil && len(entries) > 0 {
			out[secrets.LevelProject] = true
		}
		if clusterRef != "" {
			if entries, err := a.upperWriter.ReadClusterSecretKeys(ctx, clusterRef); err == nil && len(entries) > 0 {
				out[secrets.LevelCluster] = true
			}
		}
	}

	if a.backend != nil && a.appNamespaceFor != nil {
		// App-level secrets live in the same namespace as any app-env secret;
		// passing the env name is fine here because the app-level Secret name
		// is keyed by project+app, not env.
		if ns, err := a.appNamespaceFor(ctx, app.ProjectName, app.Name, envName); err == nil && ns != "" {
			if entries, err := a.backend.ListKeys(ctx, ns, secrets.AppLevelSecretName(app.ProjectName, app.Name)); err == nil && len(entries) > 0 {
				out[secrets.LevelApp] = true
			}
			if entries, err := a.backend.ListKeys(ctx, ns, secrets.AppEnvSecretName(app.ProjectName, app.Name, envName)); err == nil && len(entries) > 0 {
				out[secrets.LevelAppEnv] = true
			}
		}
	}

	return out
}

// mergeAllEnvVars resolves env vars across the six-scope hierarchy and returns
// the flattened map the publisher writes into gitops-output as the per-app
// ConfigMap. Precedence is low-to-high — later scopes overwrite earlier keys:
//
//	1. org           (org.EnvConfig.Vars)
//	2. env-type      (org.Environments[envName].EnvConfig.Vars)
//	3. project       (project.Spec.EnvConfig.Vars)
//	4. app           (app.Spec.EnvConfig.Vars)
//	5. app-env       (app.Spec.EnvironmentDefaults[envName].EnvConfig.Vars)
//	6. cluster       (suparship-envvars-cluster-{name} ConfigMap data)
//
// Errors at any layer are swallowed and the layer is treated as empty —
// publishing should not fail because an upper-level ConfigMap is unreadable;
// partial information is better than refusing to commit. Returns nil when no
// layer contributes a key, mirroring helmvalues' "omit empty" YAML behaviour.
func (a *gitOpsPublisherAdapter) mergeAllEnvVars(ctx context.Context, app *domain.App, envName, clusterRef string, org *rbac.Org) map[string]string {
	merged := map[string]string{}

	if org != nil {
		for k, v := range org.EnvConfig.Vars {
			merged[k] = v
		}
		for _, e := range org.Environments {
			if e.Name == envName {
				for k, v := range e.EnvConfig.Vars {
					merged[k] = v
				}
				break
			}
		}
	}

	if a.projectStore != nil {
		if proj, err := a.projectStore.Get(ctx, app.ProjectName); err == nil && proj != nil {
			for k, v := range proj.Spec.EnvConfig.Vars {
				merged[k] = v
			}
		}
	}

	for k, v := range app.Spec.EnvConfig.Vars {
		merged[k] = v
	}

	if override, ok := app.Spec.EnvironmentDefaults[envName]; ok {
		for k, v := range override.EnvConfig.Vars {
			merged[k] = v
		}
	}

	if clusterRef != "" && a.envConfigReader != nil {
		if cfg, err := a.envConfigReader.ReadClusterEnvConfig(ctx, clusterRef); err == nil {
			for k, v := range cfg.Vars {
				merged[k] = v
			}
		}
	}

	if len(merged) == 0 {
		return nil
	}
	return merged
}

// PublishAppEnv implements server.GitOpsPublisher by resolving cluster info for
// the single environment and writing its app.yaml + values.yaml to the GitOps
// repo. Called on every explicit promotion so the target env's files exist
// before Kargo / ArgoCD act.
func (a *gitOpsPublisherAdapter) PublishAppEnv(ctx context.Context, app *domain.App, env *domain.AppEnvironment) error {
	resolved := a.resolveEnvs(ctx)
	res, ok := resolved[env.EnvName]
	if !ok {
		res = envResolved{
			clusterServer: "https://kubernetes.default.svc",
			baseDomain:    "localhost",
			bound:         false,
		}
	}
	pub := gitops.AppPublishEnv{
		EnvName:    env.EnvName,
		EnvType:    env.EnvType,
		Order:      env.Order,
		Bound:      res.bound,
		BaseDomain: res.baseDomain,
		Namespace:  env.Namespace,
	}

	var org *rbac.Org
	if a.orgProvider != nil {
		org, _ = a.orgProvider.GetOrg(ctx)
	}
	if org != nil {
		a.enrichPubEnvWithSecrets(ctx, org, app, env.EnvName, &pub)
	}
	pub.EnvVars = a.mergeAllEnvVars(ctx, app, env.EnvName, pub.ClusterRef, org)

	return a.inner.PublishAppEnv(ctx, app, pub)
}

// UnpublishApp implements server.GitOpsPublisher by removing all gitops-output
// directories for the given app and committing the deletion.
func (a *gitOpsPublisherAdapter) UnpublishApp(ctx context.Context, projectName, appName string) error {
	return a.inner.UnpublishApp(ctx, projectName, appName)
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

	// Update the publisher with the current org config so ClusterSecretStores
	// and naming patterns are consistent when PublishEnvInfra runs.
	orgName := org.Name
	if orgName == "" {
		orgName = "default"
	}
	pub.SetOrgConfig(orgName, org.ResourceNaming, &org.SecretBackend, org.Branding)

	appSetEnvs := make([]gitops.AppSetEnv, 0, len(org.Environments))
	for _, orgEnv := range org.Environments {
		clusterServer := "https://kubernetes.default.svc"
		if orgEnv.ClusterRef != "" && clusterStore != nil {
			cluster, err := clusterStore.GetCluster(ctx, orgEnv.ClusterRef)
			if err != nil {
				logger.Warn("initial env infra: cluster not found in registry, falling back to in-cluster default",
					"env", orgEnv.Name, "clusterRef", orgEnv.ClusterRef, "err", err)
			} else if cluster.APIServer == "" {
				logger.Warn("initial env infra: cluster has empty apiServer, falling back to in-cluster default",
					"env", orgEnv.Name, "clusterRef", orgEnv.ClusterRef)
			} else {
				clusterServer = cluster.APIServer
			}
		}
		baseDomain := orgEnv.BaseDomain
		if baseDomain == "" {
			baseDomain = "localhost"
		}
		appSetEnvs = append(appSetEnvs, gitops.AppSetEnv{
			EnvName:       orgEnv.Name,
			ClusterServer: clusterServer,
			BaseDomain:    baseDomain,
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

// buildEnvForClusterResolver returns a closure that maps a registered cluster
// name to the env-name that has that cluster as its ClusterRef in the org
// config. Used by the 1Password upper-level writer to find the correct env
// vault for cluster-scope items. Returns "" for unknown clusters.
//
// The resolver captures envs by value at construction time. Restart suparship
// to pick up env-binding changes — same lifecycle as the rest of the
// 1Password wiring (SA client + bindings).
func buildEnvForClusterResolver(envs []rbac.OrgEnvironment) func(string) string {
	clusterToEnv := make(map[string]string, len(envs))
	for _, e := range envs {
		if e.ClusterRef != "" {
			clusterToEnv[e.ClusterRef] = e.Name
		}
	}
	return func(cluster string) string {
		return clusterToEnv[cluster]
	}
}

// envConfigReaderFromClient builds a cluster-scope env-var reader from the K8s
// client. Returns nil when the client is unavailable (fake mode) so the adapter
// gracefully skips the cluster layer.
func envConfigReaderFromClient(client kubernetes.Interface) *envconfig.UpperLevelEnvWriter {
	if client == nil {
		return nil
	}
	return envconfig.NewUpperLevelEnvWriter(client)
}

// registrySyncEngine builds the external-template sync engine, or returns
// nil when the cluster client is unavailable (fake/local-dev mode). Kept
// in a helper so the wiring at server.New stays narrow.
func registrySyncEngine(client kubernetes.Interface, logger *slog.Logger) *registrysync.Engine {
	if client == nil {
		return nil
	}
	return &registrysync.Engine{
		Client:     client,
		Logger:     logger,
		CloneDepth: 1,
	}
}

// startPeriodicTemplateSync runs SyncAll on a ticker so external repos
// converge without an operator manually clicking Sync. The interval comes
// from SUPARSHIP_TEMPLATE_SYNC_INTERVAL (Go duration string, e.g. "5m");
// values <= 0 disable the loop entirely. Default is 5 minutes — long
// enough that the API server isn't constantly cloning repos, short enough
// that a tag bump shows up the same workday.
//
// The goroutine returns when ctx is cancelled. Each tick reads the
// registry fresh so operator edits to External[] take effect on the next
// run without restart.
func startPeriodicTemplateSync(
	ctx context.Context,
	engine *registrysync.Engine,
	store *tpl.RegistryStore,
	logger *slog.Logger,
) {
	if engine == nil || store == nil {
		return
	}
	intervalStr := envOr("SUPARSHIP_TEMPLATE_SYNC_INTERVAL", "5m")
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		logger.Warn("template sync: invalid interval, disabling periodic sync",
			"value", intervalStr, "err", err)
		return
	}
	if interval <= 0 {
		logger.Info("template sync: periodic sync disabled (interval <= 0)")
		return
	}
	logger.Info("template sync: periodic sync enabled", "interval", interval)

	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runOneTemplateSync(ctx, engine, store, logger)
			}
		}
	}()
}

// runOneTemplateSync fetches the registry, syncs each external repo, folds
// results back via registrysync.ApplyResult, and persists. Errors are
// logged — periodic sync is a background concern, not a request-path one.
func runOneTemplateSync(
	ctx context.Context,
	engine *registrysync.Engine,
	store *tpl.RegistryStore,
	logger *slog.Logger,
) {
	reg, err := store.Get(ctx)
	if err != nil {
		// "Not configured yet" is a normal state during initial install —
		// log at debug so the noise doesn't spam during onboarding.
		logger.Debug("template sync: registry not yet configured", "err", err)
		return
	}
	if len(reg.External) == 0 {
		return
	}
	results := engine.SyncAll(ctx, reg)
	for i, repo := range reg.External {
		if i < len(results) {
			registrysync.ApplyResult(reg, repo, results[i])
			r := results[i]
			if r.Err != nil {
				logger.Warn("template sync: source failed",
					"source", r.SourceName,
					"imported", len(r.Templates),
					"err", r.Err,
				)
			} else {
				logger.Info("template sync: source ok",
					"source", r.SourceName,
					"imported", len(r.Templates),
				)
			}
		}
	}
	if err := store.Save(ctx, reg); err != nil {
		logger.Warn("template sync: persist registry state failed", "err", err)
	}
}

// clusterTemplateLoaderFromClient returns a server.ClusterTemplateLoader
// that reads templates persisted as ConfigMaps in the suparship-system
// namespace, so freshly imported charts surface in the gallery without a
// restart. Returns nil in fake/local-dev mode (no client) — the templates
// handler then serves only the disk-loaded built-ins.
func clusterTemplateLoaderFromClient(client kubernetes.Interface) server.ClusterTemplateLoader {
	if client == nil {
		return nil
	}
	return func(ctx context.Context) ([]*tpl.Template, error) {
		return kube.LoadTemplates(ctx, client)
	}
}

// kubeChartFetcher implements gitops.ChartFetcher by reading chart.tgz from
// the template's ConfigMap in suparship-system. Built once per server start
// and shared across publisher rebuilds so the underlying client stays warm.
type kubeChartFetcher struct {
	client kubernetes.Interface
}

// LoadChartBundle satisfies gitops.ChartFetcher.
func (k *kubeChartFetcher) LoadChartBundle(ctx context.Context, templateName string) ([]byte, error) {
	return kube.LoadChartBundle(ctx, k.client, templateName)
}

// chartFetcherFromClient returns a gitops.ChartFetcher backed by the cluster
// client, or nil when running without a client (fake mode) so the publisher
// preserves its prior "skip silently" behaviour for missing charts.
func chartFetcherFromClient(client kubernetes.Interface) gitops.ChartFetcher {
	if client == nil {
		return nil
	}
	return &kubeChartFetcher{client: client}
}

// appStoreNamespaceResolver returns a function that looks up the resolved K8s
// namespace for an app-env from the AppStore. Returns "" when the env is not
// persisted (e.g. apps created before AppEnvironment was introduced) so that
// callers can treat the lookup as best-effort.
func appStoreNamespaceResolver(store domain.AppStore) func(ctx context.Context, projectName, appName, envName string) (string, error) {
	if store == nil {
		return func(context.Context, string, string, string) (string, error) {
			return "", nil
		}
	}
	return func(ctx context.Context, projectName, appName, envName string) (string, error) {
		env, err := store.GetAppEnvironment(ctx, projectName, appName, envName)
		if err != nil {
			return "", err
		}
		return env.Namespace, nil
	}
}
