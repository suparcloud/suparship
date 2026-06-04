// Package server provides the suparship HTTP API server.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/gitops"
	"github.com/suparcloud/suparship/internal/k8s"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/registry"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/seal"
	"github.com/suparcloud/suparship/internal/secrets"
	"github.com/suparcloud/suparship/internal/secrets/onepassword"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
	"github.com/suparcloud/suparship/internal/tpl/credstore"
	"github.com/suparcloud/suparship/internal/tpl/registrysync"
)

const shutdownTimeout = 5 * time.Second

// clusterPoolAdapter bridges *k8s.ClusterClientPool to the sealClientPool
// interface expected by secretsHandler and clusterHandler.
type clusterPoolAdapter struct {
	pool *k8s.ClusterClientPool
}

func (a *clusterPoolAdapter) GetKubeClient(ctx context.Context, clusterName string) (kubernetes.Interface, error) {
	return a.pool.Get(ctx, clusterName)
}

// GitOpsPublisher commits app manifests to the GitOps repository. When nil,
// app creation only persists to the store and no git commit is performed.
// Implementations must be safe for concurrent use.
type GitOpsPublisher interface {
	// PublishApp writes app.yaml and values.yaml for the first bound stable
	// environment (and all preview environments) to the GitOps repo on initial
	// app creation. Higher stable environments receive their files only when an
	// explicit promotion is triggered via PublishAppEnv.
	PublishApp(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) error
	// PublishAppEnv writes app.yaml and values.yaml for a single environment to
	// the GitOps repo. Called on every explicit promotion so the target env's
	// files are present before Kargo / ArgoCD act on the promotion.
	PublishAppEnv(ctx context.Context, app *domain.App, env *domain.AppEnvironment) error
	// UnpublishApp removes all GitOps files for an app (all stable-env
	// directories) and commits + pushes the deletion. It is a no-op if no
	// files exist for the app.
	UnpublishApp(ctx context.Context, projectName, appName string) error
}

// KargoPromoter creates Kargo Promotion CRs to advance freight through the
// promotion pipeline. When nil the app promotion endpoint falls back to the
// in-store release copy (MVP stub). Implementations must be safe for concurrent use.
type KargoPromoter interface {
	// CreatePromotion creates a Kargo Promotion CR to advance the current
	// freight from fromStage to toStage in projectNS (= Kargo project namespace).
	//
	// Returns kube.ErrKargoNoFreight when fromStage has no freight to promote.
	CreatePromotion(ctx context.Context, projectNS, appName, fromStage, toStage string) (KargoPromotionResult, error)
}

// KargoStatusReader reads the live status of Kargo Promotion CRs. When nil,
// the GET promotion-status endpoint is disabled. Implementations must be safe
// for concurrent use.
type KargoStatusReader interface {
	// GetPromotionStatus returns the current observed status of a Kargo
	// Promotion CR identified by promotionName within projectNS.
	GetPromotionStatus(ctx context.Context, projectNS, promotionName string) (KargoPromotionResult, error)
}

// KargoPipelineReader reads the live status of Kargo Stage CRs for pipeline
// visibility (phase, health, available freight count). When nil, the pipeline
// status endpoint is disabled. Implementations must be safe for concurrent use.
type KargoPipelineReader interface {
	// ListAppStageStatuses returns the Kargo Stage statuses that belong to
	// appName within projectNS. Stage names follow the "{appName}-{envName}"
	// convention; the returned slice is ordered by env name.
	ListAppStageStatuses(ctx context.Context, projectNS, appName string) ([]KargoStageStatusResult, error)
}

// KargoStageStatusResult is the DTO returned by KargoPipelineReader.
type KargoStageStatusResult struct {
	// StageName is the full Kargo Stage name, e.g. "color-app-staging".
	StageName string
	// EnvName is the suparship environment name derived from the stage name.
	EnvName string
	// Phase is the current stage phase: "Steady", "Promoting", "NotReady".
	Phase string
	// Health is the aggregated health: "Healthy", "Unhealthy", "Unknown".
	Health string
	// CurrentFreight is the Freight name currently running in this stage.
	CurrentFreight string
	// AvailableFreightCount is how many new Freight items are waiting to be
	// promoted into this stage. >0 means a new image/commit is available.
	AvailableFreightCount int
}

