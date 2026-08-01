package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/audit"
	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/bootstrap"
	"github.com/suparcloud/suparship/internal/branding"
	"github.com/suparcloud/suparship/internal/config"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/fake"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/helmvalues"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/kube"
	"github.com/suparcloud/suparship/internal/license"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/registry"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/seal"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/secrets/onepassword"
	"github.com/suparcloud/suparship/internal/server"
	"github.com/suparcloud/suparship/internal/token"
	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/credstore"
	"github.com/suparcloud/suparship/internal/tpl/registrysync"
	"github.com/suparcloud/suparship/internal/version"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
		tokenStore              token.Store
		runtimeProvider         runtime.Provider
		logsProvider            runtime.LogsProvider
		appStore                domain.AppStore
		stackStore              domain.StackStore
		clusterStore            domain.ClusterStore
		vaultStore              secrets.VaultStore
		templates               []*tpl.Template
		readinessProbers        []server.ReadinessProber
		kargoPromoter           server.KargoPromoter
		kargoStatusReader       server.KargoStatusReader
		kargoPipelineReader     server.KargoPipelineReader
		deploymentHistoryReader server.DeploymentHistoryReader
		projectAppCounter       server.ProjectAppCounter
		appDiagnosticsReader    server.AppDiagnosticsReader
		stuckAppManager         server.StuckAppManager
		argoRefresh             argoRefresher // triggers ArgoCD refresh after publish

		kubeClient            kubernetes.Interface
		dynClient             dynamic.Interface
		gitopsConfigStore     *gitops.ConfigStore
		templateRegistryStore *tpl.RegistryStore
		registryStore         *registry.Store
		clusterPool           *k8s.ClusterClientPool
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
		tokenStore = token.NewMemStore()
		runtimeProvider = deps.RuntimeProvider
		logsProvider = deps.LogsProvider
		appStore = deps.AppStore
		clusterStore = deps.ClusterStore
		vaultStore = secrets.NewMemVaultStore()
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
			// Wire Kargo promoter. Each project's Kargo CRs live in its own
			// kargo-{project} namespace on this (tooling) cluster; the store
			// resolves the namespace from the project name per call.
			kargoStore := kube.NewKargoStore(dynClient)
			kargoAdapter := &kargoPromoterAdapter{store: kargoStore}
			kargoPromoter = kargoAdapter
			kargoStatusReader = kargoAdapter
			kargoPipelineReader = kargoAdapter
			// Wire ArgoCD deployment history reader. The same reader also
			// counts a project's live Applications to sequence two-phase
			// project deletion.
			argoCDReader := kube.NewArgoCDStatusReaderFromDynamic(dynClient, "")
			deploymentHistoryReader = &argoCDHistoryAdapter{reader: argoCDReader}
			projectAppCounter = argoCDReader
			appDiagnosticsReader = argoCDReader
			stuckAppManager = &stuckAppAdapter{reader: argoCDReader}
			argoRefresh = argoCDReader
			logger.Info("kargo promoter enabled via dynamic client")
			logger.Info("argocd deployment history reader enabled")
		}

		adminSecretRef := adminSecretRefFromFlags(cmd)
		logger.Info("admin auth backend configured",
			"secret_namespace", adminSecretRef.Namespace,
			"secret_name", adminSecretRef.Name,
			"username_key", adminSecretRef.UsernameKey,
			"password_hash_key", adminSecretRef.PasswordHashKey,
		)
		authenticator = auth.NewK8sAuthenticator(client, adminSecretRef).WithLogger(logger)

		adminUser := "admin"
		creds, err := auth.GetAdminSecret(context.Background(), client, adminSecretRef)
		if err != nil {
			logger.Warn("could not read admin secret at startup",
				"error", err,
				"hint", "the Secret may not exist yet, or the configured keys may not match its keys",
			)
		} else if creds == nil {
			logger.Warn("admin secret not present at startup",
				"hint", "run `suparship admin bootstrap` to create one, or provision it via your secret store",
			)
		} else {
			adminUser = creds.Username
			logger.Info("admin secret loaded", "username", adminUser)
		}

		fallbackOrg := rbac.NewDefaultOrg("default", "Default Organization", adminUser)
		orgProvider = rbac.NewK8sOrgProvider(client, fallbackOrg)

		// Wire all Kubernetes-backed store and runtime implementations
		// through the consolidated kube.ServerDeps bundle.
		kubeDeps := kube.NewServerDeps(client, rbac.NewOrgEnvNamesAdapter(orgProvider), dynClient)
		projectStore = kubeDeps.ProjectStore
		previewStore = kubeDeps.PreviewStore
		tokenStore = token.NewKubeStore(client)
		runtimeProvider = kubeDeps.RuntimeProvider
		logsProvider = kubeDeps.LogsProvider
		appStore = kubeDeps.AppStore
		stackStore = kube.NewK8sStackStore(client)
		clusterStore = kubeDeps.ClusterStore
		// Default: k8s backend (vault = namespace). Overridden below when the
		// org is configured for 1Password and an SA token is available.
		vaultStore = secrets.NewK8sVaultStore(client)

		// orgProvider is set unconditionally above; a bare block scopes the
		// 1Password wiring's locals without a redundant (always-true) nil guard.
		{
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
						logger.Warn("1Password backend: could not read SA token — falling back to k8s vault store",
							"secret", secrets.SATokenSecretName, "error", tokenErr)
					} else if saTokenRaw != "" {
						saClient, saErr := onepassword.NewSDKClient(cmd.Context(), saTokenRaw)
						if saErr != nil {
							logger.Warn("1Password backend: SA client init failed — falling back to k8s vault store", "error", saErr)
						} else {
							// Resolver loads org config fresh so vault selections
							// (global/env) made after startup take effect. Cluster
							// scope resolves to its env vault.
							resolver := func(scope secrets.Scope) (string, error) {
								o, err := orgProvider.GetOrg(context.Background())
								if err != nil {
									return "", err
								}
								return o.SecretBackend.VaultIDForScope(scope)
							}
							vaultStore = onepassword.NewSAVaultStore(saClient, resolver)
							logger.Info("1Password vault store enabled (global/env vaults resolved from org config; cluster overrides live in env vaults)")
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

		// Upgrade check: compare the persisted org-config schema version with
		// this binary's. Advisory only — surfaces migration guidance in logs.
		if org, err := orgProvider.GetOrg(cmd.Context()); err == nil && org != nil {
			switch check, msg := rbac.CheckSchema(org); check {
			case rbac.SchemaCurrent:
				// up to date — say nothing
			case rbac.SchemaDowngrade:
				logger.Error("org config schema check", "detail", msg)
			default:
				logger.Warn("org config schema check", "detail", msg)
			}
		}

		// Ensure the suparship-system ArgoCD AppProject exists. This is a
		// self-healing step: if the Helm chart was not yet upgraded (or ArgoCD
		// was installed after suparship), this creates the project automatically
		// so the root "App of Apps" can sync without manual intervention.
		// Non-fatal: if ArgoCD is not installed or permissions are insufficient,
		// a warning is logged and the server continues.
		bootstrap.ReconcileArgoCD(cmd.Context(), dynClient, logger)

		// Provision each project's Kargo credential Secrets in its kargo-{project}
		// namespace from the org registry/gitops config, so Warehouses and promotion
		// git steps can authenticate without waiting for a config-save or app-publish.
		// The cred path also ensures+labels each kargo-{project} namespace as a Kargo
		// Project namespace. Best-effort, async.
		go reconcileKargoRegistryCreds(context.Background(), registryStore, gitopsConfigStore, projectStore, logger)

		// Templates stored as ConfigMaps in the cluster (label
		// suparship.io/type=template, namespace suparship-system) are served
		// LIVE via cfg.ClusterTemplateLoader / the publisher's clusterLoader —
		// NOT frozen into the built-in set at startup. Freezing them made
		// externally-synced templates impossible to update or delete (the stale
		// startup copy shadowed the live one). With no --templates-dir there are
		// no disk built-ins; every template resolves live from the cluster.
		// A best-effort count is logged for operator visibility only.
		if templatesDir == "" {
			if clusterTemplates, err := kube.LoadTemplates(cmd.Context(), client); err != nil {
				logger.Warn("could not list cluster templates at startup (served live regardless)",
					"error", err,
				)
			} else {
				logger.Info("cluster templates available (served live)", "count", len(clusterTemplates))
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
			// TemplateLoader lets the publisher detect external-mode
			// templates (engine.chart points at a Helm registry) so it
			// routes per-app files to envs-external/... and skips the
			// charts/ extract step. Inline-mode templates pass through
			// unchanged.
			pubCfg.TemplateLoader = templateLoaderFromClient(kubeClient)

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
					kubeClient:      kubeClient,
					vault:           vaultStore,
					projectStore:    projectStore,
					stackStore:      stackStore,
					envConfigReader: envConfigReaderFromClient(kubeClient, brandingFromOrg(cmd.Context(), orgProvider)),
					builtin:         templates,
					clusterLoader:   clusterTemplateLoaderFromClient(kubeClient),
					overrideLoader:  templateOverrideLoaderFromClient(kubeClient),
					argoRefresher:   argoRefresh,
				}
				sealPublisherHolder.Swap(pub)
				logger.Info("gitops publisher enabled",
					"repo", repoCfg.RepoURL,
					"argocd_repo", pubCfg.ArgoCDRepoURL,
				)
				go func() {
					bg := context.Background()
					publishInitialEnvInfra(bg, pub, orgProvider, clusterStore, projectStore, logger)
					// Copy legacy-named app-tier secret items to their new
					// project-qualified names BEFORE the republish below emits those
					// new names, so every ExternalSecret's dataFrom resolves at
					// cut-over (no NotReady gap; deletionPolicy:Retain covers the
					// rest). Runs once per fleet, gated by its own marker; legacy
					// items are pruned later via `suparship secrets prune-legacy-items`.
					if migrator, ok := vaultStore.(secrets.LegacyItemMigrator); !ok {
						logger.Info("item migration: backend does not support item copy; skipping")
					} else if generatorStateValue(bg, kubeClient, itemNameMigrationKey) == itemNameMigrationDone {
						logger.Info("item migration: skipped — fleet already migrated")
					} else if failures := copyLegacyAppItems(bg, migrator, appStore, projectStore, orgProvider, logger); failures == 0 {
						setGeneratorStateValue(bg, kubeClient, itemNameMigrationKey, itemNameMigrationDone, logger)
					} else {
						logger.Warn("item migration: not marking done — some copies failed; will retry next boot", "failures", failures)
					}
					// Re-publish every app so manifest-contract changes (e.g. the
					// version-scoped chart layout, and the project-qualified item
					// names above) land fleet-wide — but only once per generator
					// bump, not on every boot. The republish is a full
					// clone+commit+push per app; gating on a persisted marker keeps
					// restarts (and crash loops) from re-running the whole fleet.
					// Sequential after env infra + item copy to avoid concurrent
					// pushes and to guarantee new items exist first.
					if applied := generatorStateValue(bg, kubeClient, generatorStateKey); applied == version.Generator {
						logger.Info("republish apps: skipped — fleet already on current generator", "generator", version.Generator)
					} else if failures := republishAllApps(bg, gitOpsPublisher, appStore, projectStore, orgProvider, logger); failures == 0 {
						// Mark the boundary crossed only on a clean run, so a
						// partial failure re-runs (self-heals) on the next boot.
						setGeneratorStateValue(bg, kubeClient, generatorStateKey, version.Generator, logger)
					} else {
						logger.Warn("republish apps: not marking generator applied — some apps failed; will retry next boot",
							"failures", failures, "generator", version.Generator)
					}
				}()
				go selfHealSealedTokens(context.Background(), pub, orgProvider, clusterStore, clusterPool, kubeClient, logger)

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
						pubCfg.SubPath,
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
					SubPath:       repoCfg.SubPath,
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
				RepoURL:           repoCfg.RepoURL,
				RepoUser:          username,
				RepoPassword:      password,
				ArgoCDRepoURL:     repoCfg.ArgoCDRepoURL,
				KargoGitRepoURL:   repoCfg.KargoGitRepoURL,
				Branch:            repoCfg.Branch,
				SubPath:           repoCfg.SubPath,
				CommitAuthorName:  repoCfg.CommitAuthorName,
				CommitAuthorEmail: repoCfg.CommitAuthorEmail,
				SyncAutomated:     true,
				TemplatesDir:      templatesDir,
				ChartFetcher:      chartFetcherFromClient(kubeClient),
				TemplateLoader:    templateLoaderFromClient(kubeClient),
			})
			if err != nil {
				return fmt.Errorf("rebuild gitops publisher: %w", err)
			}

			// 4. Hot-swap the live publisher so new app creates/promotes use it.
			publisherHolder.Swap(&gitOpsPublisherAdapter{
				inner:           pub,
				orgProvider:     orgProvider,
				clusterStore:    clusterStore,
				kubeClient:      kubeClient,
				vault:           vaultStore,
				projectStore:    projectStore,
				stackStore:      stackStore,
				envConfigReader: envConfigReaderFromClient(kubeClient, brandingFromOrg(ctx, orgProvider)),
				builtin:         templates,
				clusterLoader:   clusterTemplateLoaderFromClient(kubeClient),
				overrideLoader:  templateOverrideLoaderFromClient(kubeClient),
				argoRefresher:   argoRefresh,
			})
			sealPublisherHolder.Swap(pub)
			logger.Info("gitops publisher hot-reloaded", "repo", repoCfg.RepoURL)

			// 5. Trigger initial env-infra publish in background (idempotent).
			// This ensures ArgoCD ApplicationSets and AppProjects are in Git
			// even before the first app is created.
			go publishInitialEnvInfra(context.Background(), pub, orgProvider, clusterStore, projectStore, logger)
			go selfHealSealedTokens(context.Background(), pub, orgProvider, clusterStore, clusterPool, kubeClient, logger)

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
		TokenStore:              tokenStore,
		RuntimeProvider:         runtimeProvider,
		LogsProvider:            logsProvider,
		PreviewStore:            previewStore,
		AppStore:                appStore,
		StackStore:              stackStore,
		ClusterStore:            clusterStore,
		VaultStore:              vaultStore,
		GitOpsPublisher:         publisherHolder,
		KargoPromoter:           kargoPromoter,
		KargoStatusReader:       kargoStatusReader,
		KargoPipelineReader:     kargoPipelineReader,
		DeploymentHistoryReader: deploymentHistoryReader,
		ProjectAppCounter:       projectAppCounter,
		AppDiagnosticsReader:    appDiagnosticsReader,
		StuckAppManager:         stuckAppManager,
		ReadinessProbers:        readinessProbers,
		CookieSecure:            cookieSecure,
		License:                 license.Community{},                // open-source core; enterprise builds override
		Auditor:                 audit.NewSlogAuditor(logger),       // enterprise builds can supply a SIEM sink
		Authorizer:              rbac.NewOrgAuthorizer(orgProvider), // enterprise builds can supply custom-role/ABAC policy
		Logger:                  logger,
		KubeClient:              kubeClient,
		DynClient:               dynClient,
		ClusterPool:             clusterPool,
		GitOpsConfigStore:       gitopsConfigStore,
		GitOpsActivator:         gitOpsActivator,
		SealedTokenPublisher:    sealPublisherHolder,
		TemplateRegistryStore:   templateRegistryStore,
		TemplateCredStore:       templateCredStore(kubeClient, dynClient, logger),
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
	// kubeClient loads chart bundles for publish-time image discovery (reading the
	// chart's effective values to resolve each CD-selected image's repository).
	// Optional: when nil, discovery degrades to overlay-only values.
	kubeClient kubernetes.Interface
	// vault populates AppPublishEnv.ScopeKeys — which (scope, tier) items have
	// keys — so the publisher only emits ExternalSecrets that resolve. Optional:
	// when nil, no scope presence is reported (no ExternalSecrets emitted).
	vault secrets.VaultStore
	// projectStore reads the project's own EnvConfig (project-scope env vars).
	// Optional: when nil, the project layer contributes no vars.
	projectStore project.Store
	// stackStore reads an app's stack record (Spec.Stack) for the stack-scope
	// override layer (env vars + raw values) that sits between project and app.
	// Optional: nil → no stack layer.
	stackStore domain.StackStore
	// envConfigReader reads the cluster-scope env-var ConfigMap from
	// suparship-system. Optional: when nil, the cluster layer contributes no
	// vars.
	envConfigReader *envconfig.UpperLevelEnvWriter
	// builtin + clusterLoader resolve an app's template by name (cluster
	// overrides built-in) so the adapter can layer the template's PE-authored
	// DefaultValues/EnvValues overlays into the published per-env values.
	// Resolved live so externally-synced templates apply without a restart.
	// Optional: when both are nil/empty no overlay is applied.
	builtin       []*tpl.Template
	clusterLoader server.ClusterTemplateLoader
	// overrideLoader reads the org-level platform override for a template
	// (PE/SRE-authored, stored separately from the template so sync can't clobber
	// it). Merged on top of the template's own Default/Env values. Optional: nil
	// → no org layer. Best-effort — a load error degrades to "no override".
	overrideLoader func(ctx context.Context, name string) (*domain.TemplateOverride, error)
	// argoRefresher triggers an ArgoCD refresh on the affected Application(s)
	// right after a publish commits, so the change applies immediately instead of
	// waiting for ArgoCD's poll interval. Best-effort; nil → rely on the poll.
	argoRefresher argoRefresher
}

