package webhook

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

/*
 * Le contrôle de cible.
 *
 * C'est la partie du dépôt où se tromper coûte le plus cher : une URL saisie dans
 * l'interface devient une requête émise depuis le cluster, par un processus qui
 * porte un jeton de compte de service. Les tests portent donc d'abord sur ce qui
 * doit être **refusé**.
 */

func clientFor(options Options) *Client {
	if options.Timeout == 0 {
		options.Timeout = 2 * 1e9
	}
	return New(options)
}

// Les cibles interdites, et la plus dangereuse en tête.
//
// `169.254.169.254` sert les métadonnées d'instance chez tous les hébergeurs,
// identifiants compris. Une falsification qui l'atteint ne vole pas une donnée
// applicative : elle vole le cluster.
func TestLesCiblesInterditesSontRefusees(t *testing.T) {
	client := clientFor(Options{})

	cas := map[string]string{
		"métadonnées cloud":     "https://169.254.169.254/latest/meta-data/",
		"métadonnées en IPv6":   "https://[::ffff:169.254.169.254]/latest/",
		"boucle locale":         "https://127.0.0.1/hook",
		"boucle locale nommée":  "https://localhost/hook",
		"boucle locale IPv6":    "https://[::1]/hook",
		"plage privée 10":       "https://10.0.0.5/hook",
		"plage privée 172":      "https://172.16.3.1/hook",
		"plage privée 192":      "https://192.168.1.10/hook",
		"report d'opérateur":    "https://100.64.1.1/hook",
		"adresse non spécifiée": "https://0.0.0.0/hook",
	}

	for nom, cible := range cas {
		_, err := client.Post(context.Background(), cible, nil, []byte(`{}`))
		if !errors.Is(err, ErrBlockedTarget) {
			t.Errorf("%s (%s): %v, attendu ErrBlockedTarget", nom, cible, err)
		}
	}
}

// Le motif accompagne le refus : « refusé » sans raison envoie chercher une
// panne, alors que « lien-local » se corrige en une lecture.
func TestLeRefusNommeSaRaison(t *testing.T) {
	_, err := clientFor(Options{}).Post(
		context.Background(), "https://169.254.169.254/", nil, []byte(`{}`))

	if err == nil {
		t.Fatal("cible de métadonnées acceptée")
	}
	if !strings.Contains(err.Error(), "métadonnées") {
		t.Errorf("message %q — il doit nommer ce qui est refusé", err)
	}
}

// Le clair est refusé par défaut : la charge porte le tag d'une release et le
// jeton qui l'authentifie, et tout cela traverserait un réseau lisible.
func TestLeClairEstRefuseParDefaut(t *testing.T) {
	_, err := clientFor(Options{}).Post(
		context.Background(), "http://example.test/hook", nil, []byte(`{}`))

	if !errors.Is(err, ErrInsecureTarget) {
		t.Fatalf("%v, attendu ErrInsecureTarget", err)
	}
	if !strings.Contains(err.Error(), "clair") {
		t.Errorf("message %q — il doit dire ce qui fuirait", err)
	}
}

// Un schéma qui n'est pas HTTP est refusé sans même être composé : `file://` et
// `gopher://` sont des vecteurs classiques de falsification.
func TestUnSchemaEtrangerEstRefuse(t *testing.T) {
	client := clientFor(Options{AllowInsecure: true, AllowPrivate: true})

	for _, cible := range []string{
		"file:///etc/passwd", "gopher://example.test/", "ftp://example.test/",
	} {
		if _, err := client.Post(context.Background(), cible, nil, nil); !errors.Is(err, ErrBlockedTarget) {
			t.Errorf("cible %q: %v", cible, err)
		}
	}
}

/*
 * Ce que l'autorisation explicite ouvre — et ce qu'elle n'ouvre pas.
 */

// Un déploiement local a besoin de sa boucle locale, et le déclare.
func TestLAutorisationExpliciteOuvreLesCiblesPrivees(t *testing.T) {
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer jeton" {
			t.Errorf("en-tête d'authentification absent: %v", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("reçu"))
	}))
	defer serveur.Close()

	client := clientFor(Options{AllowPrivate: true, AllowInsecure: true})
	response, err := client.Post(context.Background(), serveur.URL,
		map[string]string{"Authorization": "Bearer jeton"}, []byte(`{"tag":"1.8.0"}`))
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	if !response.OK() || response.Status != http.StatusAccepted {
		t.Errorf("réponse = %+v", response)
	}
	if response.Body != "reçu" {
		t.Errorf("corps = %q", response.Body)
	}
}