// KargoPromotionResult is the DTO returned by KargoPromoter.CreatePromotion.
type KargoPromotionResult struct {
	// Name is the generated Kargo Promotion CR name.
	Name string
	// Stage is the target Stage (= target environment name).
	Stage string
	// Freight is the Freight name that was promoted.
	Freight string
	// Phase is the initial observed Promotion phase (e.g. "Pending").
	Phase string
}

// DeploymentHistoryEntry is one sync event from the ArgoCD Application history.
type DeploymentHistoryEntry struct {
	// ID is the ArgoCD sequence number for this sync event.
	ID int64
	// Revision is the Git commit SHA that was synced.
	Revision string
	// DeployedAt is the RFC 3339 timestamp when the sync completed.
	DeployedAt string
	// DeployStartedAt is the RFC 3339 timestamp when the sync began (may be empty).
	DeployStartedAt string
	// RepoURL is the source Git repository URL.
	RepoURL string
	// Path is the path within the repository that was synced.
	Path string
	// TargetRevision is the Git ref (branch/tag/commit) tracked by the Application.
	TargetRevision string
}

// DeploymentHistoryReader reads the ArgoCD sync history for an app/environment.
// When nil, the deployment history endpoint returns 501. Implementations must
// be safe for concurrent use.
type DeploymentHistoryReader interface {
	// GetAppDeploymentHistory returns the sync history for the ArgoCD Application
	// "{appName}-{envName}" in reverse-chronological order (most recent first).
	// Returns an empty slice (not an error) when no history is available.
	GetAppDeploymentHistory(ctx context.Context, appName, envName string) ([]DeploymentHistoryEntry, error)
}

// ReadinessProber is a named readiness check injected into the server.
// Each prober is called by GET /readyz; any non-nil error marks the
// server as not ready.
type ReadinessProber struct {
	// Name is the human-readable check name included in the readyz JSON response.
	Name string
	// Check is the probe function. It should complete quickly (< 2 s).
	Check func(ctx context.Context) error
}

// GitOpsActivatorFunc is called after GitOps configuration is saved through
// the settings API. It receives the saved config and the resolved credentials
// (empty strings for public repos or when none are available) and is
// responsible for: registering the repo with ArgoCD, hot-swapping the live
// GitOpsPublisher, and optionally triggering an initial env-infra publish.
//
// The function should be idempotent and treat ArgoCD registration failures as
// non-fatal warnings — ArgoCD may not be installed yet.
// Nil means no activation is wired (safe default for tests).
type GitOpsActivatorFunc func(ctx context.Context, cfg *gitops.RepoConfig, username, password string) error

// PublisherHolder wraps a GitOpsPublisher behind an RW mutex so the live
// publisher can be swapped at runtime when new GitOps config is saved.
// It implements GitOpsPublisher so it can be passed anywhere a publisher is
// expected; the inner publisher is replaced via Swap without restarting the
// server.
type PublisherHolder struct {
	mu sync.RWMutex
	p  GitOpsPublisher
}

// NewPublisherHolder creates a PublisherHolder with an optional initial publisher.
// Pass nil when no publisher is configured at startup.
func NewPublisherHolder(initial GitOpsPublisher) *PublisherHolder {
	return &PublisherHolder{p: initial}
}

// PublishApp implements GitOpsPublisher. It delegates to the currently held
// publisher; if none is set it returns nil (no-op).
func (h *PublisherHolder) PublishApp(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) error {
	h.mu.RLock()
	p := h.p
	h.mu.RUnlock()
	if p == nil {
		return nil
	}
	return p.PublishApp(ctx, app, envs)
}

// PublishAppEnv implements GitOpsPublisher. It delegates to the currently held
// publisher; if none is set it returns nil (no-op).
func (h *PublisherHolder) PublishAppEnv(ctx context.Context, app *domain.App, env *domain.AppEnvironment) error {
	h.mu.RLock()
	p := h.p
	h.mu.RUnlock()
	if p == nil {
		return nil
	}
	return p.PublishAppEnv(ctx, app, env)
}

