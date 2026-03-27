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
)

const shutdownTimeout = 5 * time.Second

// Config holds server configuration.
type Config struct {
	Addr        string
	UIDir       string   // optional: path to built frontend assets
	CORSOrigins []string // optional: allowed CORS origins
	Logger      *slog.Logger
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
