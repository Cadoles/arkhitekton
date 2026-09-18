package redmine

import (
	"context"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

/*
 * Decoding tests on fixtures.
 *
 * The fixtures are synthetic and not captured as they are: the real responses carry request
 * subjects — the client's business content — and people's names in `assigned_to`. Versioning
 * them would amount to bringing client data and personal data into the repository, for no gain
 * in coverage.
 *
 * They do reproduce, however, the oddities actually observed on the instance, which are the only
 * reason to have fixtures:
 *
 *   - four version naming conventions, including two distinct names for what appears to be the
 *     same version (`MSE 3.1.2` closed and `v3.1.2` open);
 *   - two anomaly trackers (`Anomalie` and `Anomalie (SDN4)`);
 *   - two statuses differing only in case (`En Standby`, `En StandBy`);
 *   - `Ré-ouvert` declared closed by Redmine;
 *   - requests with no version, missing due dates and timestamps.
 *
 * Conformance to the real format is checked by the `Live` tests, run on demand against the
 * instance.
 */

func fixtureServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if offset := r.URL.Query().Get("offset"); offset != "" && offset != "0" {
			key += "?offset=" + offset
		}
		name, ok := routes[key]
		if !ok {
			t.Errorf("unexpected route: %s", key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestVersionsDecodeWithTheirFourConventions(t *testing.T) {
	server := fixtureServer(t, map[string]string{
		"/projects/mse-dev/versions.json": "versions.json",
	})

	versions, err := clientFor(t, server.URL).ListVersions(context.Background(), "mse-dev")
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) != 6 {
		t.Fatalf("%d versions, expected 6", len(versions))
	}

	// The name is kept as it is: the identity is the Redmine identifier, and normalising at write
	// time would lose the information without solving anything.
	byID := map[string]provider.Version{}
	for _, version := range versions {
		byID[version.ID] = version
	}

	if byID["801"].Name != "MSE3-1.5" || byID["803"].Name != "MSE 3.1.2" || byID["805"].Name != "v3.2.0" {
		t.Errorf("names altered: %q %q %q", byID["801"].Name, byID["803"].Name, byID["805"].Name)
	}

	// Two distinct versions carry the same number under two conventions. They must stay two rows:
	// merging them would invent data.
	if byID["803"].ID == byID["804"].ID {
		t.Error("MSE 3.1.2 et v3.1.2 doivent rester distinctes")
	}
	if byID["803"].Status != "closed" || byID["804"].Status != "open" {
		t.Errorf("statuts: 803=%q 804=%q", byID["803"].Status, byID["804"].Status)
	}

	// A missing due date and an empty timestamp are not errors.
	if byID["802"].DueDate != nil {
		t.Errorf("due date invented for 802: %v", byID["802"].DueDate)
	}
	if byID["806"].UpdatedOn != nil {
		t.Errorf("timestamp invented for 806: %v", byID["806"].UpdatedOn)
	}
	if got := byID["801"].DueDate; got == nil || got.Format("2006-01-02") != "2025-04-14" {
		t.Errorf("due date of 801: %v", got)
	}
}

func TestRequestsDecodeAcrossSeveralPages(t *testing.T) {
	server := fixtureServer(t, map[string]string{
		"/issues.json":          "issues_page1.json",
		"/issues.json?offset=5": "issues_page2.json",
	})

	issues, err := clientFor(t, server.URL).ListIssues(context.Background(),
		provider.IssueQuery{Project: "mse-dev"})
	if err != nil {
		t.Fatalf("demandes: %v", err)
	}

	// Both pages together, with neither duplicate nor omission.
	if len(issues) != 8 {
		t.Fatalf("%d requests, expected 8", len(issues))
	}
	seen := map[string]int{}
	for _, issue := range issues {
		seen[issue.ID]++
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("demande %s rendue %d fois", id, count)
		}
	}
}

func TestARequestWithNoVersionStaysWithout(t *testing.T) {
	server := fixtureServer(t, map[string]string{
		"/issues.json":          "issues_page1.json",
		"/issues.json?offset=5": "issues_page2.json",
	})

	issues, err := clientFor(t, server.URL).ListIssues(context.Background(),
		provider.IssueQuery{Project: "mse-dev"})
	if err != nil {
		t.Fatalf("demandes: %v", err)
	}

	byID := map[string]provider.Issue{}
	for _, issue := range issues {
		byID[issue.ID] = issue
	}

	if byID["9004"].VersionID != nil {
		t.Errorf("version invented for 9004: %v", *byID["9004"].VersionID)
	}
	if byID["9001"].VersionID == nil || *byID["9001"].VersionID != "805" {
		t.Errorf("version of 9001: %v", byID["9001"].VersionID)
	}
}

// Redmine's flag is taken without judgement, including when it surprises: the instance declares
// `Ré-ouvert` as closed. Mapping it to a bucket is a distinct decision, carried by an editable
// table.
func TestTheClosedFlagIsTakenWithoutJudgement(t *testing.T) {
	server := fixtureServer(t, map[string]string{
		"/issues.json":          "issues_page1.json",
		"/issues.json?offset=5": "issues_page2.json",
	})

	issues, err := clientFor(t, server.URL).ListIssues(context.Background(),
		provider.IssueQuery{Project: "mse-dev"})
	if err != nil {
		t.Fatalf("demandes: %v", err)
	}

	byID := map[string]provider.Issue{}
	for _, issue := range issues {
		byID[issue.ID] = issue
	}

	reouverte := byID["9005"]
	if reouverte.Status != "Ré-ouvert" || !reouverte.StatusClosed {
		t.Errorf("9005: status %q closed=%v — Redmine's flag must be kept",
			reouverte.Status, reouverte.StatusClosed)
	}

	// The two anomaly trackers are distinct: counting anomalies by equality with "Anomalie" would
	// miss one in two.
	if byID["9002"].Tracker != "Anomalie" || byID["9003"].Tracker != "Anomalie (SDN4)" {
		t.Errorf("trackers: %q et %q", byID["9002"].Tracker, byID["9003"].Tracker)
	}

	// Two statuses differing only in case coexist in the instance.
	if byID["9008"].Status != "En StandBy" {
		t.Errorf("9008: statut %q", byID["9008"].Status)
	}
}

// `status_id=*` is essential: without it Redmine returns only the open requests, and a finished
// version would look empty — a silent data loss.
func TestTheRequestAsksForEveryStatusAndAStableSort(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte(`{"issues":[],"total_count":0}`))
	}))
	defer server.Close()

	if _, err := clientFor(t, server.URL).ListIssues(context.Background(),
		provider.IssueQuery{Project: "mse-dev", Since: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("demandes: %v", err)
	}

	for _, expected := range []string{
		"status_id=%2A",
		"sort=updated_on",
		"project_id=mse-dev",
		"updated_on=%3E%3D2026-08-01T00%3A00%3A00Z",
	} {
		if !strings.Contains(query, expected) {
			t.Errorf("the request does not carry %s: %s", expected, query)
		}
	}
}

// An empty page while the total announces more would signal an inconsistency on Redmine's side.
// Looping on it would run forever.
func TestAnEmptyPageStopsThePagination(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Write([]byte(`{"issues":[],"total_count":5000}`))
	}))
	defer server.Close()

	issues, err := clientFor(t, server.URL).ListIssues(context.Background(),
		provider.IssueQuery{Project: "mse-dev"})
	if err != nil {
		t.Fatalf("demandes: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("%d demandes rendues", len(issues))
	}
	if calls != 1 {
		t.Errorf("%d calls: the pagination must stop on an empty page", calls)
	}
}

// A project left unset is a fault of the caller, detected before any network call.
func TestAMissingProjectIsRefusedBeforeAnyCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no call should be made")
	}))
	defer server.Close()

	client := clientFor(t, server.URL)
	if _, err := client.ListVersions(context.Background(), ""); err == nil {
		t.Error("ListVersions should refuse an empty project")
	}
	if _, err := client.ListIssues(context.Background(), provider.IssueQuery{}); err == nil {
		t.Error("ListIssues should refuse an empty project")
	}
}