// UnpublishApp implements GitOpsPublisher. It delegates to the currently held
// publisher; if none is set it returns nil (no-op).
func (h *PublisherHolder) UnpublishApp(ctx context.Context, projectName, appName string) error {
	h.mu.RLock()
	p := h.p
	h.mu.RUnlock()
	if p == nil {
		return nil
	}
	return p.UnpublishApp(ctx, projectName, appName)
}

// Swap replaces the inner publisher atomically. Subsequent PublishApp calls
// will use the new publisher. Any in-flight call completes against the old one.
func (h *PublisherHolder) Swap(p GitOpsPublisher) {
	h.mu.Lock()
	h.p = p
	h.mu.Unlock()
}

// SecretStoreReconciler recomputes and publishes the full set of ESO
// ClusterSecretStores (global + per-env + per-cluster) to the gitops repo.
// Called by the env/cluster lifecycle hooks so the stores exist before app
// ExternalSecrets reference them.
type SecretStoreReconciler interface {
	ReconcileSecretStores(ctx context.Context) error
}

// ReconcileSecretStores delegates to the held publisher when it implements
// SecretStoreReconciler; otherwise it is a no-op. This lets PublisherHolder
// satisfy SecretStoreReconciler without widening the GitOpsPublisher interface.
func (h *PublisherHolder) ReconcileSecretStores(ctx context.Context) error {
	h.mu.RLock()
	p := h.p
	h.mu.RUnlock()
	if r, ok := p.(SecretStoreReconciler); ok {
		return r.ReconcileSecretStores(ctx)
	}
	return nil
}

// SealPublisherHolder wraps a SealedTokenPublisher behind an RW mutex so it
// can be hot-swapped when GitOps config is changed via the settings UI.
type SealPublisherHolder struct {
	mu sync.RWMutex
	p  SealedTokenPublisher
}

// NewSealPublisherHolder creates a holder with an optional initial publisher.
func NewSealPublisherHolder(initial SealedTokenPublisher) *SealPublisherHolder {
	return &SealPublisherHolder{p: initial}
}

// PublishSealedReadToken implements SealedTokenPublisher.
func (h *SealPublisherHolder) PublishSealedReadToken(ctx context.Context, params gitops.SealedReadTokenPublishParams) error {
	h.mu.RLock()
	p := h.p
	h.mu.RUnlock()
	if p == nil {
		return nil
	}
	return p.PublishSealedReadToken(ctx, params)
}

// DeleteSealedReadToken implements SealedTokenPublisher.
func (h *SealPublisherHolder) DeleteSealedReadToken(ctx context.Context, params gitops.DeleteSealedReadTokenParams) error {
	h.mu.RLock()
	p := h.p
	h.mu.RUnlock()
	if p == nil {
		return nil
	}
	return p.DeleteSealedReadToken(ctx, params)
}

// RefreshSecretStore implements SealedTokenPublisher.
func (h *SealPublisherHolder) RefreshSecretStore(ctx context.Context, params gitops.RefreshSecretStoreParams) error {
	h.mu.RLock()
	p := h.p
	h.mu.RUnlock()
	if p == nil {
		return nil
	}
	return p.RefreshSecretStore(ctx, params)
}

// Swap replaces the inner publisher atomically.
func (h *SealPublisherHolder) Swap(p SealedTokenPublisher) {
	h.mu.Lock()
	h.p = p
	h.mu.Unlock()
}