// argoRefresher triggers ArgoCD reconciliation after a publish. Implemented by
// kube.ArgoCDStatusReader (RefreshApps/RefreshAppSets).
type argoRefresher interface {
	RefreshApps(ctx context.Context, project string, appNames []string) error
	RefreshAppSets(ctx context.Context, appSetNames []string) error
}

// refreshApps triggers a best-effort ArgoCD refresh of the given apps; a failure
// is logged and swallowed (ArgoCD's poll is the fallback).
func (a *gitOpsPublisherAdapter) refreshApps(ctx context.Context, project string, appNames ...string) {
	if a.argoRefresher == nil || len(appNames) == 0 {
		return
	}
	if err := a.argoRefresher.RefreshApps(ctx, project, appNames); err != nil {
		slog.Warn("argocd refresh failed; falling back to poll", "project", project, "apps", appNames, "err", err)
	}
}

// resolveTemplate resolves a template by name live (cluster overrides built-in)
// via the shared server.ResolveTemplates so the publish path agrees with the
// gallery / app-creation lookups.
//
// A cluster-fetch failure is returned as an error rather than swallowed: the
// publish path must fail loud instead of silently shipping a values.yaml without
// the PE-authored platform/env overlays. Returns (nil, nil) when the fetch
// succeeds but the name simply isn't found.
func (a *gitOpsPublisherAdapter) resolveTemplate(ctx context.Context, name string) (*tpl.Template, error) {
	byName, err := server.ResolveTemplates(ctx, a.builtin, a.clusterLoader)
	if err != nil {
		return nil, fmt.Errorf("load cluster templates: %w", err)
	}
	return byName[name], nil
}

// setPlatformOverlays populates the PE-authored values overlays on an
// AppPublishEnv: the template's own Default/Env values, with the org-level
// override (ov) merged ON TOP (org refines the chart/template). Both are resolved
// once per publish and threaded in, so a multi-env publish does not re-fetch per
// env. The developer's rawValues still win over this platform layer downstream
// (publisher.envOverlay). No-op when tmpl is nil (app has no matching template).
func setPlatformOverlays(pub *gitops.AppPublishEnv, tmpl *tpl.Template, ov *domain.TemplateOverride, envName string) {
	if tmpl == nil {
		return
	}
	// BYO/passthrough templates opt out of the injected canonical values base.
	pub.SkipCanonicalBase = !tmpl.Spec.CanonicalValues()
	def, env, cluster := computePlatformOverlays(tmpl, ov, envName)
	pub.PlatformDefaultValues = def
	pub.PlatformEnvValues = env
	pub.PlatformClusterValues = cluster
}

// computePlatformOverlays resolves a template's PE-authored value overlays for one
// env: the template's own Default/Env values with the org-level TemplateOverride
// (ov) merged ON TOP (org refines the template), plus the per-cluster override map.
// Shared by the app's primary template (setPlatformOverlays) and per composed
// component (setComponentPlatformOverlays).
func computePlatformOverlays(tmpl *tpl.Template, ov *domain.TemplateOverride, envName string) (def, env map[string]any, cluster map[string]map[string]any) {
	if tmpl == nil {
		return nil, nil, nil
	}
	def = helmvalues.DeepCopyMap(tmpl.Spec.DefaultValues)
	if tmpl.Spec.EnvValues != nil {
		env = helmvalues.DeepCopyMap(tmpl.Spec.EnvValues[envName])
	}
	if ov != nil {
		def = helmvalues.DeepMerge(def, helmvalues.DeepCopyMap(ov.DefaultValues))
		if ov.EnvValues != nil {
			env = helmvalues.DeepMerge(env, helmvalues.DeepCopyMap(ov.EnvValues[envName]))
		}
		if len(ov.ClusterValues) > 0 {
			cluster = make(map[string]map[string]any, len(ov.ClusterValues))
			for k, v := range ov.ClusterValues {
				cluster[k] = helmvalues.DeepCopyMap(v)
			}
		}
	}
	return def, env, cluster
}

