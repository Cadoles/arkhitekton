// Package http porte le serveur HTTP du rôle `api`.
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

// ReadinessCheck renvoie une erreur quand la dépendance n'est pas exploitable.
// Le nom apparaît tel quel dans la réponse de /readyz : il doit désigner la
// dépendance, pas la fonction qui la teste.
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

// NewServer assemble les sondes et, si fourni, le routeur applicatif monté sous
// /api/v1. Les sondes restent hors de ce préfixe : elles sont interrogées par le
// kubelet, pas par le frontend, et ne doivent pas dépendre du routage applicatif.
func NewServer(cfg config.HTTPConfig, logger *slog.Logger, api http.Handler, checks ...ReadinessCheck) *Server {
	s := &Server{cfg: cfg, logger: logger, checks: checks}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /readyz", s.handleReady)
	if api != nil {
		mux.Handle("/api/v1/", http.StripPrefix("/api/v1", api))
	}

	// L'interface est montée en dernier, sur la racine. Le motif "/" du
	// ServeMux est le moins spécifique : les sondes et l'API gardent la main sur
	// leurs préfixes, et tout le reste — y compris les routes du navigateur —
	// arrive ici.
	if ui, err := web.Handler(logger); err != nil {
		logger.Error("interface non montée", "error", err)
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

// Handler expose le routeur, pour les tests et pour un éventuel montage externe.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Run sert jusqu'à annulation du contexte, puis rend la main après extinction
// progressive. Couper net fermerait les flux SSE en cours et interromprait une
// collecte de logs de pipeline en plein vol.
func (s *Server) Run(ctx context.Context) error {
	errs := make(chan error, 1)
	go func() {
		s.logger.Info("serveur http démarré", "addr", s.cfg.Addr)
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
		s.logger.Info("extinction du serveur http", "timeout", s.cfg.ShutdownTimeout)
		return s.http.Shutdown(shutdownCtx)
	}
}

// handleHealth répond dès que le processus est vivant. Volontairement sans
// dépendance : une sonde de vivacité qui échoue parce que PostgreSQL est
// indisponible fait redémarrer un processus sain, et transforme une panne de
// base en boucle de crash.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady vérifie les dépendances : c'est le rôle de la sonde de préparation,
// qui retire l'instance du service sans la tuer.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	results := make(map[string]string, len(s.checks))
	status := http.StatusOK

	for _, check := range s.checks {
		if err := check.Check(ctx); err != nil {
			// Le détail de l'erreur va au log, pas à la réponse : /readyz est
			// souvent exposé plus largement que le reste, et un message de
			// connexion peut porter un hôte, un port, voire un identifiant.
			s.logger.Error("dépendance non prête", "dependency", check.Name, "error", err)
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
