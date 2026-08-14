package http

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Une charge utile multiligne doit être découpée en autant de `data:` : un saut
// de ligne brut terminerait l'événement, et le client ne recevrait que la
// première ligne du journal.
func TestMultilinePayloadIsSplitAcrossDataFields(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream, ok := newSSEWriter(recorder)
	if !ok {
		t.Fatal("le ResponseRecorder devrait supporter le flux")
	}

	if err := stream.event("log", "42", "première ligne\nseconde ligne"); err != nil {
		t.Fatalf("émission: %v", err)
	}

	body := recorder.Body.String()
	if count := strings.Count(body, "data: "); count != 2 {
		t.Errorf("%d champ(s) data, attendu 2:\n%s", count, body)
	}
	if !strings.Contains(body, "id: 42\n") {
		t.Errorf("identifiant de reprise absent:\n%s", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Errorf("l'événement n'est pas terminé par une ligne vide:\n%q", body)
	}
}

// nginx accumule un flux SSE par défaut : l'interface afficherait un run figé
// puis sauterait à son état final.
func TestStreamHeadersDisableBuffering(t *testing.T) {
	recorder := httptest.NewRecorder()
	if _, ok := newSSEWriter(recorder); !ok {
		t.Fatal("flux non supporté")
	}

	for header, expected := range map[string]string{
		"Content-Type":      "text/event-stream",
		"Cache-Control":     "no-cache",
		"X-Accel-Buffering": "no",
	} {
		if got := recorder.Header().Get(header); got != expected {
			t.Errorf("%s = %q, attendu %q", header, got, expected)
		}
	}
}

// Un commentaire ne doit déclencher aucun événement côté client : c'est ce qui
// permet de maintenir la connexion ouverte sans polluer l'interface.
func TestHeartbeatIsAComment(t *testing.T) {
	recorder := httptest.NewRecorder()
	stream, _ := newSSEWriter(recorder)

	if err := stream.comment("battement"); err != nil {
		t.Fatalf("battement: %v", err)
	}

	body := recorder.Body.String()
	if !strings.HasPrefix(body, ": ") {
		t.Errorf("le battement n'est pas un commentaire: %q", body)
	}
	if strings.Contains(body, "data:") || strings.Contains(body, "event:") {
		t.Errorf("le battement porte un événement: %q", body)
	}
}
