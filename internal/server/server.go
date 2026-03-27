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
	"github.com/suparcloud/suparship/internal/project"
	"github.com/suparcloud/suparship/internal/rbac"
	"github.com/suparcloud/suparship/internal/session"
	"github.com/suparcloud/suparship/internal/tpl"
)

const shutdownTimeout = 5 * time.Second

// Config holds server configuration.
type Config struct {
	Addr          string
	UIDir         string              // optional: path to built frontend assets
	CORSOrigins   []string            // optional: allowed CORS origins
	Authenticator auth.Authenticator  // optional: enables auth endpoints when set
	OrgProvider   rbac.OrgProvider    // optional: enables RBAC-protected routes when set
	Templates     []*tpl.Template     // optional: pre-loaded templates for /api/v1/templates
	ProjectStore  project.Store       // optional: enables service creation when set
	CookieSecure  bool                // true for production (HTTPS)
	Logger        *slog.Logger
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
			auth:        ah,
			orgProvider: cfg.OrgProvider,
		}
		if cfg.ProjectStore != nil {
			rh.serviceHandler = newServiceHandler(cfg.ProjectStore, cfg.Templates)
			cfg.Logger.Info("service creation endpoint enabled")
		}
		rh.registerRoutes(mux)
		cfg.Logger.Info("RBAC-protected routes enabled")
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

	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.Addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
		},
		logger: cfg.Logger,
	}
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
