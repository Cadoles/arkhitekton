package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
)

// The dashboard must be able to run without a registry: the image inventory is then
// missing, the rest works. The error must be distinguishable from an access refusal,
// so the interface says so instead of showing a fault.
func TestOpenWithoutCredentialsIsDistinguishable(t *testing.T) {
	_, err := Open(config.RegistryConfig{URL: "https://reg.example.fr"})
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("error = %v, expected ErrNotConfigured", err)
	}
}

// `registry.project` carries the host because that is the form it takes in pipeline
// parameters; the registry's API expects only the path.
func TestProjectDropsTheHost(t *testing.T) {
	client, err := Open(config.RegistryConfig{
		URL: "https://reg.cadoles.com", Username: "u", Password: "p",
		Project: "reg.cadoles.com/cnous-mse",
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if client.Project() != "cnous-mse" {
		t.Errorf("project = %q, expected cnous-mse", client.Project())
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
		URL: server.URL, Username: "u", Password: "p", Project: "reg.example.fr/project",
	})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	return client
}

// The bearer token flow: a registry answering 401 with a challenge must be followed
// to its authorization service. Handling Basic alone would work today and fail at
// the registry's first configuration change.
func TestBearerChallengeIsFollowed(t *testing.T) {
	var tokenIssued bool

	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/service/token" {
			tokenIssued = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"bearer-token"}`))
			return
		}

		if r.Header.Get("Authorization") != "Bearer bearer-token" {
			w.Header().Set("WWW-Authenticate",
				`Bearer realm="`+"http://"+r.Host+`/service/token",service="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tags":["1.8.0","1.8.0_testing"]}`))
	}))

	tags, err := client.Tags(context.Background(), "portal")
	if err != nil {
		t.Fatalf("tag list: %v", err)
	}
	if !tokenIssued {
		t.Error("the challenge was not followed to the authorization service")
	}
	if len(tags) != 2 {
		t.Errorf("tags = %v", tags)
	}
}

// The operation that makes the registry worth reading: a promotion being a re-tag of
// the same digest, a digest's tags say where a version was promoted — including for
// production, whose cluster is out of reach.
func TestTagsByDigestGroupsPromotions(t *testing.T) {
	digests := map[string]string{
		"3.1.0":          "sha256:aaa",
		"3.1.0_unstable": "sha256:aaa",
		"3.1.0_testing":  "sha256:aaa",
		"3.0.0":          "sha256:bbb",
	}

	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			tag := r.URL.Path[len("/v2/project/portal/manifests/"):]
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
		t.Fatalf("indexing: %v", err)
	}

	if len(byDigest["sha256:aaa"]) != 3 {
		t.Errorf("digest aaa = %v, expected three tags", byDigest["sha256:aaa"])
	}
	if len(byDigest["sha256:bbb"]) != 1 {
		t.Errorf("digest bbb = %v", byDigest["sha256:bbb"])
	}
}

// An unreadable tag — deleted between the listing and the resolution — must not cost
// the others.
func TestUnreadableTagIsSkipped(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			if r.URL.Path == "/v2/project/portal/manifests/gone" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:ccc")
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"tags":["gone","present"]}`))
	}))

	byDigest, err := client.TagsByDigest(context.Background(), "portal", 0)
	if err != nil {
		t.Fatalf("indexing: %v", err)
	}
	if len(byDigest["sha256:ccc"]) != 1 || byDigest["sha256:ccc"][0] != "present" {
		t.Errorf("index = %v", byDigest)
	}
}

// The sweep is bounded: a repository with hundreds of tags would take minutes, for
// information that only concerns live versions.
func TestTagsByDigestKeepsTheMostRecent(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Docker-Content-Digest", "sha256:"+r.URL.Path[len("/v2/project/portal/manifests/"):])
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"tags":["1.0.0","2.0.0","3.0.0","4.0.0"]}`))
	}))

	byDigest, err := client.TagsByDigest(context.Background(), "portal", 2)
	if err != nil {
		t.Fatalf("indexing: %v", err)
	}
	if len(byDigest) != 2 {
		t.Fatalf("%d digest(s), expected 2: %v", len(byDigest), byDigest)
	}
	// Registries return tags in ascending order: the end is the most recent.
	if _, present := byDigest["sha256:4.0.0"]; !present {
		t.Errorf("the most recent tag is missing: %v", byDigest)
	}
}
