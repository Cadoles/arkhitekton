package redmine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// La garantie que ce paquet doit tenir : aucune méthode d'écriture n'atteint le
// réseau. Vérifiée sur un serveur qui compte ce qu'il reçoit — si une requête
// passait, le compteur le dirait.
func TestAucuneEcritureNAtteintLeReseau(t *testing.T) {
	reached := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := clientFor(t, server.URL)

	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, "PROPFIND", "TRACE",
	} {
		request, err := http.NewRequestWithContext(context.Background(), method, server.URL+"/issues.json", nil)
		if err != nil {
			t.Fatalf("requête %s: %v", method, err)
		}

		_, err = client.http.Do(request)
		if !errors.Is(err, ErrWriteForbidden) {
			t.Errorf("%s: erreur %v, attendu ErrWriteForbidden", method, err)
		}
	}

	if reached != 0 {
		t.Errorf("%d requête(s) d'écriture ont atteint le serveur", reached)
	}
}

// La lecture, elle, doit passer : une garde qui bloque tout ne protège rien, elle
// casse.
func TestLaLectureEstAutorisee(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"user":{"id":7,"login":"lecteur","admin":false}}`))
	}))
	defer server.Close()

	account, err := clientFor(t, server.URL).CheckAccess(context.Background())
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if account.Login != "lecteur" || account.ID != 7 {
		t.Errorf("compte %+v", account)
	}
}

// La clé voyage en en-tête et jamais dans l'URL : une chaîne de requête est
// journalisée par le serveur et recopiée dans les messages d'erreur du client Go.
func TestLaCleVoyageEnEnteteEtJamaisDansLURL(t *testing.T) {
	var seenHeader, seenQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("X-Redmine-API-Key")
		seenQuery = r.URL.RawQuery
		w.Write([]byte(`{"user":{"id":1,"login":"x"}}`))
	}))
	defer server.Close()

	if _, err := clientFor(t, server.URL).CheckAccess(context.Background()); err != nil {
		t.Fatalf("lecture: %v", err)
	}

	if seenHeader != "cle-de-test" {
		t.Errorf("en-tête d'authentification %q", seenHeader)
	}
	if strings.Contains(seenQuery, "cle-de-test") {
		t.Errorf("la clé est passée dans la chaîne de requête: %q", seenQuery)
	}
}

// La clé ne doit apparaître dans aucun message d'erreur : c'est le chemin par
// lequel un secret finit le plus souvent dans un journal.
func TestAucuneErreurNePorteLaCle(t *testing.T) {
	// Un serveur fermé aussitôt, pour provoquer une erreur de transport — le cas où
	// le client Go recopie l'URL complète dans son `url.Error`.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	client := clientFor(t, url)

	_, err := client.CheckAccess(context.Background())
	if err == nil {
		t.Fatal("un serveur fermé devrait produire une erreur")
	}
	if strings.Contains(err.Error(), "cle-de-test") {
		t.Errorf("la clé fuit dans l'erreur: %v", err)
	}

	// Le refus d'écriture non plus ne doit pas la porter.
	request, _ := http.NewRequest(http.MethodPost, url+"/issues.json", nil)
	_, err = client.http.Do(request)
	if err != nil && strings.Contains(err.Error(), "cle-de-test") {
		t.Errorf("la clé fuit dans le refus d'écriture: %v", err)
	}
}

// Sans URL ni clé, le client refuse d'exister plutôt que d'échouer au premier
// appel : l'absence de configuration est une décision de déploiement.
func TestSansConfigurationLeClientNestPasConstruit(t *testing.T) {
	for _, options := range []Options{
		{},
		{URL: "https://redmine.example.org"},
		{APIKey: "cle"},
		{URL: "   ", APIKey: "   "},
	} {
		if _, err := New(options); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("%+v: erreur %v, attendu ErrNotConfigured", options, err)
		}
	}
}

// La clé voyage à chaque requête : en clair sur le réseau, elle est lisible par
// tout intermédiaire, et il s'agit d'un accès à la production.
func TestUneURLEnClairEstRefuseeHorsLocalhost(t *testing.T) {
	if _, err := New(Options{URL: "http://redmine.example.org", APIKey: "cle"}); err == nil {
		t.Error("une URL http distante devrait être refusée")
	}

	// localhost reste admis : les tests et un tunnel local n'exposent rien.
	if _, err := New(Options{URL: "http://127.0.0.1:3000", APIKey: "cle"}); err != nil {
		t.Errorf("127.0.0.1 devrait être admis: %v", err)
	}
}

// Une clé refusée par Redmine se distingue d'une panne : c'est presque toujours une
// clé révoquée ou sans droit sur le projet, et le message doit le dire.
func TestUneCleRefuseeEstNommeeCommeTelle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := clientFor(t, server.URL).CheckAccess(context.Background())
	if err == nil {
		t.Fatal("un 401 devrait produire une erreur")
	}
	if !strings.Contains(err.Error(), "clé invalide ou sans droit") {
		t.Errorf("message peu explicite: %v", err)
	}
}

func clientFor(t *testing.T, serverURL string) *Client {
	t.Helper()
	client, err := New(Options{URL: serverURL, APIKey: "cle-de-test"})
	if err != nil {
		t.Fatalf("construction: %v", err)
	}
	return client
}