// setComponentPlatformOverlays threads the PE-authored value overlays for EACH
// composed component's OWN template (+ its org override) onto the pub env, so the
// publisher merges them beneath each component's Values. It also DISCOVERS each
// component's CD images from that same effective-values context (chart defaults ⊕
// template/org overlays ⊕ the component's Values overlay) and resolves the
// component's ComponentSpec.Images selection against them — so a component image is
// auto-identified from values, mirroring the single-source resolveCDImages. No-op
// for single-source apps (their primary template's overlays are set by
// setPlatformOverlays).
func (a *gitOpsPublisherAdapter) setComponentPlatformOverlays(ctx context.Context, pub *gitops.AppPublishEnv, app *domain.App, envName string) {
	if !app.Spec.IsComposed() {
		return
	}
	m := make(map[string]gitops.ComponentPlatformValues, len(app.Spec.Components))
	imgs := make(map[string][]gitops.KargoImage, len(app.Spec.Components))
	for _, c := range app.Spec.Components {
		if c.Template == nil {
			continue
		}
		tmpl, err := a.resolveTemplate(ctx, c.Template.Name)
		if err != nil || tmpl == nil {
			continue
		}
		ov := a.loadOverride(ctx, c.Template.Name)
		def, env, cluster := computePlatformOverlays(tmpl, ov, envName)
		// Component template preview defaults (spec ⊕ org override), threaded so a
		// composed preview applies each component's own template preview overlay.
		preview := helmvalues.DeepCopyMap(tmpl.Spec.PreviewDefaultValues)
		if ov != nil && len(ov.PreviewDefaultValues) > 0 {
			preview = helmvalues.DeepMerge(preview, helmvalues.DeepCopyMap(ov.PreviewDefaultValues))
		}
		m[c.Name] = gitops.ComponentPlatformValues{Default: def, Env: env, Cluster: cluster, Preview: preview}

		// The component's per-env values overlay — an image repository is commonly
		// set here (a BYO chart whose template declares no slot and whose base leaves
		// image.repository empty), so it must feed discovery or that image is never
		// wired to Kargo.
		envOverlay := app.Spec.EnvironmentDefaults[envName].ComponentValues[c.Name]
		// Skip discovery only when there is genuinely nothing that could carry an
		// image: no bindings, no template-declared slots, AND no component values
		// (base or per-env) that could set image.repository. Skipping on the first two
		// alone dropped value-defined images (e.g. a `web` component pointing image at
		// its own repo via its per-env values).
		if len(c.Images) == 0 && len(server.EffectiveTemplateImageSlots(tmpl, ov)) == 0 &&
			len(c.Values) == 0 && len(envOverlay) == 0 {
			continue
		}
		discovered := server.DiscoverComponentImages(ctx, a.kubeClient, tmpl, ov, envName, c.Values, envOverlay)
		var resolved []gitops.KargoImage
		if len(c.Images) == 0 {
			// No explicit selection: inherit the template-DECLARED images (auto-bind).
			resolved = gitops.SelectDeclaredKargoImages(discovered)
		} else {
			resolved = gitops.SelectKargoImages(discovered, componentImageBindings(c.Images))
		}
		// SelectKargoImages names each image after the DISCOVERED slot; re-stamp the
		// owning component so the promotion targets that component's values file.
		for i := range resolved {
			resolved[i].Name = c.Name
		}
		if len(resolved) > 0 {
			imgs[c.Name] = resolved
		}
	}
	pub.ComponentPlatformValues = m
	pub.ComponentTemplateImages = imgs
}

// componentImageBindings adapts a component's image SELECTIONS to the shared
// AppImageBinding shape SelectKargoImages consumes (both are tag-key-keyed
// selections; the repository is discovered, not carried here).
func componentImageBindings(images []domain.ComponentImage) []domain.AppImageBinding {
	out := make([]domain.AppImageBinding, len(images))
	for i, img := range images {
		out[i] = domain.AppImageBinding{
			Name:              img.Name,
			TagKey:            img.TagKey,
			TagPattern:        img.TagPattern,
			SelectionStrategy: img.SelectionStrategy,
		}
	}
	return out
}

// orgNameOf returns the org's name, or "" when the org is unavailable.
func orgNameOf(org *rbac.Org) string {
	if org == nil {
		return ""
	}
	return org.Name
}

// resolveCDImages resolves the app's CD-selected images for one environment to the
// publisher's KargoImage form. Images are DISCOVERED from the app+env's effective
// Helm values (so each repository reflects the values, never a stale snapshot),
// then filtered to the user's selection (app.Spec.Images) by tag key. A selection
// whose image no longer appears in the values is skipped with a warning. Empty
// selection → nil, letting the publisher fall back to the legacy single image.
func (a *gitOpsPublisherAdapter) resolveCDImages(ctx context.Context, tmpl *tpl.Template, ov *domain.TemplateOverride, app *domain.App, env *domain.AppEnvironment, orgName string) []gitops.KargoImage {
	// Skip discovery entirely when there's no explicit selection AND the template
	// declares no image slots — nothing to watch (resolveKargoImages still applies
	// the legacy image_repository fallback downstream).
	if len(app.Spec.Images) == 0 && len(server.EffectiveTemplateImageSlots(tmpl, ov)) == 0 {
		return nil
	}
	var envRaw map[string]any
	if ovr, ok := app.Spec.EnvironmentDefaults[env.EnvName]; ok {
		envRaw = ovr.RawValues
	}
	discovered := server.DiscoverAppImages(ctx, a.kubeClient, tmpl, ov, app, env.EnvName, env.EnvType, env.Namespace, orgName, app.Spec.RawValues, envRaw)
	if len(app.Spec.Images) == 0 {
		// No explicit selection: inherit the template-DECLARED images (auto-bind) so
		// the Warehouse is healthy with zero config.
		return gitops.SelectDeclaredKargoImages(discovered)
	}
	return gitops.SelectKargoImages(discovered, app.Spec.Images)
}

// setStackOverlays populates the app's stack shared Helm-values overlay onto the
// publish env (StackRawValues for all envs + StackEnvRawValues for this env),
// layered below the developer RawValues. No-op when the app isn't in a stack or
// the stack has no values. Best-effort — a load error degrades to no stack layer.
func (a *gitOpsPublisherAdapter) setStackOverlays(ctx context.Context, pub *gitops.AppPublishEnv, app *domain.App, envName string) {
	if app.Spec.Stack == "" || a.stackStore == nil {
		return
	}
	st, err := a.stackStore.GetStack(ctx, app.ProjectName, app.Spec.Stack)
	if err != nil || st == nil {
		return
	}
	pub.StackRawValues = st.Spec.RawValues
	if ev, ok := st.Spec.EnvRawValues[envName]; ok {
		pub.StackEnvRawValues = ev
	}
	// A stack-level auto-promote opts in every member app (the effective value is
	// the app's own setting ORed with the stack's).
	if st.Spec.AutoPromote != nil && *st.Spec.AutoPromote {
		pub.AutoPromote = true
	}
}

// loadOverride best-effort reads a template's org-level platform override.
// Returns nil on no loader or any error — the override is additive/optional and
// must never fail a publish (unlike template resolution, which fails loud).
func (a *gitOpsPublisherAdapter) loadOverride(ctx context.Context, name string) *domain.TemplateOverride {
	if a.overrideLoader == nil {
		return nil
	}
	ov, err := a.overrideLoader(ctx, name)
	if err != nil {
		slog.Warn("publish: load template override failed; proceeding without org layer", "template", name, "err", err)
		return nil
	}
	return ov
}

// envResolved holds the resolved cluster and domain info for one environment.
type envResolved struct {
	clusterServer    string
	baseDomain       string
	namespacePattern string
	// clusters is the env's DEFAULT deploy-target set from DeployMode (one entry
	// in "active" mode, every bound cluster in "all" mode). Used as the fallback
	// when an app makes no per-env cluster selection.
	clusters []gitops.ClusterTarget
	// allClusters is every bound cluster the env is allowed to use (all
	// ClusterRefs resolved), regardless of DeployMode — the superset a per-app
	// TargetClusters selection picks from, and the AppProject destination set.
	allClusters []gitops.ClusterTarget
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

		// Resolve the cluster API server from the cluster store. The active
		// cluster drives the single-cluster destination + the per-cluster status
		// lookup; ResolveDeployTargets() yields the full fan-out set ("active" →
		// just the active cluster, "all" → every bound clusterRef).
		activeRef := orgEnv.EffectiveClusterRef()
		if activeRef != "" && a.clusterStore != nil {
			cluster, err := a.clusterStore.GetCluster(ctx, activeRef)
			if err != nil {
				slog.Warn("resolveEnvs: cluster not found in registry, falling back to in-cluster default",
					"env", orgEnv.Name, "clusterRef", activeRef, "err", err)
			} else if cluster.APIServer == "" {
				slog.Warn("resolveEnvs: cluster has empty apiServer, falling back to in-cluster default",
					"env", orgEnv.Name, "clusterRef", activeRef)
				res.bound = true // ClusterRef is valid even if apiServer defaults
			} else {
				res.clusterServer = cluster.APIServer
				res.bound = true
			}
		}

		// Resolve every deploy target to {name, APIServer} for fan-out. Skip
		// clusters that aren't registered or lack an API server.
		if a.clusterStore != nil {
			for _, ref := range orgEnv.ResolveDeployTargets() {
				cluster, err := a.clusterStore.GetCluster(ctx, ref)
				if err != nil || cluster == nil || cluster.APIServer == "" {
					slog.Warn("resolveEnvs: skipping unresolved deploy target",
						"env", orgEnv.Name, "clusterRef", ref)
					continue
				}
				res.clusters = append(res.clusters, gitops.ClusterTarget{
					Name:            ref,
					Server:          cluster.APIServer,
					BaseDomain:      cluster.BaseDomain,
					RoutingProfiles: cluster.RoutingProfiles,
				})
			}

			// allClusters resolves EVERY ClusterRef the env is allowed to use
			// (independent of DeployMode), so a per-app TargetClusters selection can
			// pick any subset — and the AppProject authorizes them all.
			for _, ref := range orgEnv.ClusterRefs {
				cluster, err := a.clusterStore.GetCluster(ctx, ref)
				if err != nil || cluster == nil || cluster.APIServer == "" {
					continue
				}
				res.allClusters = append(res.allClusters, gitops.ClusterTarget{
					Name:            ref,
					Server:          cluster.APIServer,
					BaseDomain:      cluster.BaseDomain,
					RoutingProfiles: cluster.RoutingProfiles,
				})
			}
		}

		result[orgEnv.Name] = res
	}

	return result
}

// appClusterTargets resolves an app's deploy-target clusters for one env: its
// per-env TargetClusters selection against the env's DeployMode default
// (res.clusters) and full set (res.allClusters). Empty selection inherits the
// env default; ["*"] = all; else a subset. Falls back to the env default when
// the env has no resolvable full set (unbound/edge).
func (a *gitOpsPublisherAdapter) appClusterTargets(app *domain.App, envName string, res envResolved) []gitops.ClusterTarget {
	if len(res.allClusters) == 0 {
		return res.clusters
	}
	names := domain.ResolveAppClusterTargets(
		app.Spec.EnvironmentDefaults[envName].TargetClusters,
		clusterTargetNames(res.clusters),
		clusterTargetNames(res.allClusters),
	)
	if sel := selectClusterTargets(res.allClusters, names); len(sel) > 0 {
		return sel
	}
	return res.clusters
}

// clusterTargetNames extracts the cluster names from a ClusterTarget slice,
// preserving order.
func clusterTargetNames(targets []gitops.ClusterTarget) []string {
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.Name
	}
	return names
}

// selectClusterTargets returns the ClusterTargets from pool whose names are in
// want, in want's order. Names not in pool are skipped.
func selectClusterTargets(pool []gitops.ClusterTarget, want []string) []gitops.ClusterTarget {
	byName := make(map[string]gitops.ClusterTarget, len(pool))
	for _, t := range pool {
		byName[t.Name] = t
	}
	out := make([]gitops.ClusterTarget, 0, len(want))
	for _, n := range want {
		if t, ok := byName[n]; ok {
			out = append(out, t)
		}
	}
	return out
}

// kargoPromoterAdapter bridges kube.KargoStore (which returns *kube.KargoPromotionInfo)
// to the server.KargoPromoter interface (which returns server.KargoPromotionResult).
type kargoPromoterAdapter struct {
	store *kube.KargoStore
}

