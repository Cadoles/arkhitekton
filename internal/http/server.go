// Package http carries the HTTP server of the `api` role. The routes are listed in
// docs/07-http-api.md.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/web"
)

// ReadinessCheck returns an error when the dependency is unusable. The name appears as it is
// in /readyz's response: it must name the dependency, not the function that tests it.
type ReadinessCheck struct {
	Name  string
	Check func(context.Context) error
}

// Server assemble le routage et le cycle de vie du serveur HTTP.
type Server struct {
	cfg    config.HTTPConfig
	logger *slog.Logger
	checks []ReadinessCheck
	http   *http.Server
}

// NewServer assembles the probes and, when supplied, the application router mounted under
// /api/v1. The probes stay outside that prefix: they are queried by the kubelet, not by the
// front end, and must not depend on application routing.
func NewServer(cfg config.HTTPConfig, logger *slog.Logger, api http.Handler, checks ...ReadinessCheck) *Server {
	s := &Server{cfg: cfg, logger: logger, checks: checks}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	if api != nil {
		mux.Handle("/api/v1/", http.StripPrefix("/api/v1", api))
	}

	// The interface is mounted last, on the root. The ServeMux's "/" pattern is the least
	// specific: the probes and the API keep their prefixes, and everything else — including the
	// browser's routes — arrives here.
	if ui, err := web.Handler(logger); err != nil {
		logger.Error("interface not mounted", "error", err)
	} else {
		mux.Handle("/", ui)
	}

	s.http = &http.Server{
		Addr:         cfg.Addr,
		Handler:      mux,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	}
	return s
}

// Handler exposes the router, for tests and for a possible external mount.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Run serves until the context is cancelled, then returns after a graceful shutdown. Cutting
// abruptly would close the SSE streams in flight and interrupt a pipeline log collection
// mid-air.
func (s *Server) Run(ctx context.Context) error {
	errs := make(chan error, 1)
	go func() {
		s.logger.Info("http server started", "addr", s.cfg.Addr)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
		defer cancel()
		s.logger.Info("http server shutting down", "timeout", s.cfg.ShutdownTimeout)
		return s.http.Shutdown(shutdownCtx)
	}
}

// handleHealth answers as soon as the process is alive. Deliberately without a dependency: a
// liveness probe that fails because PostgreSQL is unavailable restarts a healthy process, and
// turns a database outage into a crash loop.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady checks the dependencies: that is the readiness probe's role, which removes the
// instance from the Service without killing it.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	results := make(map[string]string, len(s.checks))
	status := http.StatusOK

	for _, check := range s.checks {
		if err := check.Check(ctx); err != nil {
			// The error's detail goes to the log, not to the response: /readyz is often exposed more
			// widely than the rest, and a connection message can carry a host, a port, even a credential.
			s.logger.Error("dependency not ready", "dependency", check.Name, "error", err)
			results[check.Name] = "erreur"
			status = http.StatusServiceUnavailable
			continue
		}
		results[check.Name] = "ok"
	}

	writeJSON(w, status, map[string]any{"status": statusLabel(status), "checks": results})
}

func statusLabel(status int) string {
	if status == http.StatusOK {
		return "ready"
	}
	return "degraded"
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
