package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func clientFor(t *testing.T, serverURL string) *Client {
	t.Helper()
	client, err := New(Options{URL: serverURL, Token: "jeton-de-test", Repository: "CNOUS/mse"})
	if err != nil {
		t.Fatalf("construction: %v", err)
	}
	return client
}

// La garantie du paquet : aucune méthode d'écriture n'atteint le réseau. Le jour où
// le dashboard posera des tags sur la forge, ce sera un autre client.
func TestAucuneEcritureNAtteintLeReseau(t *testing.T) {
	reached := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := clientFor(t, server.URL)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		request, _ := http.NewRequestWithContext(context.Background(), method,
			server.URL+"/api/v1/repos/CNOUS/mse/tags", nil)
		if _, err := client.http.Do(request); !errors.Is(err, ErrWriteForbidden) {
			t.Errorf("%s: erreur %v, attendu ErrWriteForbidden", method, err)
		}
	}
	if reached != 0 {
		t.Errorf("%d requête(s) d'écriture ont atteint le serveur", reached)
	}
}

// Le jeton voyage en en-tête, jamais dans l'URL, et ne fuit dans aucune erreur.
func TestLeJetonResteEnEnteteEtHorsDesErreurs(t *testing.T) {
	var header, query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Authorization")
		query = r.URL.RawQuery
		w.Write([]byte(`{"default_branch":"develop"}`))
	}))
	defer server.Close()

	if _, err := clientFor(t, server.URL).DefaultBranch(context.Background()); err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if header != "token jeton-de-test" {
		t.Errorf("en-tête %q", header)
	}
	if strings.Contains(query, "jeton-de-test") {
		t.Errorf("le jeton est passé dans l'URL: %q", query)
	}

	// Erreur de transport : l'URL complète est recopiée dans url.Error, le jeton ne
	// doit pas y être.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	_, err := clientFor(t, deadURL).DefaultBranch(context.Background())
	if err == nil || strings.Contains(err.Error(), "jeton-de-test") {
		t.Errorf("le jeton fuit dans l'erreur: %v", err)
	}
}

// La pagination réunit les pages et s'arrête sur la page courte.
func TestLesBranchesSePaginentJusquALaPageCourte(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		count := pageSize
		if page == 2 {
			count = 3 // page courte : la liste est finie
		}
		var payload []map[string]any
		for i := 0; i < count; i++ {
			payload = append(payload, map[string]any{
				"name":   fmt.Sprintf("branche-%d-%d", page, i),
				"commit": map[string]any{"id": "abc"},
			})
		}
		json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()

	refs, truncated, err := clientFor(t, server.URL).ListBranches(context.Background())
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	if len(refs) != pageSize+3 || pages != 2 {
		t.Errorf("%d branches en %d pages, attendu %d en 2", len(refs), pages, pageSize+3)
	}
	if truncated {
		t.Error("liste complète marquée tronquée")
	}
}

// La borne coupe une liste interminable, et le dit : une liste silencieusement
// incomplète se lirait comme exhaustive.
func TestUneListeInterminableEstBorneeEtLeDit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var payload []map[string]any
		for i := 0; i < pageSize; i++ {
			payload = append(payload, map[string]any{
				"name": fmt.Sprintf("tag-%d", i), "commit": map[string]any{"sha": "abc"},
			})
		}
		json.NewEncoder(w).Encode(payload)
	}))
	defer server.Close()

	refs, truncated, err := clientFor(t, server.URL).ListTags(context.Background())
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(refs) != maxRefs {
		t.Errorf("%d références, attendu la borne %d", len(refs), maxRefs)
	}
	if !truncated {
		t.Error("la troncature doit être signalée")
	}
}

// Sans configuration complète, pas de client : l'absence est une décision de
// déploiement, pas une panne à découvrir au premier appel.
func TestSansConfigurationLeClientNestPasConstruit(t *testing.T) {
	for _, options := range []Options{
		{},
		{URL: "https://forge.example.org", Token: "x"},
		{URL: "https://forge.example.org", Repository: "CNOUS/mse"},
		{Token: "x", Repository: "CNOUS/mse"},
	} {
		if _, err := New(options); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("%+v: erreur %v, attendu ErrNotConfigured", options, err)
		}
	}

	// Un dépôt sans owner est une faute de forme, distincte de l'absence.
	if _, err := New(Options{URL: "https://forge.example.org", Token: "x", Repository: "mse"}); err == nil {
		t.Error("un dépôt sans owner devrait être refusé")
	}
}

// Le jeton ouvre la forge entière au nom de son détenteur : en clair sur le réseau,
// hors de question.
func TestUneURLEnClairEstRefuseeHorsLocalhost(t *testing.T) {
	if _, err := New(Options{URL: "http://forge.example.org", Token: "x", Repository: "a/b"}); err == nil {
		t.Error("une URL http distante devrait être refusée")
	}
	if _, err := New(Options{URL: "http://127.0.0.1:3000", Token: "x", Repository: "a/b"}); err != nil {
		t.Errorf("127.0.0.1 devrait être admis: %v", err)
	}
}

// Un jeton refusé se distingue d'une panne, et le message oriente.
func TestUnJetonRefuseEstNommeCommeTel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := clientFor(t, server.URL).DefaultBranch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "jeton invalide ou sans droit") {
		t.Errorf("message peu explicite: %v", err)
	}
}
