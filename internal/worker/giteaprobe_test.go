package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * The pull-request probe, against a fake forge.
 *
 * What is at stake here is what the probe must hold itself — the loop holds the rest: the
 * **deduplication key**, derived from the repository and the number, and the **fact's date**,
 * that of the opening and never that of the read.
 */

// testForge serves a list of open requests.
func testForge(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pulls") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("state") != "open" {
			t.Errorf("state requested = %q, expected open", r.URL.Query().Get("state"))
		}
		// The second page is empty: pagination stops on a short page.
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
}

const twoRequests = `[
  {"number":412,"title":"Fix the clone","html_url":"https://forge.example.fr/CNOUS/mse/pulls/412",
   "user":{"login":"vcarroy"},"head":{"ref":"fix/clone"},"base":{"ref":"main"},
   "created_at":"2026-09-03T10:30:00Z"},
  {"number":405,"title":"Older request","html_url":"https://forge.example.fr/CNOUS/mse/pulls/405",
   "user":{"login":"autre"},"head":{"ref":"feat/x"},"base":{"ref":"main"},
   "created_at":"2026-08-01T09:00:00Z"}
]`

func forgeConfig(url string) map[string]string {
	return map[string]string{"url": url, "token": "token"}
}

// An opened request becomes a fact, dated from its opening and identified by its repository
// and its number.
func TestAnOpenedRequestBecomesAFactDatedFromItsOpening(t *testing.T) {
	server := testForge(t, twoRequests)
	defer server.Close()

	facts, cursor, err := NewGiteaPullProbe(time.Second).Poll(
		context.Background(),
		store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"},
		forgeConfig(server.URL), "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	if len(facts) != 2 {
		t.Fatalf("%d facts, expected 2", len(facts))
	}

	first := facts[0]
	if first.Type != "gitea.pull_request.opened" {
		t.Errorf("type = %q — it must be prefixed by the plugin", first.Type)
	}
	if first.Subject != "#412" {
		t.Errorf("subject = %q", first.Subject)
	}
	// The **fact's** date, not the read's.
	if !first.OccurredAt.Equal(time.Date(2026, 9, 3, 10, 30, 0, 0, time.UTC)) {
		t.Errorf("instant = %v, expected that of the opening", first.OccurredAt)
	}
	// The key carries the repository **and** the number: a number alone would repeat from one
	// repository to the next, and two projects would steal each other's facts.
	if first.DedupKey != "gitea.pull_request.opened:CNOUS/mse:412" {
		t.Errorf("key = %q", first.DedupKey)
	}
	for _, field := range []string{"number", "title", "author", "source_branch", "target_branch", "url"} {
		if first.Payload[field] == "" {
			t.Errorf("payload without %q: %v", field, first.Payload)
		}
	}

	// The cursor keeps the most recent one, so that the next pass starts again from there.
	if cursor != "2026-09-03T10:30:00Z" {
		t.Errorf("cursor = %q", cursor)
	}
}

// The cursor sets aside what was opened before it.
//
// The **creation** date decides, never the update date: a request opened a month ago and
// commented on this morning is not a new fact, and treating it as one would re-notify at
// every comment.
func TestTheCursorSetsAsideWhatIsEarlier(t *testing.T) {
	server := testForge(t, twoRequests)
	defer server.Close()

	facts, _, err := NewGiteaPullProbe(time.Second).Poll(
		context.Background(),
		store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"},
		forgeConfig(server.URL), "2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	if len(facts) != 1 || facts[0].Subject != "#412" {
		t.Errorf("facts = %d, expected only the request later than the cursor", len(facts))
	}
}

// The cursor never moves backwards.
//
// A pass that sees nothing new returns the previous cursor, rather than a zero that would
// restart a bootstrap and lose the whole deduplication history.
func TestTheCursorNeverMovesBackwards(t *testing.T) {
	server := testForge(t, `[]`)
	defer server.Close()

	_, cursor, err := NewGiteaPullProbe(time.Second).Poll(
		context.Background(),
		store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"},
		forgeConfig(server.URL), "2026-09-01T00:00:00Z")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if cursor != "2026-09-01T00:00:00Z" {
		t.Errorf("cursor = %q, expected unchanged", cursor)
	}
}

// A repository with no request at all still leaves the bootstrap.
//
// Without that it would stay there forever — and the first request opened would emit nothing,
// since the loop emits nothing at bootstrap.
func TestARepositoryWithNoRequestLeavesTheBootstrap(t *testing.T) {
	server := testForge(t, `[]`)
	defer server.Close()

	_, cursor, err := NewGiteaPullProbe(time.Second).Poll(
		context.Background(),
		store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"},
		forgeConfig(server.URL), "")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if cursor == "" {
		t.Error("empty cursor: the project would stay in bootstrap forever")
	}
}

// An unconfigured forge is named as such, rather than left to the client.
//
// "Invalid URL" would send someone hunting for a typo where it is a setting that was never
// filled in.
func TestAnUnconfiguredForgeIsNamedAsSuch(t *testing.T) {
	probe := NewGiteaPullProbe(time.Second)
	project := store.Project{ID: 1, Slug: "mse", Repository: "CNOUS/mse"}

	for _, config := range []map[string]string{
		{},
		{"url": "https://forge.example.fr"},
		{"token": "token"},
	} {
		_, _, err := probe.Poll(context.Background(), project, config, "")
		if err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Errorf("config %v: %v", config, err)
		}
	}
}

// A project with no declared repository is refused by name: the probe has nothing to query,
// and saying so avoids a URL error that would not name the cause.
func TestAProjectWithNoRepositoryIsRefusedByName(t *testing.T) {
	_, _, err := NewGiteaPullProbe(time.Second).Poll(
		context.Background(), store.Project{ID: 1, Slug: "mse"},
		forgeConfig("https://forge.example.fr"), "")

	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Errorf("%v", err)
	}
}

// The probe declares itself: its plugin, and its own cursor.
func TestTheProbeDeclaresItself(t *testing.T) {
	probe := NewGiteaPullProbe(time.Second)

	if probe.PluginID() != "gitea" {
		t.Errorf("plugin = %q", probe.PluginID())
	}
	// Specific to the probe and not to the plugin: Gitea could carry others, and a shared cursor
	// would have one advance the other's.
	if probe.Source() != "gitea.pull_requests" {
		t.Errorf("source = %q", probe.Source())
	}
}
