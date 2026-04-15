// Package server provides the suparship HTTP API server.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/suparcloud/suparship/internal/auth"
	"github.com/suparcloud/suparship/internal/domain"
	"github.com/suparcloud/suparship/internal/envconfig"
	"github.com/suparcloud/suparship/internal/preview"
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/runtime"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

const shutdownTimeout = 5 * time.Second

// GitOpsPublisher commits app manifests to the GitOps repository. When nil,
// app creation only persists to the store and no git commit is performed.
// Implementations must be safe for concurrent use.
type GitOpsPublisher interface {
	// PublishApp writes app.yaml and values.yaml for each environment to the
	// GitOps repo. BaseDomain controls the routing.host value in values.yaml;
	// it is taken from the environment's cluster registration (defaults to
	// "localhost" when the environment has no cluster or when the cluster has
	// no configured base domain).
	PublishApp(ctx context.Context, app *domain.App, envs []*domain.AppEnvironment) error
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

// Config holds server configuration.
type Config struct {
	Addr            string
	UIDir           string               // optional: path to built frontend assets
	CORSOrigins     []string             // optional: allowed CORS origins
	Authenticator   auth.Authenticator   // optional: enables auth endpoints when set
	OrgProvider     rbac.OrgStore        // optional: enables RBAC-protected routes when set (write ops also require OrgStore)
	Templates       []*tpl.Template      // optional: pre-loaded templates for /api/v1/templates
	ProjectStore    project.Store        // optional: enables service creation when set
	RuntimeProvider runtime.Provider     // optional: enables runtime inventory when set
	LogsProvider    runtime.LogsProvider // optional: enables logs endpoint when set
	PreviewStore    preview.Store        // optional: enables preview endpoints when set
	AppStore        domain.AppStore      // optional: enables app read endpoints when set
	ClusterStore    domain.ClusterStore  // optional: enables /api/v1/clusters endpoints when set
	GitOpsPublisher GitOpsPublisher      // optional: commits app manifests to gitops repo on create
	KargoPromoter   KargoPromoter        // optional: enables real Kargo-backed promotions
	KargoStatusReader KargoStatusReader  // optional: enables GET promotion-status endpoint
	KargoPipelineReader KargoPipelineReader // optional: enables GET pipeline-stages endpoint
	DeploymentHistoryReader DeploymentHistoryReader // optional: enables GET .../environments/{env}/history endpoint
	ReadinessProbers []ReadinessProber   // optional: checked by GET /readyz
	CookieSecure    bool                 // true for production (HTTPS)
	Logger          *slog.Logger
	// UpperLevelEnvWriter, when set, writes Org/Environment/Project runtime
	// ConfigMaps in suparship-system alongside domain-store saves. Requires a
	// live Kubernetes client; omit in unit tests.
	UpperLevelEnvWriter *envconfig.UpperLevelEnvWriter
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
		th := newTemplateHandler(ah, cfg.Templates)
		th.registerRoutes(mux)
		cfg.Logger.Info("template endpoints enabled", "count", len(cfg.Templates))
	}

	if cfg.OrgProvider != nil && ah != nil {
		rh := &rbacHandler{
			auth:         ah,
			orgStore:     cfg.OrgProvider,
			projectStore: cfg.ProjectStore,
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
		rh.registerRoutes(mux)
		cfg.Logger.Info("RBAC-protected routes enabled")
	}

	if cfg.ClusterStore != nil && ah != nil {
		ch := &clusterHandler{store: cfg.ClusterStore, auth: ah}
		ch.registerRoutes(mux)
		cfg.Logger.Info("cluster endpoints enabled")
	}

	oh := &onboardingHandler{
		orgProvider:  cfg.OrgProvider, // OrgStore satisfies OrgProvider
		projectStore: cfg.ProjectStore,
		authEnabled:  cfg.Authenticator != nil,
	}
	mux.HandleFunc("GET /api/v1/onboarding/status", oh.handleStatus)

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
