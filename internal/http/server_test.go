package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

func testServer(checks ...ReadinessCheck) *Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(config.HTTPConfig{Addr: ":0", ShutdownTimeout: time.Second}, logger, nil, checks...)
}

func do(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// La vivacité ne doit dépendre d'aucune dépendance externe : sinon une panne de
// PostgreSQL fait redémarrer des processus sains et la transforme en crashloop.
func TestHealthzIgnoresFailingDependencies(t *testing.T) {
	s := testServer(ReadinessCheck{
		Name:  "postgres",
		Check: func(context.Context) error { return errors.New("connexion refusée") },
	})

	if rec := do(t, s, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, attendu 200 malgré une dépendance en panne", rec.Code)
	}
}

func TestReadyzReportsOKWhenAllChecksPass(t *testing.T) {
	s := testServer(
		ReadinessCheck{Name: "postgres", Check: func(context.Context) error { return nil }},
		ReadinessCheck{Name: "minio", Check: func(context.Context) error { return nil }},
	)

	rec := do(t, s, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz = %d, attendu 200", rec.Code)
	}

	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("réponse illisible: %v", err)
	}
	if body.Status != "ready" || body.Checks["postgres"] != "ok" || body.Checks["minio"] != "ok" {
		t.Errorf("corps inattendu: %+v", body)
	}
}

func TestReadyzReturns503WhenADependencyFails(t *testing.T) {
	s := testServer(
		ReadinessCheck{Name: "postgres", Check: func(context.Context) error { return nil }},
		ReadinessCheck{Name: "minio", Check: func(context.Context) error { return errors.New("timeout") }},
	)

	rec := do(t, s, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, attendu 503", rec.Code)
	}
}

// Un message de connexion peut porter un hôte, un port, voire un identifiant.
// /readyz est souvent exposé plus largement que le reste de l'API.
func TestReadyzDoesNotLeakErrorDetails(t *testing.T) {
	s := testServer(ReadinessCheck{
		Name: "postgres",
		Check: func(context.Context) error {
			return errors.New("dial tcp 10.42.0.7:5432: mot de passe refusé pour mse_app")
		},
	})

	body := do(t, s, "/readyz").Body.String()

	for _, leak := range []string{"10.42.0.7", "5432", "mse_app", "mot de passe"} {
		if strings.Contains(body, leak) {
			t.Errorf("la réponse divulgue %q: %s", leak, body)
		}
	}
}

func TestRunStopsOnContextCancellation(t *testing.T) {
	s := testServer()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("extinction en erreur: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("le serveur ne s'est pas éteint après annulation du contexte")
	}
}