// **Elle ne rouvre jamais le lien-local.** Les métadonnées d'instance ne sont pas
// une cible qu'un déploiement veut atteindre, et l'autorisation générale ne doit
// pas les rendre accessibles par la bande.
func TestLAutorisationPriveeNOuvrePasLeLienLocal(t *testing.T) {
	client := clientFor(Options{AllowPrivate: true, AllowInsecure: true})

	_, err := client.Post(context.Background(), "http://169.254.169.254/", nil, nil)
	if !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("%v, attendu ErrBlockedTarget malgré l'autorisation privée", err)
	}
}

/*
 * La redirection, contournement évident du contrôle.
 */

// Aucune redirection n'est suivie : le destinataire répond 302 vers les
// métadonnées, et un client complaisant y va.
func TestAucuneRedirectionNestSuivie(t *testing.T) {
	var atteint bool
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/suite" {
			atteint = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/suite", http.StatusFound)
	}))
	defer serveur.Close()

	client := clientFor(Options{AllowPrivate: true, AllowInsecure: true})
	_, err := client.Post(context.Background(), serveur.URL+"/depart", nil, []byte(`{}`))

	if !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("%v, attendu un refus de redirection", err)
	}
	if atteint {
		t.Error("la redirection a été suivie")
	}
}

/*
 * Ce que le client rapporte, et ce qu'il tait.
 */

// Un refus du destinataire est un **constat**, pas une panne du dashboard : la
// réponse est rendue avec son code, et l'appelant décide.
func TestUnRefusDuDestinataireEstUnConstat(t *testing.T) {
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("jeton invalide"))
	}))
	defer serveur.Close()

	client := clientFor(Options{AllowPrivate: true, AllowInsecure: true})
	response, err := client.Post(context.Background(), serveur.URL, nil, []byte(`{}`))
	if err != nil {
		t.Fatalf("un refus du destinataire ne doit pas être une erreur: %v", err)
	}
	if response.OK() {
		t.Error("un 403 est annoncé comme réussi")
	}
	if response.Status != http.StatusForbidden || response.Body != "jeton invalide" {
		t.Errorf("réponse = %+v", response)
	}
}

// Un 3xx n'est pas un succès : la redirection n'ayant pas été suivie, personne
// n'a reçu la charge.
func TestUneRedirectionNestPasUnSucces(t *testing.T) {
	if (Response{Status: http.StatusFound}).OK() {
		t.Error("un 302 est annoncé comme réussi")
	}
	for _, code := range []int{200, 201, 202, 204} {
		if !(Response{Status: code}).OK() {
			t.Errorf("%d n'est pas annoncé comme réussi", code)
		}
	}
}

// La réponse est bornée : un destinataire hostile ferait sinon grossir la mémoire
// du worker par un corps sans fin.
func TestLaReponseEstBornee(t *testing.T) {
	serveur := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 100_000)))
	}))
	defer serveur.Close()

	client := clientFor(Options{AllowPrivate: true, AllowInsecure: true, MaxResponseBytes: 64})
	response, err := client.Post(context.Background(), serveur.URL, nil, nil)
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	if len(response.Body) > 64 {
		t.Errorf("corps de %d octets, borné à 64", len(response.Body))
	}
}

// L'URL est expurgée dans les messages d'erreur : un webhook de messagerie porte
// souvent son jeton dans sa chaîne de requête, et un message d'erreur finit dans
// un journal.
func TestLURLEstExpurgeeDansLesErreurs(t *testing.T) {
	_, err := clientFor(Options{}).Post(context.Background(),
		"http://example.test/hook?token=secret-a-ne-pas-journaliser", nil, nil)

	if err == nil {
		t.Fatal("appel en clair accepté")
	}
	if strings.Contains(err.Error(), "secret-a-ne-pas-journaliser") {
		t.Errorf("le jeton transparaît dans %q", err)
	}
}

// Une URL sans hôte est refusée plutôt que composée : `https:///hook` n'atteint
// personne, et l'erreur réseau ne dirait pas pourquoi.
func TestUneURLSansHoteEstRefusee(t *testing.T) {
	for _, cible := range []string{"", "   ", "https:///hook", "pas une url"} {
		if _, err := clientFor(Options{}).Post(
			context.Background(), cible, nil, nil); !errors.Is(err, ErrBlockedTarget) {
			t.Errorf("cible %q: %v", cible, err)
		}
	}
}
