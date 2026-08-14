package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
)

// Le dashboard doit pouvoir tourner sans registre : l'inventaire des images
// manque alors, le reste fonctionne. L'erreur doit être distinguable d'un refus
// d'accès, pour que l'interface le dise au lieu d'afficher une panne.
func TestOpenWithoutCredentialsIsDistinguishable(t *testing.T) {
	_, err := Open(config.RegistryConfig{URL: "https://reg.example.fr"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("erreur = %v, attendu ErrNotConfigured", err)
	}
}

// `registry.project` porte l'hôte parce que c'est sous cette forme qu'il entre
// dans les paramètres de pipeline ; l'API du registre n'attend que le chemin.
func TestProjectDropsTheHost(t *testing.T) {
	client, err := Open(config.RegistryConfig{
		URL: "https://reg.cadoles.com", Username: "u", Password: "p",
		Project: "reg.cadoles.com/cnous-mse",
	})
	if err != nil {
		t.Fatalf("ouverture: %v", err)
	}
	if client.Project() != "cnous-mse" {
		t.Errorf("projet = %q, attendu cnous-mse", client.Project())
	}
}

func TestParseChallenge(t *testing.T) {
	parameters := parseChallenge(`Bearer realm="https://reg.example.fr/service/token",service="harbor-registry",scope="repository:a/b:pull"`)

	if parameters["realm"] != "https://reg.example.fr/service/token" {
		t.Errorf("realm = %q", parameters["realm"])
	}
	if parameters["service"] != "harbor-registry" {
		t.Errorf("service = %q", parameters["service"])
	}
	if parameters["scope"] != "repository:a/b:pull" {
		t.Errorf("scope = %q", parameters["scope"])
	}
}

func testClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := Open(config.RegistryConfig{
		URL: server.URL, Username: "u", Password: "p", Project: "reg.example.fr/projet",
	})
	if err != nil {
		t.Fatalf("ouverture: %v", err)
	}
	return client
}

// Le flux de jeton porteur : un registre qui répond 401 avec un défi doit être
// suivi jusqu'au service d'autorisation. Ne traiter que Basic fonctionnerait
// aujourd'hui et échouerait au premier changement de configuration du registre.
func TestBearerChallengeIsFollowed(t *testing.T) {
	var tokenIssued bool

	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/service/token" {
			tokenIssued = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"jeton-porteur"}`))
			return
		}

		if r.Header.Get("Authorization") != "Bearer jeton-porteur" {
			w.Header().Set("WWW-Authenticate",
				`Bearer realm="`+"http://"+r.Host+`/service/token",service="registre"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tags":["1.8.0","1.8.0_testing"]}`))
	}))

	tags, err := client.Tags(context.Background(), "portal")
	if err != nil {
		t.Fatalf("liste des tags: %v", err)
	}
	if !tokenIssued {
		t.Error("le défi n'a pas été suivi jusqu'au service d'autorisation")
	}
	if len(tags) != 2 {
		t.Errorf("tags = %v", tags)
	}
}

// L'opération qui donne son intérêt au registre : une promotion étant un re-tag
// du même digest, les tags d'un digest disent où une version a été promue — y
// compris pour la production, dont le cluster est hors de portée.
func TestTagsByDigestGroupsPromotions(t *testing.T) {
	digests := map[string]string{
		"3.1.0":          "sha256:aaa",
		"3.1.0_unstable": "sha256:aaa",
		"3.1.0_testing":  "sha256:aaa",
		"3.0.0":          "sha256:bbb",
	}

	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			tag := r.URL.Path[len("/v2/projet/portal/manifests/"):]
			digest, known := digests[tag]
			if !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", digest)
			w.WriteHeader(http.StatusOK)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tags":["3.0.0","3.1.0","3.1.0_testing","3.1.0_unstable"]}`))
	}))

	byDigest, err := client.TagsByDigest(context.Background(), "portal", 0)
	if err != nil {
		t.Fatalf("indexation: %v", err)
	}

	if len(byDigest["sha256:aaa"]) != 3 {
		t.Errorf("digest aaa = %v, attendu trois tags", byDigest["sha256:aaa"])
	}
	if len(byDigest["sha256:bbb"]) != 1 {
		t.Errorf("digest bbb = %v", byDigest["sha256:bbb"])
	}
}

// Un tag illisible — supprimé entre le listing et la résolution — ne doit pas
// faire perdre les autres.
func TestUnreadableTagIsSkipped(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			if r.URL.Path == "/v2/projet/portal/manifests/disparu" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:ccc")
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"tags":["disparu","present"]}`))
	}))

	byDigest, err := client.TagsByDigest(context.Background(), "portal", 0)
	if err != nil {
		t.Fatalf("indexation: %v", err)
	}
	if len(byDigest["sha256:ccc"]) != 1 || byDigest["sha256:ccc"][0] != "present" {
		t.Errorf("index = %v", byDigest)
	}
}

// Le balayage est borné : un dépôt de plusieurs centaines de tags prendrait des
// minutes, pour une information qui ne concerne que les versions vivantes.
func TestTagsByDigestKeepsTheMostRecent(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Docker-Content-Digest", "sha256:"+r.URL.Path[len("/v2/projet/portal/manifests/"):])
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"tags":["1.0.0","2.0.0","3.0.0","4.0.0"]}`))
	}))

	byDigest, err := client.TagsByDigest(context.Background(), "portal", 2)
	if err != nil {
		t.Fatalf("indexation: %v", err)
	}
	if len(byDigest) != 2 {
		t.Fatalf("%d digest(s), attendu 2: %v", len(byDigest), byDigest)
	}
	// Les registres rendent les tags par ordre croissant : la fin est la plus
	// récente.
	if _, present := byDigest["sha256:4.0.0"]; !present {
		t.Errorf("le tag le plus récent est absent: %v", byDigest)
	}
}