func (a *kargoPromoterAdapter) CreatePromotion(ctx context.Context, projectName, appName, fromStage, toStage string) (server.KargoPromotionResult, error) {
	info, err := a.store.CreatePromotion(ctx, projectName, appName, fromStage, toStage)
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

// CurrentFreightImageTag / LatestFreightImageTag expose the store methods so the
// app handler can restore a stable env to its real Kargo image when unpinning a
// preview (current stage freight, else the latest the Warehouse discovered).
func (a *kargoPromoterAdapter) CurrentFreightImageTag(ctx context.Context, projectName, appName, env, repoSubstr string) (string, error) {
	return a.store.CurrentFreightImageTag(ctx, projectName, appName, env, repoSubstr)
}

func (a *kargoPromoterAdapter) LatestFreightImageTag(ctx context.Context, projectName, appName, repoSubstr string) (string, error) {
	return a.store.LatestFreightImageTag(ctx, projectName, appName, repoSubstr)
}

// GetPromotionStatus implements server.KargoStatusReader. The promotion lives in
// the project's kargo-{project} namespace, so the project name is required to
// resolve it.
func (a *kargoPromoterAdapter) GetPromotionStatus(ctx context.Context, projectName, promotionName string) (server.KargoPromotionResult, error) {
	info, err := a.store.GetPromotionStatus(ctx, projectName, promotionName)
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
// The store scopes the query to the project's kargo-{project} namespace and the
// app label; here we strip the "{app}-" name prefix to recover the env name.
func (a *kargoPromoterAdapter) ListAppStageStatuses(ctx context.Context, projectName, appName string) ([]server.KargoStageStatusResult, error) {
	all, err := a.store.ListAppStageStatuses(ctx, projectName, appName)
	if err != nil {
		return nil, err
	}

	prefix := appName + "-"
	var results []server.KargoStageStatusResult
	for stageName, s := range all {
		envName := strings.TrimPrefix(stageName, prefix)
		results = append(results, server.KargoStageStatusResult{
			StageName:             stageName,
			EnvName:               envName,
			Phase:                 s.Phase,
			Health:                s.Health,
			CurrentFreight:        s.CurrentFreight,
			AvailableFreightCount: s.AvailableFreightCount,
		})
	}

	slog.Debug("kargo pipeline adapter: stages for app",
		"namespace", gitops.KargoNamespaceForProject(projectName),
		"project", projectName,
		"app", appName,
		"appStages", len(results),
	)

	// Order by envName for deterministic UI rendering: staging before prod.
	sort.Slice(results, func(i, j int) bool {
		return results[i].EnvName < results[j].EnvName
	})
	return results, nil
}

func (a *gitOpsPublisherAdapter) PublishApp(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) error {
	pubEnvs, appSetEnvs, err := a.buildAppBundle(ctx, app, envs)
	if err != nil {
		return err
	}
	// Write appset.yaml + appproject.yaml for each bound environment so ArgoCD
	// can discover apps through its Git File generator. This is idempotent.
	if err := a.inner.PublishEnvInfra(ctx, app.ProjectName, appSetEnvs); err != nil {
		return fmt.Errorf("publish env infra: %w", err)
	}
	// Write app.yaml + values.yaml for each bound environment.
	if err := a.inner.PublishApp(ctx, app, pubEnvs); err != nil {
		return err
	}
	a.refreshApps(ctx, app.ProjectName, app.Name)
	return nil
}

// buildAppBundle resolves an app's per-env AppPublishEnv slice (values overlays,
// secrets, CD images, suspend key) and the AppSet envs for its infra. Shared by
// PublishApp and the batched PublishApps so both produce identical output.
func (a *gitOpsPublisherAdapter) buildAppBundle(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) ([]gitops.AppPublishEnv, []gitops.AppSetEnv, error) {
	resolved := a.resolveEnvs(ctx)

	// Resolve the app's template ONCE (the name is fixed across envs; only the
	// per-env overlay slice differs). Fail loud on a cluster-fetch error rather
	// than publishing values.yaml without the PE platform/env overlays.
	tmpl, err := a.resolveTemplate(ctx, app.Spec.Template.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("publish app %s/%s: %w", app.ProjectName, app.Name, err)
	}
	// Org-level platform override (best-effort; additive). Loaded once.
	ov := a.loadOverride(ctx, app.Spec.Template.Name)

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

		// Resolve this app's target clusters for the env (per-env selection vs.
		// the env's DeployMode default / full set).
		appTargets := a.appClusterTargets(app, env.EnvName, res)

		// Only include bound environments in the ApplicationSet so ArgoCD
		// doesn't try to connect to a cluster that hasn't been registered. The
		// AppSet/AppProject infra spans the env's FULL cluster set (allClusters)
		// since it is shared by every app in the env; per-app targeting is
		// expressed by which per-cluster app.yaml files each app writes.
		if res.bound {
			infraClusters := res.allClusters
			if len(infraClusters) == 0 {
				infraClusters = res.clusters
			}
			appSetEnvs = append(appSetEnvs, gitops.AppSetEnv{
				EnvName:       env.EnvName,
				ClusterServer: res.clusterServer,
				Clusters:      infraClusters,
				BaseDomain:    res.baseDomain,
			})
		}

		pub := gitops.AppPublishEnv{
			EnvName:         env.EnvName,
			EnvType:         env.EnvType,
			Order:           env.Order,
			Bound:           res.bound,
			BaseDomain:      res.baseDomain,
			Namespace:       env.Namespace,
			RoutingProfiles: lookupOrgEnvRoutingProfiles(org, env.EnvName),
			Clusters:        appTargets,
			AutoPromote:     app.Spec.CD.AutoPromote,
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
		setPlatformOverlays(&pub, tmpl, ov, env.EnvName)
		a.setComponentPlatformOverlays(ctx, &pub, app, env.EnvName)
		pub.TemplateImages = a.resolveCDImages(ctx, tmpl, ov, app, env, orgNameOf(org))
		if tmpl != nil {
			// Suspend is read per-env from the app spec by the publisher, but the
			// values KEY comes from the template — thread it on EVERY publish path
			// (not just PublishAppEnv) so a suspended env stays suspended across
			// full-app republishes (config edits, sync, promote, stack sync).
			pub.SuspendKey = tmpl.Spec.SuspendKey()
		}
		a.setStackOverlays(ctx, &pub, app, env.EnvName)

		pubEnvs = append(pubEnvs, pub)
	}
	return pubEnvs, appSetEnvs, nil
}

// PublishApps implements server.BatchAppPublisher — the batched form of
// republishApp + PublishAppEnv(focus): it resolves every target's bundle then
// writes them all in one clone/commit/push, so a stack pin/unpin fan-out is one
// git round-trip instead of ~3 per member.
func (a *gitOpsPublisherAdapter) PublishApps(ctx context.Context, targets []server.AppPublishTarget) error {
	// Build bundles through a request-scoped cached vault so the shared-scope
	// (project/stack) 1Password lookups run once for the whole fan-out, not once
	// per member — the fix for stack pin/unpin 504s.
	ca := a.withCachedVault()
	bundles := make([]gitops.AppPublishBundle, 0, len(targets))
	for _, t := range targets {
		pubEnvs, appSetEnvs, err := ca.buildAppBundle(ctx, t.App, t.Envs)
		if err != nil {
			return err
		}
		b := gitops.AppPublishBundle{App: t.App, Envs: pubEnvs, AppSetEnvs: appSetEnvs}
		for _, fe := range t.FocusEnvs {
			pe, err := ca.buildAppEnvPub(ctx, t.App, fe)
			if err != nil {
				return err
			}
			b.FocusEnvs = append(b.FocusEnvs, pe)
		}
		bundles = append(bundles, b)
	}
	if err := a.inner.PublishApps(ctx, bundles); err != nil {
		return err
	}
	byProject := map[string][]string{}
	for _, t := range targets {
		byProject[t.App.ProjectName] = append(byProject[t.App.ProjectName], t.App.Name)
	}
	for proj, names := range byProject {
		a.refreshApps(ctx, proj, names...)
	}
	return nil
}

// lookupOrgEnvRoutingProfiles returns the per-env RoutingProfiles override
// map for envName, or nil when the org or env isn't found. The publisher
// passes this to the helmvalues mapper so per-env entries replace org-level
// profiles by name; absent names inherit the org default.
func lookupOrgEnvRoutingProfiles(org *rbac.Org, envName string) domain.RoutingProfiles {
	if org == nil {
		return nil
	}
	for _, e := range org.Environments {
		if e.Name == envName {
			return e.RoutingProfiles
		}
	}
	return nil
}

// enrichPubEnvWithSecrets sets ClusterRef and ScopeKeys on pub. ScopeKeys
// reports which (scope, tier) items have keys so the publisher only emits
// ExternalSecrets that resolve — ESO errors trying to extract a missing item.
// cachedVault memoizes the idempotent read/ensure vault operations for the
// duration of one publish batch. A stack fan-out enriches secrets per member and
// per env, but the project- and stack-scope 1Password lookups are IDENTICAL
// across members — without caching they run once per member (dozens of slow
// 1Password round-trips), which was timing publishes out at the gateway (504).
type cachedVault struct {
	secrets.VaultStore
	mu      sync.Mutex
	ensured map[string]error
	listed  map[string]cachedList
}

type cachedList struct {
	entries []secrets.SecretEntry
	err     error
}

func newCachedVault(v secrets.VaultStore) *cachedVault {
	return &cachedVault{VaultStore: v, ensured: map[string]error{}, listed: map[string]cachedList{}}
}

func vaultKey(scope secrets.Scope, tier secrets.Tier, app string) string {
	return fmt.Sprintf("%+v|%s|%s", scope, tier, app)
}

func (c *cachedVault) EnsureItem(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) error {
	key := vaultKey(scope, tier, app)
	c.mu.Lock()
	if err, ok := c.ensured[key]; ok {
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()
	err := c.VaultStore.EnsureItem(ctx, scope, tier, app)
	c.mu.Lock()
	c.ensured[key] = err
	c.mu.Unlock()
	return err
}

func (c *cachedVault) ListKeys(ctx context.Context, scope secrets.Scope, tier secrets.Tier, app string) ([]secrets.SecretEntry, error) {
	key := vaultKey(scope, tier, app)
	c.mu.Lock()
	if r, ok := c.listed[key]; ok {
		c.mu.Unlock()
		return r.entries, r.err
	}
	c.mu.Unlock()
	entries, err := c.VaultStore.ListKeys(ctx, scope, tier, app)
	c.mu.Lock()
	c.listed[key] = cachedList{entries: entries, err: err}
	c.mu.Unlock()
	return entries, err
}

// withCachedVault returns a shallow copy of the adapter whose vault is a
// request-scoped cache — so enrich/collectScopeKeys (which use the receiver's
// a.vault) dedupe the shared-scope 1Password lookups across a fan-out. The copy
// is local to one call, so it never races other requests.
func (a *gitOpsPublisherAdapter) withCachedVault() *gitOpsPublisherAdapter {
	if a.vault == nil {
		return a
	}
	cp := *a
	cp.vault = newCachedVault(a.vault)
	return &cp
}

func (a *gitOpsPublisherAdapter) enrichPubEnvWithSecrets(ctx context.Context, org *rbac.Org, app *domain.App, envName string, pub *gitops.AppPublishEnv) {
	// Resolve the cluster bound to this env from the org config so the
	// publisher can emit the cluster-scope ExternalSecret.
	for _, e := range org.Environments {
		if e.Name == envName {
			pub.ClusterRef = e.EffectiveClusterRef()
			break
		}
	}

	pub.ScopeKeys = a.collectScopeKeys(ctx, app, envName, pub.ClusterRef)

	// A component may curate a SUBSET of the app's secret keys (renamed) into its
	// own <app>-<component>-secrets — a data[] projection that needs the actual key
	// names per scope to resolve each selection. Collect them only then; the common
	// case (whole-item dataFrom) needs no names, so skip the extra vault lists.
	if app.Spec.CuratesSecrets() {
		pub.ScopeSecretKeys = a.collectScopeSecretKeys(ctx, app, envName, pub.ClusterRef)
	}

	// Guarantee every app gets a <app>-secrets ExternalSecret (and thus a K8s
	// Secret) even with no secrets configured: the chart's envFrom references it
	// unconditionally, so without this the pod fails to resolve the ref. The
	// global-app item is the env-agnostic home for an app's own secrets — ensure
	// it exists (empty) in the backend and always include it in the merged
	// ExternalSecret. Only when a vault is configured: without a backend there is
	// nothing for ESO to extract, and forcing the ref would dangle.
	if a.vault != nil {
		if err := a.vault.EnsureItem(ctx, secrets.GlobalScope().WithProject(app.ProjectName), secrets.TierApp, app.Name); err != nil {
			slog.Warn("gitops: ensuring baseline app secret item",
				"app", app.Name, "err", err)
		} else {
			pub.ScopeKeys.GlobalApp = true
		}

		// The per-env app item is the home for this app's env-specific secrets.
		// Ensure it exists (empty) in the target env's vault and always reference
		// it, so promoting/syncing an app makes the env vault ready to receive
		// values without a manual "add a secret to create the item" step — and a
		// later value is picked up by ESO's refresh without re-publishing. Skipped
		// gracefully when the env vault isn't provisioned (EnsureItem errors →
		// presence left as collectScopeKeys probed it).
		if err := a.vault.EnsureItem(ctx, secrets.EnvScope(envName).WithProject(app.ProjectName), secrets.TierApp, app.Name); err != nil {
			slog.Warn("gitops: ensuring env-app secret item",
				"app", app.Name, "env", envName, "err", err)
		} else {
			pub.ScopeKeys.EnvApp = true
		}

		// Org-level env-shared secrets apply to every app in this environment.
		// Ensure the item exists (empty) and always reference it — the same
		// ensure-and-always-reference contract as the project-env / stack-env
		// shared items below — so an org-admin setting an env-shared secret is
		// picked up by ESO's refresh without re-publishing every app in the env.
		if err := a.vault.EnsureItem(ctx, secrets.EnvScope(envName), secrets.TierShared, ""); err != nil {
			slog.Warn("gitops: ensuring env-shared secret item",
				"env", envName, "err", err)
		} else {
			pub.ScopeKeys.EnvShared = true
		}

		// Project-scope shared secrets apply to every app in the project. Like
		// the baseline app item, ensure the project items exist (empty) and
		// always reference them, so setting a project secret is picked up by
		// ESO's refresh without re-publishing every app's ExternalSecret. The
		// project-global item lives in the global vault; project-env in the env
		// vault (skipped gracefully when that env vault isn't provisioned).
		if app.ProjectName != "" {
			if err := a.vault.EnsureItem(ctx, secrets.ProjectScope(app.ProjectName), secrets.TierShared, ""); err != nil {
				slog.Warn("gitops: ensuring project-global secret item",
					"project", app.ProjectName, "err", err)
			} else {
				pub.ScopeKeys.ProjectShared = true
			}
			if err := a.vault.EnsureItem(ctx, secrets.ProjectEnvScope(app.ProjectName, envName), secrets.TierShared, ""); err != nil {
				slog.Warn("gitops: ensuring project-env secret item",
					"project", app.ProjectName, "env", envName, "err", err)
			} else {
				pub.ScopeKeys.ProjectEnvShared = true
			}
		}

		// Stack-scope shared secrets apply to every app in the stack (same
		// ensure-and-always-reference pattern as project secrets).
		if app.Spec.Stack != "" && app.ProjectName != "" {
			if err := a.vault.EnsureItem(ctx, secrets.StackScope(app.ProjectName, app.Spec.Stack), secrets.TierShared, ""); err != nil {
				slog.Warn("gitops: ensuring stack-global secret item",
					"project", app.ProjectName, "stack", app.Spec.Stack, "err", err)
			} else {
				pub.ScopeKeys.StackShared = true
			}
			if err := a.vault.EnsureItem(ctx, secrets.StackEnvScope(app.ProjectName, app.Spec.Stack, envName), secrets.TierShared, ""); err != nil {
				slog.Warn("gitops: ensuring stack-env secret item",
					"project", app.ProjectName, "stack", app.Spec.Stack, "env", envName, "err", err)
			} else {
				pub.ScopeKeys.StackEnvShared = true
			}
		}
	}
}

// ReconcileSecretStores implements server.SecretStoreReconciler. It computes the
// full desired set of ESO ClusterSecretStores (global + one per environment +
// one per cluster) from current org + cluster state and publishes them to the
// gitops repo, pruning any that no longer belong.
func (a *gitOpsPublisherAdapter) ReconcileSecretStores(ctx context.Context) error {
	if a.inner == nil || a.orgProvider == nil {
		return nil
	}
	org, err := a.orgProvider.GetOrg(ctx)
	if err != nil {
		return err
	}
	if org == nil {
		return nil
	}

	envNames := make([]string, 0, len(org.Environments))
	for _, e := range org.Environments {
		envNames = append(envNames, e.Name)
	}

	stores := gitops.BuildSecretStoresForConfig(org.SecretBackend, envNames, org.Branding)
	return a.inner.PublishSecretStores(ctx, stores)
}

// collectScopeKeys probes each scope level and reports which ones currently
// have at least one key in the vault. Errors are swallowed (treated as "no
// keys") because the resolved view is best-effort during a publish — partial
// information is better than failing the entire commit.
func (a *gitOpsPublisherAdapter) collectScopeKeys(ctx context.Context, app *domain.App, envName, clusterRef string) gitops.ScopePresence {
	var p gitops.ScopePresence
	if a.vault == nil {
		return p
	}
	has := func(scope secrets.Scope, tier secrets.Tier, appName string) bool {
		entries, err := a.vault.ListKeys(ctx, scope, tier, appName)
		return err == nil && len(entries) > 0
	}

	// App-tier probes tag the scope with the app's project so the item name
	// matches what the publisher/vault write path uses (project-qualified);
	// shared-tier probes stay project-less (org/env-wide items).
	g := secrets.GlobalScope()
	p.GlobalShared = has(g, secrets.TierShared, "")
	p.GlobalApp = has(g.WithProject(app.ProjectName), secrets.TierApp, app.Name)

	e := secrets.EnvScope(envName)
	p.EnvShared = has(e, secrets.TierShared, "")
	p.EnvApp = has(e.WithProject(app.ProjectName), secrets.TierApp, app.Name)

	if clusterRef != "" {
		c := secrets.ClusterScope(envName, clusterRef)
		p.ClusterShared = has(c, secrets.TierShared, "")
		p.ClusterApp = has(c.WithProject(app.ProjectName), secrets.TierApp, app.Name)
	}
	return p
}

// collectScopeSecretKeys lists the actual secret KEY NAMES per (scope, tier) so
// the per-component data[] projection can resolve a selected key to the item that
// holds it (highest-precedence wins). Only called when the app curates secrets —
// the app-wide secret uses whole-item dataFrom and needs no names, so paying for
// these extra vault lists on every publish would be wasteful. Errors are swallowed
// (best-effort, same contract as collectScopeKeys); the ordering must mirror
// buildScopeItems so presence and key-name gating stay in sync.
func (a *gitOpsPublisherAdapter) collectScopeSecretKeys(ctx context.Context, app *domain.App, envName, clusterRef string) gitops.ScopeSecretKeys {
	var sk gitops.ScopeSecretKeys
	if a.vault == nil {
		return sk
	}
	keys := func(scope secrets.Scope, tier secrets.Tier, appName string) []string {
		entries, err := a.vault.ListKeys(ctx, scope, tier, appName)
		if err != nil || len(entries) == 0 {
			return nil
		}
		out := make([]string, 0, len(entries))
		for _, e := range entries {
			out = append(out, e.Key)
		}
		return out
	}

	g := secrets.GlobalScope()
	sk.GlobalShared = keys(g, secrets.TierShared, "")
	sk.GlobalApp = keys(g.WithProject(app.ProjectName), secrets.TierApp, app.Name)

	e := secrets.EnvScope(envName)
	sk.EnvShared = keys(e, secrets.TierShared, "")
	sk.EnvApp = keys(e.WithProject(app.ProjectName), secrets.TierApp, app.Name)

	if clusterRef != "" {
		c := secrets.ClusterScope(envName, clusterRef)
		sk.ClusterShared = keys(c, secrets.TierShared, "")
		sk.ClusterApp = keys(c.WithProject(app.ProjectName), secrets.TierApp, app.Name)
	}

	if app.ProjectName != "" {
		sk.ProjectShared = keys(secrets.ProjectScope(app.ProjectName), secrets.TierShared, "")
		sk.ProjectEnvShared = keys(secrets.ProjectEnvScope(app.ProjectName, envName), secrets.TierShared, "")
	}

	if app.Spec.Stack != "" && app.ProjectName != "" {
		sk.StackShared = keys(secrets.StackScope(app.ProjectName, app.Spec.Stack), secrets.TierShared, "")
		sk.StackEnvShared = keys(secrets.StackEnvScope(app.ProjectName, app.Spec.Stack, envName), secrets.TierShared, "")
	}
	return sk
}

// mergeAllEnvVars resolves env vars across the six-scope hierarchy and returns
// the flattened map the publisher writes into gitops-output as the per-app
// ConfigMap. Precedence is low-to-high — later scopes overwrite earlier keys:
//
//  1. org           (org.EnvConfig.Vars)
//  2. env-type      (org.Environments[envName].EnvConfig.Vars)
//  3. project       (project.Spec.EnvConfig.Vars)
//     3b. project-env   (project.Spec.EnvConfigByEnv[env].Vars)
//  4. app           (app.Spec.EnvConfig.Vars)
//  5. app-env       (app.Spec.EnvironmentDefaults[envName].EnvConfig.Vars)
//  6. cluster       (suparship-envvars-cluster-{name} ConfigMap data)
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
			// Project per-environment overrides win over project-global.
			for k, v := range proj.Spec.EnvConfigByEnv[envName].Vars {
				merged[k] = v
			}
		}
	}

	// Stack layer (between project and app): a stack's shared env vars apply to
	// every member app, overriding project and overridden by the app.
	if app.Spec.Stack != "" && a.stackStore != nil {
		if st, err := a.stackStore.GetStack(ctx, app.ProjectName, app.Spec.Stack); err == nil && st != nil {
			for k, v := range st.Spec.EnvConfig.Vars {
				merged[k] = v
			}
			// Stack per-environment overrides win over stack-global.
			for k, v := range st.Spec.EnvConfigByEnv[envName].Vars {
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
		// Cluster-global (every env on this cluster) then cluster-env (this env
		// only) — the per-env set is the most specific, so it wins over both
		// cluster-global and every lower scope.
		if cfg, err := a.envConfigReader.ReadClusterEnvConfig(ctx, clusterRef); err == nil {
			for k, v := range cfg.Vars {
				merged[k] = v
			}
		}
		if envName != "" {
			if cfg, err := a.envConfigReader.ReadClusterEnvScopedConfig(ctx, clusterRef, envName); err == nil {
				for k, v := range cfg.Vars {
					merged[k] = v
				}
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
	pub, err := a.buildAppEnvPub(ctx, app, env)
	if err != nil {
		return err
	}
	if err := a.inner.PublishAppEnv(ctx, app, pub); err != nil {
		return err
	}
	a.refreshApps(ctx, app.ProjectName, app.Name)
	return nil
}

// buildAppEnvPub resolves one env's full AppPublishEnv (cluster, secrets, env
// vars, platform/stack overlays, CD images, suspend key). Shared by PublishAppEnv
// and the batched PublishAppsEnv so both write identical values.yaml.
func (a *gitOpsPublisherAdapter) buildAppEnvPub(ctx context.Context, app *domain.App, env *domain.AppEnvironment) (gitops.AppPublishEnv, error) {
	// Resolve the template up front; fail loud on a cluster-fetch error so a
	// promote never writes values.yaml missing the platform/env overlays.
	tmpl, err := a.resolveTemplate(ctx, app.Spec.Template.Name)
	if err != nil {
		return gitops.AppPublishEnv{}, fmt.Errorf("publish app env %s/%s/%s: %w", app.ProjectName, app.Name, env.EnvName, err)
	}
	ov := a.loadOverride(ctx, app.Spec.Template.Name)

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
		Clusters:   a.appClusterTargets(app, env.EnvName, res),
	}

	var org *rbac.Org
	if a.orgProvider != nil {
		org, _ = a.orgProvider.GetOrg(ctx)
	}
	pub.RoutingProfiles = lookupOrgEnvRoutingProfiles(org, env.EnvName)
	if org != nil {
		a.enrichPubEnvWithSecrets(ctx, org, app, env.EnvName, &pub)
	}
	pub.EnvVars = a.mergeAllEnvVars(ctx, app, env.EnvName, pub.ClusterRef, org)
	setPlatformOverlays(&pub, tmpl, ov, env.EnvName)
	a.setComponentPlatformOverlays(ctx, &pub, app, env.EnvName)
	pub.TemplateImages = a.resolveCDImages(ctx, tmpl, ov, app, env, orgNameOf(org))
	if tmpl != nil {
		pub.SuspendKey = tmpl.Spec.SuspendKey()
	}
	a.setStackOverlays(ctx, &pub, app, env.EnvName)
	return pub, nil
}

// PublishAppsEnv implements server.GitOpsPublisher — the batched form of
// PublishAppEnv: it resolves each (app, env) target then writes them all in a
// single git commit, so a stack fan-out is one clone/commit/push instead of one
// per member.
func (a *gitOpsPublisherAdapter) PublishAppsEnv(ctx context.Context, targets []server.AppEnvTarget) error {
	// Cached vault: dedupe shared-scope 1Password lookups across members.
	ca := a.withCachedVault()
	items := make([]gitops.AppEnvPublish, 0, len(targets))
	for _, t := range targets {
		pub, err := ca.buildAppEnvPub(ctx, t.App, t.Env)
		if err != nil {
			return err
		}
		items = append(items, gitops.AppEnvPublish{App: t.App, Env: pub})
	}
	if err := a.inner.PublishAppsEnv(ctx, items); err != nil {
		return err
	}
	a.refreshTargets(ctx, targets)
	return nil
}

// refreshTargets triggers an ArgoCD refresh for every target's app, grouped by
// project (a stack fan-out is one project, but group defensively).
func (a *gitOpsPublisherAdapter) refreshTargets(ctx context.Context, targets []server.AppEnvTarget) {
	byProject := map[string][]string{}
	for _, t := range targets {
		byProject[t.App.ProjectName] = append(byProject[t.App.ProjectName], t.App.Name)
	}
	for proj, names := range byProject {
		a.refreshApps(ctx, proj, names...)
	}
}

// PublishAppPreview implements server.GitOpsPublisher for previews. A preview
// reuses baseEnv's cluster, per-env config and vault: its env vars are the base
// env's merged vars with the reserved "preview" band overlaid, and its
// ExternalSecret reads the base env vault's preview-band (+ per-PR) items on top
// of the base env's own items. No per-preview vault or store is created.
func (a *gitOpsPublisherAdapter) PublishAppPreview(ctx context.Context, app *domain.App, preview *domain.EnvironmentInstance, baseEnv, imageTag string) error {
	spec, err := a.buildPreviewSpec(ctx, app, preview, baseEnv, imageTag)
	if err != nil {
		return err
	}
	if err := a.inner.PublishPreview(ctx, app, spec); err != nil {
		return err
	}
	// Refresh the app's existing preview Application (re-publish case), and nudge
	// the preview ApplicationSets to re-scan so a brand-new preview Application is
	// generated immediately rather than on the appset's poll interval.
	a.refreshApps(ctx, app.ProjectName, app.Name)
	if a.argoRefresher != nil {
		if err := a.argoRefresher.RefreshAppSets(ctx, []string{"previews", "previews-platform"}); err != nil {
			slog.Warn("argocd preview appset refresh failed; falling back to poll", "project", app.ProjectName, "app", app.Name, "err", err)
		}
	}
	return nil
}

// PublishPreviews implements server.BatchPreviewPublisher — the batched form of
// PublishAppPreview. It builds every target's spec through a request-scoped
// cached vault (shared-scope 1Password lookups run once for the whole fan-out,
// not per member), writes them all in ONE clone/commit/push, then consolidates
// the per-app refresh + one preview-appset refresh. This is the stack-preview
// 504 fix, mirroring PublishApps for pin.
func (a *gitOpsPublisherAdapter) PublishPreviews(ctx context.Context, targets []server.PreviewPublishTarget) error {
	if len(targets) == 0 {
		return nil
	}
	ca := a.withCachedVault()
	bundles := make([]gitops.PreviewPublishBundle, 0, len(targets))
	for _, t := range targets {
		spec, err := ca.buildPreviewSpec(ctx, t.App, t.Preview, t.BaseEnv, t.ImageTag)
		if err != nil {
			return err
		}
		bundles = append(bundles, gitops.PreviewPublishBundle{App: t.App, Preview: spec})
	}
	if err := a.inner.PublishPreviews(ctx, bundles); err != nil {
		return err
	}
	byProject := map[string][]string{}
	for _, t := range targets {
		byProject[t.App.ProjectName] = append(byProject[t.App.ProjectName], t.App.Name)
	}
	for proj, names := range byProject {
		a.refreshApps(ctx, proj, names...)
	}
	if a.argoRefresher != nil {
		if err := a.argoRefresher.RefreshAppSets(ctx, []string{"previews", "previews-platform"}); err != nil {
			slog.Warn("argocd preview appset refresh failed; falling back to poll", "err", err)
		}
	}
	return nil
}

// buildPreviewSpec resolves everything a preview publish needs — template, base
// env cluster/domain, secret wiring, env vars, overlays — into a
// gitops.PreviewPublishSpec. Git-free, so a stack fan-out can build every
// member's spec concurrently (through a cached vault) before one batch publish.
func (a *gitOpsPublisherAdapter) buildPreviewSpec(ctx context.Context, app *domain.App, preview *domain.EnvironmentInstance, baseEnv, imageTag string) (gitops.PreviewPublishSpec, error) {
	tmpl, err := a.resolveTemplate(ctx, app.Spec.Template.Name)
	if err != nil {
		return gitops.PreviewPublishSpec{}, fmt.Errorf("publish preview %s/%s/%s: %w", app.ProjectName, app.Name, preview.EnvName, err)
	}

	// Resolve the BASE env's cluster + base domain — the preview deploys there.
	resolved := a.resolveEnvs(ctx)
	res, ok := resolved[baseEnv]
	if !ok {
		res = envResolved{
			clusterServer: "https://kubernetes.default.svc",
			baseDomain:    "localhost",
			bound:         false,
		}
	}

	var org *rbac.Org
	if a.orgProvider != nil {
		org, _ = a.orgProvider.GetOrg(ctx)
	}

	// Reuse the stable-env secret wiring for the base env (resolves ClusterRef +
	// global/env/project/stack ScopeKeys and ensures their items exist), then add
	// the preview bands on top.
	var basePub gitops.AppPublishEnv
	if org != nil {
		a.enrichPubEnvWithSecrets(ctx, org, app, baseEnv, &basePub)
	}
	scopeKeys := basePub.ScopeKeys
	clusterRef := basePub.ClusterRef
	if a.vault != nil {
		// Always provision + reference the per-app preview band so a "preview"-scope
		// secret set later resolves without re-publishing.
		if err := a.vault.EnsureItem(ctx, secrets.PreviewScope(baseEnv).WithProject(app.ProjectName), secrets.TierApp, app.Name); err == nil {
			scopeKeys.PreviewApp = true
		}
		has := func(scope secrets.Scope, tier secrets.Tier, appName string) bool {
			entries, err := a.vault.ListKeys(ctx, scope, tier, appName)
			return err == nil && len(entries) > 0
		}
		band := secrets.PreviewScope(baseEnv)
		scopeKeys.PreviewShared = has(band, secrets.TierShared, "")
		// Per-PR override items are written out-of-band (CI/API); reference them
		// only when present so ESO never extracts a missing item.
		pr := secrets.PreviewPRScope(baseEnv, preview.EnvName)
		scopeKeys.PreviewPRShared = has(pr, secrets.TierShared, "")
		scopeKeys.PreviewPRApp = has(pr.WithProject(app.ProjectName), secrets.TierApp, app.Name)
	}

	// Env vars: base env's merged vars + the reserved "preview" band on top.
	envVars := a.mergeAllEnvVars(ctx, app, baseEnv, clusterRef, org)
	if ov, ok := app.Spec.EnvironmentDefaults[domain.PreviewOverrideKey]; ok && len(ov.EnvConfig.Vars) > 0 {
		if envVars == nil {
			envVars = map[string]string{}
		}
		for k, v := range ov.EnvConfig.Vars {
			envVars[k] = v
		}
	}

	// Resolve the base env's value overlays (template/org platform overrides +
	// stack shared values) exactly as a stable publish would, so the preview
	// inherits them. The preview band layers on top of these in PublishPreview.
	tmplOv := a.loadOverride(ctx, app.Spec.Template.Name)
	setPlatformOverlays(&basePub, tmpl, tmplOv, baseEnv)
	a.setStackOverlays(ctx, &basePub, app, baseEnv)
	// Composed apps: resolve each component's own template/org overlays for the
	// base env so a composed preview renders per-component values with the same PE
	// overrides the base env deploys (no-op for single-source apps).
	a.setComponentPlatformOverlays(ctx, &basePub, app, baseEnv)

	// Template-level default preview overlay: the template spec's default merged
	// with the operator's sync-safe override (override wins), applied to every
	// preview below the app's own preview band.
	var templatePreviewValues map[string]any
	if tmpl != nil {
		templatePreviewValues = helmvalues.DeepCopyMap(tmpl.Spec.PreviewDefaultValues)
	}
	if tmplOv != nil && len(tmplOv.PreviewDefaultValues) > 0 {
		templatePreviewValues = helmvalues.DeepMerge(templatePreviewValues, helmvalues.DeepCopyMap(tmplOv.PreviewDefaultValues))
	}

	spec := gitops.PreviewPublishSpec{
		PreviewName:             preview.EnvName,
		BaseEnv:                 baseEnv,
		ClusterServer:           res.clusterServer,
		Cluster:                 clusterRef,
		Namespace:               preview.Namespace,
		BaseDomain:              res.baseDomain,
		EnvVars:                 envVars,
		ScopeKeys:               scopeKeys,
		ImageTag:                imageTag,
		SkipCanonicalBase:       !tmpl.Spec.CanonicalValues(),
		PlatformDefaultValues:   basePub.PlatformDefaultValues,
		PlatformEnvValues:       basePub.PlatformEnvValues,
		PlatformClusterValues:   basePub.PlatformClusterValues,
		StackRawValues:          basePub.StackRawValues,
		StackEnvRawValues:       basePub.StackEnvRawValues,
		TemplatePreviewValues:   templatePreviewValues,
		ComponentPlatformValues: basePub.ComponentPlatformValues,
	}
	return spec, nil
}

// DeleteAppPreview implements server.AppPreviewDeleter by removing one app's
// preview gitops-output tree (previews/{baseEnv}/{project}/{name}/{app}) and
// committing, so ArgoCD prunes the generated Application and its namespace.
func (a *gitOpsPublisherAdapter) DeleteAppPreview(ctx context.Context, projectName, previewName, appName, baseEnv string) error {
	return a.inner.DeletePreview(ctx, projectName, previewName, appName, baseEnv)
}

// UnpublishApp implements server.GitOpsPublisher by removing all gitops-output
// directories for the given app and committing the deletion.
func (a *gitOpsPublisherAdapter) UnpublishApp(ctx context.Context, projectName, appName string) error {
	return a.inner.UnpublishApp(ctx, projectName, appName)
}

// RemoveAppEnv implements server.GitOpsPublisher — removes one env's files for an
// app so ArgoCD prunes that env's workload (the explicit "remove from cluster").
func (a *gitOpsPublisherAdapter) RemoveAppEnv(ctx context.Context, projectName, appName, envName string) error {
	return a.inner.RemoveAppEnv(ctx, projectName, appName, envName)
}

// UnpublishProjectApps implements server.GitOpsPublisher (phase 1 of project
// deletion: app dirs, previews, Kargo CRs — the AppProject stays).
func (a *gitOpsPublisherAdapter) UnpublishProjectApps(ctx context.Context, projectName string) error {
	return a.inner.UnpublishProjectApps(ctx, projectName)
}

// UnpublishProjectInfra implements server.GitOpsPublisher (phase 2 of project
// deletion: the ArgoCD AppProject).
func (a *gitOpsPublisherAdapter) UnpublishProjectInfra(ctx context.Context, projectName string) error {
	return a.inner.UnpublishProjectInfra(ctx, projectName)
}

// argoCDHistoryAdapter bridges kube.ArgoCDStatusReader.GetAppDeploymentHistory
// to the server.DeploymentHistoryReader interface.
type argoCDHistoryAdapter struct {
	reader *kube.ArgoCDStatusReader
}

// GetAppDeploymentHistory implements server.DeploymentHistoryReader.
func (a *argoCDHistoryAdapter) GetAppDeploymentHistory(ctx context.Context, projectName, appName, envName string) ([]server.DeploymentHistoryEntry, error) {
	raw, err := a.reader.GetAppDeploymentHistory(ctx, projectName, appName, envName)
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

// stuckAppAdapter bridges the ArgoCD reader's stuck-app detection/unstick to
// the server.StuckAppManager interface, converting the kube DTO to the server one.
type stuckAppAdapter struct {
	reader *kube.ArgoCDStatusReader
}

func (a *stuckAppAdapter) ListStuckApplications(ctx context.Context) ([]server.StuckApp, error) {
	raw, err := a.reader.ListStuckApplications(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]server.StuckApp, 0, len(raw))
	for _, s := range raw {
		out = append(out, server.StuckApp{
			Name:              s.Name,
			Project:           s.Project,
			DeletionTimestamp: s.DeletionTimestamp,
			Finalizers:        s.Finalizers,
			Reason:            s.Reason,
		})
	}
	return out, nil
}

func (a *stuckAppAdapter) UnstickApplication(ctx context.Context, name string) error {
	return a.reader.UnstickApplication(ctx, name)
}

// fakeHistoryAdapter bridges fake.FakeDeploymentHistoryReader to the
// server.DeploymentHistoryReader interface for use in fake/local dev mode.
type fakeHistoryAdapter struct {
	inner *fake.FakeDeploymentHistoryReader
}

// GetAppDeploymentHistory implements server.DeploymentHistoryReader.
func (a *fakeHistoryAdapter) GetAppDeploymentHistory(_ context.Context, _ /*projectName*/, appName, envName string) ([]server.DeploymentHistoryEntry, error) {
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
	pub.SetOrgConfig(orgName, org.ResourceNaming, &org.SecretBackend, org.Branding, org.RoutingProfiles)

	// Build the appSetEnvs list with the SAME bound-only filter the
	// per-app publish path (gitOpsPublisherAdapter.PublishApp) uses.
	// Otherwise the two writers produce different destinations: lists
	// for the same AppProject and flip-flop on every commit pair (the
	// startup/reload run adds kubernetes.default.svc for unbound envs;
	// the per-app run removes it; rinse, repeat).
	//
	// "Bound" means: ClusterRef set + cluster exists in the registry +
	// has a non-empty APIServer. Unbound envs are intentionally skipped
	// at the AppProject layer too — they have no destination cluster
	// to authorize.
	appSetEnvs := make([]gitops.AppSetEnv, 0, len(org.Environments))
	for _, orgEnv := range org.Environments {
		activeRef := orgEnv.EffectiveClusterRef()
		if activeRef == "" || clusterStore == nil {
			logger.Debug("initial env infra: skipping unbound env",
				"env", orgEnv.Name, "reason", "no clusterRef")
			continue
		}
		cluster, err := clusterStore.GetCluster(ctx, activeRef)
		if err != nil {
			logger.Warn("initial env infra: skipping env — cluster not in registry",
				"env", orgEnv.Name, "clusterRef", activeRef, "err", err)
			continue
		}
		if cluster.APIServer == "" {
			logger.Warn("initial env infra: skipping env — cluster has empty apiServer",
				"env", orgEnv.Name, "clusterRef", activeRef)
			continue
		}
		baseDomain := orgEnv.BaseDomain
		if baseDomain == "" {
			baseDomain = "localhost"
		}
		// Authorize EVERY cluster the env is allowed to use (its full ClusterRefs),
		// not just the DeployMode target. The AppProject destinations built from
		// this list must be a superset of what any app can pick: a per-app
		// TargetClusters selection may deploy to a non-active cluster in the env's
		// ClusterRefs, and ArgoCD rejects (InvalidSpecError) any Application whose
		// destination isn't in the AppProject. This MUST match resolveEnvs'
		// allClusters (the per-app publish path) so the two writers of this same
		// AppProject don't disagree — using ResolveDeployTargets() here narrowed it
		// to the active cluster and dropped per-app targets whenever this
		// startup/reload writer ran last.
		var clusters []gitops.ClusterTarget
		for _, ref := range orgEnv.ClusterRefs {
			c, cerr := clusterStore.GetCluster(ctx, ref)
			if cerr != nil || c == nil || c.APIServer == "" {
				continue
			}
			clusters = append(clusters, gitops.ClusterTarget{
				Name:            ref,
				Server:          c.APIServer,
				BaseDomain:      c.BaseDomain,
				RoutingProfiles: c.RoutingProfiles,
			})
		}
		appSetEnvs = append(appSetEnvs, gitops.AppSetEnv{
			EnvName:       orgEnv.Name,
			ClusterServer: cluster.APIServer,
			Clusters:      clusters,
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

// republishAllApps re-publishes every app's gitops files at startup. It exists
// so a manifest-contract change (e.g. the version-scoped chart layout, which
// adds chartPath to app.yaml and moves charts to charts/{template}/{version})
// lands across the whole fleet without per-app manual action — old app.yaml
// files lack the new keys until rewritten. Idempotent: a no-op commit when
// content is unchanged. Best-effort; per-app failures are logged.
// reconcileKargoRegistryCreds provisions (or refreshes) the Kargo credential
// Secrets in every project's kargo-{project} namespace: the image cred (Warehouse
// tag discovery) from the registry config, and the git cred (promotion git steps)
// from the gitops config. The cred path also ensures+labels each kargo-{project}
// namespace as a Kargo Project namespace. Run at startup so Kargo can authenticate
// without waiting for a config save or an app publish. Best-effort: per-project
// failures are logged, not fatal. No-op for a store that is nil/unconfigured.
func reconcileKargoRegistryCreds(ctx context.Context, store *registry.Store, gitStore *gitops.ConfigStore, projectStore project.Store, logger *slog.Logger) {
	if projectStore == nil || (store == nil && gitStore == nil) {
		return
	}
	projects, err := projectStore.List(ctx)
	if err != nil {
		logger.Warn("kargo cred reconcile: list projects failed", "error", err)
		return
	}
	var ok, failed int
	for _, p := range projects {
		ns := gitops.KargoNamespaceForProject(p.Metadata.Name)
		ferr := false
		if store != nil {
			if err := store.EnsureKargoCred(ctx, ns); err != nil {
				logger.Warn("kargo image cred reconcile", "project", p.Metadata.Name, "namespace", ns, "error", err)
				ferr = true
			}
		}
		if gitStore != nil {
			if err := gitStore.EnsureKargoGitCred(ctx, ns); err != nil {
				logger.Warn("kargo git cred reconcile", "project", p.Metadata.Name, "namespace", ns, "error", err)
				ferr = true
			}
		}
		if ferr {
			failed++
			continue
		}
		ok++
	}
	if ok > 0 || failed > 0 {
		logger.Info("kargo cred reconcile complete", "provisioned", ok, "failed", failed)
	}
}

// republishAllApps re-publishes every app's stable-env gitops files and returns
// the number of apps that failed. A zero return means the whole fleet is on the
// current layout (callers use it to gate the generator marker).
func republishAllApps(
	ctx context.Context,
	pub server.GitOpsPublisher,
	appStore domain.AppStore,
	projectStore project.Store,
	orgProvider rbac.OrgProvider,
	logger *slog.Logger,
) int {
	if pub == nil || appStore == nil || projectStore == nil {
		return 0
	}
	projects, err := projectStore.List(ctx)
	if err != nil {
		logger.Warn("republish apps: list projects failed", "error", err)
		return 1
	}
	failures := 0
	for _, p := range projects {
		apps, err := appStore.ListApps(ctx, p.Metadata.Name)
		if err != nil {
			logger.Warn("republish apps: list apps failed", "project", p.Metadata.Name, "error", err)
			failures++
			continue
		}
		for _, app := range apps {
			envs, err := appStore.ListAppEnvironments(ctx, p.Metadata.Name, app.Name)
			if err != nil {
				logger.Warn("republish apps: list envs failed", "project", p.Metadata.Name, "app", app.Name, "error", err)
				failures++
				continue
			}
			var stable []*domain.AppEnvironment
			for _, e := range envs {
				if e.EnvType != domain.AppEnvPreview {
					stable = append(stable, e)
				}
			}
			// Apps created against the org's default pipeline have no persisted
			// AppEnvironment records — they're resolved at publish time. Without
			// this fallback those apps would be silently skipped, leaving their
			// app.yaml on the old (chartPath-less) layout. Mirrors the create
			// path (appHandler.stableEnvsFromOrg).
			if len(stable) == 0 {
				stable = server.StableEnvsFromOrg(ctx, orgProvider, app)
			}
			if err := pub.PublishApp(ctx, app, stable); err != nil {
				logger.Warn("republish apps: publish failed", "project", p.Metadata.Name, "app", app.Name, "error", err)
				failures++
			}
		}
	}
	// Preview environments are intentionally not republished here: they are
	// ephemeral (per-PR) and regenerate with the current layout on the next
	// preview create/sync, and the preview publish path is not wired into
	// startup. This is a documented limitation, not a silent skip.
	logger.Info("republish apps: complete (stable envs only; previews regenerate on next sync)", "failures", failures)
	return failures
}

// generatorStateConfigMap records the generator contract version last applied
// across the fleet, so the startup republish runs once per bump (not per boot).
const (
	generatorStateConfigMap = "suparship-generator-state"
	generatorStateKey       = "appliedGeneratorVersion"
	// itemNameMigrationKey records whether the app-tier secret item rename
	// ({app}-* → {project}-{app}-*) has copied the fleet's legacy items. Value is
	// itemNameMigrationDone once a clean copy has run; bump that sentinel to force
	// a re-copy. Stored in the same ConfigMap as the generator marker.
	itemNameMigrationKey  = "appliedItemNameMigration"
	itemNameMigrationDone = "v1"
)

// generatorStateValue returns the value stored under key in the generator-state
// ConfigMap, or "" when unknown (no client, missing ConfigMap, read error).
func generatorStateValue(ctx context.Context, client kubernetes.Interface, key string) string {
	if client == nil {
		return ""
	}
	cm, err := client.CoreV1().ConfigMaps(secrets.SystemNamespace).Get(ctx, generatorStateConfigMap, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return cm.Data[key]
}

// setGeneratorStateValue upserts key=value in the generator-state ConfigMap.
// Best-effort: a failure just means the next boot re-runs the (idempotent) work.
func setGeneratorStateValue(ctx context.Context, client kubernetes.Interface, key, value string, logger *slog.Logger) {
	if client == nil {
		return
	}
	api := client.CoreV1().ConfigMaps(secrets.SystemNamespace)
	cm, err := api.Get(ctx, generatorStateConfigMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = api.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      generatorStateConfigMap,
				Namespace: secrets.SystemNamespace,
				Labels:    map[string]string{"app.kubernetes.io/managed-by": "suparship"},
			},
			Data: map[string]string{key: value},
		}, metav1.CreateOptions{})
		if err != nil {
			logger.Warn("generator-state: could not record marker", "key", key, "error", err)
		}
		return
	}
	if err != nil {
		logger.Warn("generator-state: could not read marker for update", "key", key, "error", err)
		return
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[key] = value
	if _, err := api.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		logger.Warn("generator-state: could not update marker", "key", key, "error", err)
	}
}

// selfHealSealedTokens reconstructs missing per-env sealed Connect token
// files in the gitops repo from the platform's local stash. Runs once
// per startup (after the publisher is wired) so that an operator who
// wiped the gitops repo gets a fully recovered state without re-pasting
// every token through the UI.
//
// Resilience contract:
//   - Backend != 1Password → no-op.
//   - No 1Password bindings configured → no-op.
//   - Per-binding: if the gitops repo already has the sealed-token, skip
//     (sealed-secrets are non-deterministic; re-publishing every time
//     would produce noisy commits on every restart).
//   - Per-binding: if the local stash is empty, log "operator must
//     re-paste" and continue — we have no way to reconstruct without it.
//   - Errors fetching kubeseal certs / publishing are logged per-env;
//     other envs keep going.
func selfHealSealedTokens(
	ctx context.Context,
	pub *gitops.Publisher,
	orgProvider rbac.OrgProvider,
	clusterStore domain.ClusterStore,
	clusterPool *k8s.ClusterClientPool,
	kubeClient kubernetes.Interface,
	logger *slog.Logger,
) {
	if pub == nil || orgProvider == nil || clusterStore == nil || clusterPool == nil || kubeClient == nil {
		return
	}
	org, err := orgProvider.GetOrg(ctx)
	if err != nil || org == nil {
		return
	}
	if org.SecretBackend.Effective() != secrets.Backend1Password {
		return // only 1Password publishes sealed tokens + stores
	}
	clusters, err := clusterStore.ListClusters(ctx)
	if err != nil {
		logger.Warn("self-heal: listing clusters failed", "error", err)
		return
	}

	certCache := seal.NewK8sCertCache(kubeClient)
	for _, c := range clusters {
		// Skip clusters whose store is already published — re-sealing is
		// non-deterministic and would churn the repo on every restart.
		if present, err := pub.HasClusterSecretStore(ctx, c.Name); err != nil {
			logger.Warn("self-heal: store presence check failed", "cluster", c.Name, "error", err)
			continue
		} else if present {
			continue
		}

		// Need the cluster's stashed token to reconstruct the sealed Secret.
		token, err := secrets.LoadConnectToken(ctx, kubeClient, secrets.ClusterStashKey(c.Name))
		if err != nil || len(token) == 0 {
			logger.Info("self-heal: no stashed Connect token — operator must re-paste it",
				"cluster", c.Name)
			continue
		}

		// Vaults this cluster reads: the global vault + the env vault of each
		// environment bound to it (mirrors server.clusterVaultIDs).
		var vaultIDs []string
		if ref := org.SecretBackend.FindVault(secrets.GlobalScope()); ref != nil && ref.VaultID != "" {
			vaultIDs = append(vaultIDs, ref.VaultID)
		}
		for _, e := range org.Environments {
			if !e.IsBoundTo(c.Name) {
				continue
			}
			if ref := org.SecretBackend.FindVault(secrets.EnvScope(e.Name)); ref != nil && ref.VaultID != "" {
				vaultIDs = append(vaultIDs, ref.VaultID)
			}
		}
		if len(vaultIDs) == 0 || c.APIServer == "" {
			continue
		}

		kubeForCluster, err := clusterPool.Get(ctx, c.Name)
		if err != nil {
			logger.Warn("self-heal: building cluster client failed", "cluster", c.Name, "error", err)
			continue
		}
		cert, err := seal.FetchAndCache(ctx, certCache, kubeForCluster, c.Name, seal.FetchOptions{})
		if err != nil {
			logger.Warn("self-heal: fetching sealing cert failed", "cluster", c.Name, "error", err)
			continue
		}

		var connectEndpoint string
		if ref := org.SecretBackend.FindClusterToken(c.Name); ref != nil {
			connectEndpoint = ref.ConnectEndpoint
		}
		if connectEndpoint == "" && org.SecretBackend.OnePassword != nil {
			connectEndpoint = org.SecretBackend.OnePassword.Connect.Endpoint
		}

		if err := pub.PublishClusterSecretStore(ctx, gitops.ClusterSealParams{
			ClusterName:       c.Name,
			ArgoCDDestination: c.APIServer,
			ESONamespace:      c.EffectiveESONamespace(),
			Cert:              cert,
			Token:             token,
			VaultIDs:          vaultIDs,
			ConnectEndpoint:   connectEndpoint,
		}); err != nil {
			logger.Warn("self-heal: republish failed", "cluster", c.Name, "error", err)
			continue
		}
		logger.Info("self-heal: republished sealed token + unified store from stash", "cluster", c.Name)
	}
}

// brandingFromOrg fetches Org.Branding once at startup and returns its zero
// value when the org isn't loadable. Used to seed Branding on writers that
// embed it; live reload (e.g. on org-config save) requires a server restart
// — same lifecycle as gitops.PublisherConfig.
func brandingFromOrg(ctx context.Context, op rbac.OrgProvider) branding.Config {
	if op == nil {
		return branding.Config{}
	}
	org, err := op.GetOrg(ctx)
	if err != nil || org == nil {
		return branding.Config{}
	}
	return org.Branding
}

// envConfigReaderFromClient builds a cluster-scope env-var reader from the K8s
// client. Returns nil when the client is unavailable (fake mode) so the adapter
// gracefully skips the cluster layer.
//
// brand is applied so writes that go through the same struct (when the env
// writer is wired into the env-config handler) emit replicator-matching
// annotations consistent with the namespace labels the gitops publisher
// puts on app/project namespaces. Read paths ignore Branding.
func envConfigReaderFromClient(client kubernetes.Interface, brand branding.Config) *envconfig.UpperLevelEnvWriter {
	if client == nil {
		return nil
	}
	w := envconfig.NewUpperLevelEnvWriter(client)
	w.Branding = brand
	return w
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

// templateCredStore wires the SealedSecret-backed credentials writer for
// UI-managed external-template-repo credentials. Returns nil in fake/
// local-dev mode (no kube clients) so the credentials endpoints respond
// 503 rather than panicking on a nil client.
func templateCredStore(client kubernetes.Interface, dyn dynamic.Interface, logger *slog.Logger) *credstore.Store {
	if client == nil || dyn == nil {
		return nil
	}
	return &credstore.Store{
		Client:    client,
		DynClient: dyn,
		Logger:    logger,
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
		// Eager first run so a freshly installed cluster doesn't sit with
		// an empty template gallery for `interval` before the first sync.
		// runOneTemplateSync silently no-ops when the registry is empty,
		// so this is safe to always run — no extra guards needed.
		runOneTemplateSync(ctx, engine, store, logger)
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

// templateOverrideLoaderFromClient returns a loader for a template's org-level
// platform override (separate ConfigMap, sync-safe). Returns nil in
// fake/local-dev mode (no client) — the publisher then applies no org layer.
func templateOverrideLoaderFromClient(client kubernetes.Interface) func(context.Context, string) (*domain.TemplateOverride, error) {
	if client == nil {
		return nil
	}
	return func(ctx context.Context, name string) (*domain.TemplateOverride, error) {
		return kube.LoadTemplateOverride(ctx, client, name)
	}
}

// kubeChartFetcher implements gitops.ChartFetcher by reading chart.tgz from
// the template's ConfigMap in suparship-system. Built once per server start
// and shared across publisher rebuilds so the underlying client stays warm.
type kubeChartFetcher struct {
	client kubernetes.Interface
}

// LoadChartBundle satisfies gitops.ChartFetcher. Resolves the per-version
// archive when version is non-empty (with a fall-back to the alias when
// the archive is missing — covers templates persisted before versioned
// naming landed). Empty version reads the alias directly.
func (k *kubeChartFetcher) LoadChartBundle(ctx context.Context, templateName, version string) ([]byte, error) {
	return kube.LoadChartBundleVersion(ctx, k.client, templateName, version)
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

// kubeTemplateLoader implements gitops.TemplateLoader by reading the
// versioned-or-current template ConfigMap from suparship-system and
// parsing it. Used by the publisher to detect external-mode templates
// at publish time so it can route to envs-external/ and skip syncChart.
type kubeTemplateLoader struct {
	client kubernetes.Interface
}

func (l *kubeTemplateLoader) LoadTemplate(ctx context.Context, name string) (*tpl.Template, error) {
	if l.client == nil || name == "" {
		return nil, nil
	}
	cm, err := l.client.CoreV1().ConfigMaps("suparship-system").Get(
		ctx, kube.TemplateConfigMapName(name), metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		// Template not in cluster (e.g. disk-only built-in via
		// SUPARSHIP_TEMPLATES_DIR). Silent fall-through — publisher
		// treats unresolvable as inline-mode.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	raw := cm.Data[tpl.TemplateFileName]
	if raw == "" {
		return nil, nil
	}
	return tpl.Parse([]byte(raw))
}

// templateLoaderFromClient mirrors chartFetcherFromClient: nil in fake
// mode so the publisher falls back to inline-only behaviour without the
// caller needing to gate the wiring.
func templateLoaderFromClient(client kubernetes.Interface) gitops.TemplateLoader {
	if client == nil {
		return nil
	}
	return &kubeTemplateLoader{client: client}
}
