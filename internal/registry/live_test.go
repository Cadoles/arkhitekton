package registry

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/config"
)

// Test contre un registre réel. Ce qu'il vérifie — le schéma d'authentification
// effectif, la forme des digests, la présence de l'en-tête Docker-Content-Digest
// — ne se simule pas utilement : un bouchon ne prouve que la cohérence du
// bouchon.
//
// Ignoré visiblement sans identifiants.
func TestLiveRegistry(t *testing.T) {
	username := os.Getenv("TEST_REGISTRY_USERNAME")
	password := os.Getenv("TEST_REGISTRY_PASSWORD")
	if username == "" || password == "" {
		t.Skip("TEST_REGISTRY_USERNAME/PASSWORD non définis: test d'intégration ignoré")
	}

	client, err := Open(config.RegistryConfig{
		URL:      envOr("TEST_REGISTRY_URL", "https://reg.cadoles.com"),
		Username: username,
		Password: password,
		Project:  envOr("TEST_REGISTRY_PROJECT", "reg.cadoles.com/cnous-mse"),
	})
	if err != nil {
		t.Fatalf("ouverture: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	repository := envOr("TEST_REGISTRY_REPOSITORY", "portal")

	tags, err := client.Tags(ctx, repository)
	if err != nil {
		t.Fatalf("liste des tags: %v", err)
	}
	if len(tags) == 0 {
		t.Fatal("aucun tag: le dépôt de test est-il le bon ?")
	}
	t.Logf("%d tag(s) dans %s", len(tags), repository)

	digest, err := client.Digest(ctx, repository, tags[len(tags)-1])
	if err != nil {
		t.Fatalf("digest de %s: %v", tags[len(tags)-1], err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("digest = %q, forme inattendue", digest)
	}

	// L'opération qui porte tout l'intérêt du registre : plusieurs tags sur un
	// même digest disent où une image a été promue.
	byDigest, err := client.TagsByDigest(ctx, repository, 20)
	if err != nil {
		t.Fatalf("indexation: %v", err)
	}

	var shared int
	for _, tagsOfDigest := range byDigest {
		if len(tagsOfDigest) > 1 {
			shared++
			t.Logf("digest partagé par %v", tagsOfDigest)
		}
	}
	t.Logf("%d digest(s) sur les 20 derniers tags, dont %d partagés", len(byDigest), shared)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