// Config holds server configuration.
type Config struct {
	Addr          string
	UIDir         string             // optional: path to built frontend assets
	CORSOrigins   []string           // optional: allowed CORS origins
	Authenticator auth.Authenticator // optional: enables auth endpoints when set
	OrgProvider   rbac.OrgStore      // optional: enables RBAC-protected routes when set (write ops also require OrgStore)
	Templates     []*tpl.Template    // optional: pre-loaded templates for /api/v1/templates
	// ClusterTemplateLoader resolves cluster-stored templates on each
	// request. When non-nil it is merged with Templates so newly imported
	// charts surface in the gallery without restarting the server. Built-in
	// names take precedence on collisions.
	ClusterTemplateLoader   ClusterTemplateLoader
	ProjectStore            project.Store           // optional: enables service creation when set
	RuntimeProvider         runtime.Provider        // optional: enables runtime inventory when set
	LogsProvider            runtime.LogsProvider    // optional: enables logs endpoint when set
	PreviewStore            preview.Store           // optional: enables preview endpoints when set
	AppStore                domain.AppStore         // optional: enables app read endpoints when set
	ClusterStore            domain.ClusterStore     // optional: enables /api/v1/clusters endpoints when set
	GitOpsPublisher         GitOpsPublisher         // optional: commits app manifests to gitops repo on create
	KargoPromoter           KargoPromoter           // optional: enables real Kargo-backed promotions
	KargoStatusReader       KargoStatusReader       // optional: enables GET promotion-status endpoint
	KargoPipelineReader     KargoPipelineReader     // optional: enables GET pipeline-stages endpoint
	DeploymentHistoryReader DeploymentHistoryReader // optional: enables GET .../environments/{env}/history endpoint
	VaultStore              secrets.VaultStore      // optional: enables secret CRUD across global/env/cluster scopes
	SecretsAuditor          *secrets.Auditor        // optional: enables audit logging for secret ops
	ReadinessProbers        []ReadinessProber       // optional: checked by GET /readyz
	CookieSecure            bool                    // true for production (HTTPS)
	Logger                  *slog.Logger
	// UpperLevelEnvWriter, when set, writes Org/Environment/Project runtime
	// ConfigMaps in suparship-system alongside domain-store saves. Requires a
	// live Kubernetes client; omit in unit tests.
	UpperLevelEnvWriter *envconfig.UpperLevelEnvWriter
	// KubeClient is the Kubernetes clientset for prerequisite detection.
	// Nil in fake mode (placeholder data is returned instead).
	KubeClient kubernetes.Interface
	// DynClient is the dynamic Kubernetes client for CRD interactions (ArgoCD, Kargo).
	// Used for the ArgoCD system-project prerequisite check. Nil disables that check.
	DynClient dynamic.Interface
	// ClusterPool builds per-cluster Kubernetes clients from stored kubeconfigs.
	// Used to auto-fetch sealed-secrets certificates on cache miss or refresh.
	// Nil disables auto-fetch (cert must be pre-populated in the ConfigMap).
	ClusterPool *k8s.ClusterClientPool
	// GitOpsConfigStore reads/writes the GitOps repo ConfigMap. Nil disables
	// the /api/v1/gitops/* endpoints.
	GitOpsConfigStore *gitops.ConfigStore
	// GitOpsActivator is called after GitOps config is saved via the settings
	// API to apply it immediately (ArgoCD registration + publisher hot-reload).
	// Nil disables post-save activation (safe for tests and fake mode).
	GitOpsActivator GitOpsActivatorFunc
	// SealedTokenPublisher publishes sealed Connect tokens to the GitOps repo.
	// Nil disables GitOps publishing in the binding flow (binding still saves state).
	SealedTokenPublisher SealedTokenPublisher
	// TemplateRegistryStore reads/writes the template registry ConfigMap.
	// Nil disables the /api/v1/templates/registry and /sources endpoints.
	TemplateRegistryStore *tpl.RegistryStore
	// RegistryStore reads/writes the container registry ConfigMap.
	// Nil disables the /api/v1/registry/* endpoints.
	RegistryStore *registry.Store
	// RegistrySyncEngine drives the external-template sync flow. When nil
	// the registry's read endpoints still work; the /sync POST routes
	// return 503.
	RegistrySyncEngine *registrysync.Engine
	// TemplateCredStore seals UI-submitted external-template-repo
	// credentials into the management cluster as SealedSecret CRs. Nil
	// disables the /credentials and /test-connection endpoints (operators
	// must hand-create Secrets and reference them via existingSecret).
	TemplateCredStore *credstore.Store
}

// Server is the suparship HTTP API server.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
}

