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
	CookieSecure    bool                 // true for production (HTTPS)
	Logger          *slog.Logger
}

// Server is the suparship HTTP API server.
type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
}

// New creates a Server from the given Config.
func New(cfg Config) *Server {
	mux := http.NewServeMux()
	registerRoutes(mux)

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
			cfg.Logger.Info("app endpoints enabled")
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
