package registry

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// A test against a real registry. What it checks — the effective authentication
// scheme, the shape of digests, the presence of the Docker-Content-Digest header —
// cannot usefully be simulated: a stub only proves the stub is consistent.
//
// Visibly skipped without credentials.
func TestLiveRegistry(t *testing.T) {
	username := os.Getenv("TEST_REGISTRY_USERNAME")
	password := os.Getenv("TEST_REGISTRY_PASSWORD")
	if username == "" || password == "" {
		t.Skip("TEST_REGISTRY_USERNAME/PASSWORD not set: integration test skipped")
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
		t.Fatalf("tag list: %v", err)
	}
	if len(tags) == 0 {
		t.Fatal("no tag: is the test repository the right one?")
	}
	t.Logf("%d tag(s) in %s", len(tags), repository)

	digest, err := client.Digest(ctx, repository, tags[len(tags)-1])
	if err != nil {
		t.Fatalf("digest of %s: %v", tags[len(tags)-1], err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		t.Errorf("digest = %q, forme inattendue", digest)
	}

	// The operation that carries the registry's whole interest: several tags on one
	// digest say where an image was promoted.
	byDigest, err := client.TagsByDigest(ctx, repository, 20)
	if err != nil {
		t.Fatalf("indexation: %v", err)
	}

	var shared int
	for _, tagsOfDigest := range byDigest {
		if len(tagsOfDigest) > 1 {
			shared++
			t.Logf("digest shared by %v", tagsOfDigest)
		}
	}
	t.Logf("%d digest(s) over the last 20 tags, %d of them shared", len(byDigest), shared)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