// New creates a Server from the given Config.
func New(cfg Config) *Server {
	mux := http.NewServeMux()
	registerRoutes(mux, cfg.ReadinessProbers)

	var ah *authHandler
	if cfg.Authenticator != nil {
		ah = &authHandler{
			authenticator: cfg.Authenticator,
			sessions:      session.NewStore(sessionTTL),
			cookieSecure:  cfg.CookieSecure,
		}
		ah.registerRoutes(mux)
		cfg.Logger.Info("auth endpoints enabled")
	}

	if ah != nil {
		th := newTemplateHandler(ah, cfg.Templates, cfg.ClusterTemplateLoader, cfg.Logger)
		th.kubeClient = cfg.KubeClient
		// Same admin-gating shape as the registry handler: when the org
		// provider is wired we require org_admin on DELETE; without it we
		// fall back to plain auth so harnesses without an OrgStore work.
		if cfg.OrgProvider != nil {
			rh := &rbacHandler{auth: ah, orgStore: cfg.OrgProvider, projectStore: cfg.ProjectStore}
			th.authMiddleware = func(next http.HandlerFunc) http.HandlerFunc {
				return ah.requireAuth(rh.requireOrgAdmin(next))
			}
		}
		th.registerRoutes(mux)
		cfg.Logger.Info("template endpoints enabled", "count", len(cfg.Templates))
	}

	if cfg.OrgProvider != nil && ah != nil {
		rh := &rbacHandler{
			auth:         ah,
			orgStore:     cfg.OrgProvider,
			projectStore: cfg.ProjectStore,
		}
		if r, ok := cfg.GitOpsPublisher.(SecretStoreReconciler); ok {
			rh.storeReconciler = r
		}
		if cfg.ProjectStore != nil {
			rh.serviceHandler = newServiceHandler(cfg.ProjectStore, cfg.Templates)
			cfg.Logger.Info("service creation endpoint enabled")

			rh.inventoryHandler = newInventoryHandler(cfg.ProjectStore, cfg.RuntimeProvider)
			rh.inventoryHandler.orgProvider = cfg.OrgProvider
			cfg.Logger.Info("inventory endpoints enabled")

			if cfg.PreviewStore != nil {
				rh.previewHandler = newPreviewHandler(cfg.PreviewStore, cfg.ProjectStore, cfg.RuntimeProvider, cfg.OrgProvider)
				cfg.Logger.Info("preview endpoints enabled")
			}

			rh.promoteHandler = newPromoteHandler(cfg.ProjectStore)
			cfg.Logger.Info("promote endpoint enabled")

			if cfg.LogsProvider != nil {
				rh.logsHandler = newLogsHandler(cfg.ProjectStore, cfg.LogsProvider)
				cfg.Logger.Info("logs endpoint enabled")
			}
		}
		if cfg.AppStore != nil {
			rh.appHandler = newAppHandler(cfg.AppStore, cfg.Templates, cfg.ProjectStore)
			rh.appHandler.kubeClient = cfg.KubeClient
			if cfg.OrgProvider != nil {
				rh.appHandler.orgProvider = cfg.OrgProvider
			}
			if cfg.RuntimeProvider != nil {
				rh.appHandler.runtimeProvider = cfg.RuntimeProvider
				cfg.Logger.Info("app live status enrichment enabled")
			}
			if cfg.LogsProvider != nil {
				rh.appHandler.logsProvider = cfg.LogsProvider
				cfg.Logger.Info("app logs endpoint enabled")
			}
			if cfg.GitOpsPublisher != nil {
				rh.appHandler.gitOpsPublisher = cfg.GitOpsPublisher
				cfg.Logger.Info("app gitops publisher enabled")
			} else {
				cfg.Logger.Info("app gitops publisher not configured — skipping git commits on app create")
			}
			if cfg.KargoPromoter != nil {
				rh.appHandler.kargoPromoter = cfg.KargoPromoter
				cfg.Logger.Info("kargo promoter enabled — promotions will use Kargo Promotion CRs")
			} else {
				cfg.Logger.Info("kargo promoter not configured — using in-store release copy for promotions")
			}
			if cfg.KargoStatusReader != nil {
				rh.appHandler.kargoStatusReader = cfg.KargoStatusReader
				cfg.Logger.Info("kargo status reader enabled — promotion status endpoint active")
			}
			if cfg.KargoPipelineReader != nil {
				rh.appHandler.kargoPipelineReader = cfg.KargoPipelineReader
				cfg.Logger.Info("kargo pipeline reader enabled — stage status endpoint active")
			}
			if cfg.DeploymentHistoryReader != nil {
				rh.appHandler.deploymentHistoryReader = cfg.DeploymentHistoryReader
				cfg.Logger.Info("deployment history reader enabled — history endpoint active")
			}
			cfg.Logger.Info("app endpoints enabled")
		}
		if cfg.AppStore != nil && cfg.ProjectStore != nil {
			ech := &envConfigHandler{
				orgStore:         cfg.OrgProvider,
				projectStore:     cfg.ProjectStore,
				appStore:         cfg.AppStore,
				upperLevelWriter: cfg.UpperLevelEnvWriter,
				publisher:        cfg.GitOpsPublisher,
				logger:           cfg.Logger,
			}
			rh.envConfigHandler = ech
			cfg.Logger.Info("env config endpoints enabled")
		}
		if cfg.AppStore != nil && cfg.VaultStore != nil {
			rh.secretsHandler = &secretsHandler{
				orgStore: cfg.OrgProvider,
				appStore: cfg.AppStore,
				vault:    cfg.VaultStore,
				auditor:  cfg.SecretsAuditor,
				logger:   cfg.Logger,
			}
			if cfg.KubeClient != nil {
				rh.secretsHandler.kubeClient = cfg.KubeClient
				rh.secretsHandler.saTokenStore = NewKubeSATokenStore(cfg.KubeClient)
				rh.secretsHandler.saClientFactory = func(ctx context.Context, token string) (onepassword.SAClient, error) {
					return onepassword.NewSDKClient(ctx, token)
				}
				rh.secretsHandler.certCache = seal.NewK8sCertCache(cfg.KubeClient)
			}
			if cfg.ClusterPool != nil {
				rh.secretsHandler.clusterPool = &clusterPoolAdapter{pool: cfg.ClusterPool}
			}
			if cfg.ClusterStore != nil {
				rh.secretsHandler.clusterStore = cfg.ClusterStore
			}
			if cfg.SealedTokenPublisher != nil {
				rh.secretsHandler.sealPublisher = cfg.SealedTokenPublisher
			}
			cfg.Logger.Info("secrets management endpoints enabled")
		}
		rh.registerRoutes(mux)
		cfg.Logger.Info("RBAC-protected routes enabled")

		if cfg.KubeClient != nil {
			tih := &templateImportHandler{
				client: cfg.KubeClient,
				authMiddleware: func(next http.HandlerFunc) http.HandlerFunc {
					return ah.requireAuth(rh.requireOrgAdmin(next))
				},
				logger: cfg.Logger,
			}
			tih.registerRoutes(mux)
			cfg.Logger.Info("template import endpoints enabled")
		}
	}

	if cfg.ClusterStore != nil && ah != nil {
		ch := &clusterHandler{
			store:     cfg.ClusterStore,
			auth:      ah,
			certCache: seal.NewK8sCertCache(cfg.KubeClient),
			logger:    cfg.Logger,
		}
		if cfg.ClusterPool != nil {
			ch.pool = &clusterPoolAdapter{pool: cfg.ClusterPool}
		}
		if r, ok := cfg.GitOpsPublisher.(SecretStoreReconciler); ok {
			ch.storeReconciler = r
		}
		ch.registerRoutes(mux)
		cfg.Logger.Info("cluster endpoints enabled")
	}

	oh := &onboardingHandler{
		orgProvider:  cfg.OrgProvider, // OrgStore satisfies OrgProvider
		projectStore: cfg.ProjectStore,
		authEnabled:  cfg.Authenticator != nil,
	}
	mux.HandleFunc("GET /api/v1/onboarding/status", oh.handleStatus)

	if cfg.KubeClient != nil {
		ph := &prerequisitesHandler{client: cfg.KubeClient, dynClient: cfg.DynClient}
		ph.registerRoutes(mux)
		cfg.Logger.Info("prerequisites detection endpoint enabled")
	} else {
		ph := &placeholderPrerequisitesHandler{}
		ph.registerRoutes(mux)
		cfg.Logger.Info("prerequisites detection endpoint enabled (fake mode)")
	}

	if cfg.GitOpsConfigStore != nil && ah != nil {
		gh := &gitopsHandler{
			store:     cfg.GitOpsConfigStore,
			auth:      ah,
			logger:    cfg.Logger,
			activator: cfg.GitOpsActivator,
		}
		gh.registerRoutes(mux)
		cfg.Logger.Info("gitops config endpoints enabled")
	}

	if cfg.TemplateRegistryStore != nil && ah != nil {
		trh := &templateRegistryHandler{
			store:      cfg.TemplateRegistryStore,
			auth:       ah,
			engine:     cfg.RegistrySyncEngine,
			credStore:  cfg.TemplateCredStore,
			kubeClient: cfg.KubeClient,
			logger:     cfg.Logger,
		}
		// When the org provider is wired we can require org_admin on the
		// write/sync routes; without it we fall back to plain auth so test
		// harnesses without an OrgStore keep working.
		if cfg.OrgProvider != nil {
			rh := &rbacHandler{auth: ah, orgStore: cfg.OrgProvider, projectStore: cfg.ProjectStore}
			trh.authMiddleware = func(next http.HandlerFunc) http.HandlerFunc {
				return ah.requireAuth(rh.requireOrgAdmin(next))
			}
		}
		trh.registerRoutes(mux)
		cfg.Logger.Info("template registry endpoints enabled",
			"sync_engine", cfg.RegistrySyncEngine != nil,
		)
	}

	if cfg.RegistryStore != nil && ah != nil {
		rgh := &registryHandler{
			store:  cfg.RegistryStore,
			auth:   ah,
			logger: cfg.Logger,
		}
		rgh.registerRoutes(mux)
		cfg.Logger.Info("container registry config endpoints enabled")
	}

	if ah != nil {
		eh := &exportHandler{
			auth:                  ah,
			orgProvider:           cfg.OrgProvider,
			clusterStore:          cfg.ClusterStore,
			gitopsConfigStore:     cfg.GitOpsConfigStore,
			registryStore:         cfg.RegistryStore,
			templateRegistryStore: cfg.TemplateRegistryStore,
			logger:                cfg.Logger,
		}
		eh.registerRoutes(mux)
		cfg.Logger.Info("config export endpoint enabled")

		chh := &credentialHealthHandler{
			auth:                  ah,
			kubeClient:            cfg.KubeClient,
			orgProvider:           cfg.OrgProvider,
			gitopsConfigStore:     cfg.GitOpsConfigStore,
			registryStore:         cfg.RegistryStore,
			templateRegistryStore: cfg.TemplateRegistryStore,
			logger:                cfg.Logger,
		}
		chh.registerRoutes(mux)
		cfg.Logger.Info("credential health endpoint enabled")
	}

	if cfg.UIDir != "" {
		mux.Handle("/", spaHandler(cfg.UIDir))
		cfg.Logger.Info("serving frontend", "dir", cfg.UIDir)
	}

	var handler http.Handler = mux
	if len(cfg.CORSOrigins) > 0 {
		handler = corsMiddleware(cfg.CORSOrigins, handler)
		cfg.Logger.Info("CORS enabled", "origins", cfg.CORSOrigins)
	}

	// Request logging middleware wraps the outermost handler so every
	// request — regardless of path — is logged at Debug level.
	if cfg.Logger != nil {
		handler = requestLogMiddleware(cfg.Logger, handler)
	}

	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.Addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		},
		logger: cfg.Logger,
	}
}

// Handler returns the HTTP handler used by the server. Useful for testing
// with httptest.NewServer without starting a real TCP listener.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// requestLogMiddleware logs every HTTP request at slog.LevelDebug.
// It records method, path, status code, and elapsed time. Health-check
// paths (/healthz, /readyz) are intentionally included so their polling
// cadence is visible when debugging.
func requestLogMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		logger.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

// responseRecorder wraps http.ResponseWriter to capture the status code
// written by the handler so it can be included in the access log.
type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (rr *responseRecorder) WriteHeader(code int) {
	rr.status = code
	rr.ResponseWriter.WriteHeader(code)
}

// Run starts the server and blocks until ctx is cancelled, then shuts down
// gracefully.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.httpServer.Addr, err)
	}

	s.logger.Info("server listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
		s.logger.Info("shutting down server")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown: %w", err)
	}

	s.logger.Info("server stopped")
	return nil
}
