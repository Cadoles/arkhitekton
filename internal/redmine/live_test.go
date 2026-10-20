package redmine

import (
	"context"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/provider"
	"os"
	"testing"
	"time"
)

/*
 * Check against the real instance.
 *
 * Guarded behind `REDMINE_LIVE=1` and not behind the mere presence of the key: the key is set in
 * the current development environment, and making the test depend on its presence would hit the
 * **production** instance at every `make test`. A test suite that calls on a client's
 * third-party service ends up calling on it at the wrong moment.
 *
 * The rest of the tests work on fixtures captured here, which gives the same decoding coverage
 * with no network dependency.
 *
 *   REDMINE_LIVE=1 go test ./internal/redmine/ -run Live -v
 */
func liveClient(t *testing.T) (*Client, string) {
	t.Helper()
	if os.Getenv("REDMINE_LIVE") != "1" {
		t.Skip("REDMINE_LIVE not set: the check against the real instance is skipped")
	}

	client, err := New(Options{URL: os.Getenv("REDMINE_URL"), APIKey: os.Getenv("REDMINE_API_KEY")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	project := os.Getenv("REDMINE_PROJECT")
	if project == "" {
		t.Fatal("REDMINE_PROJECT is required for the real check")
	}
	return client, project
}

// Does the decoding match the format actually served? It is the only question fixtures cannot
// settle, since they come from it.
func TestLiveTheVersionsDecode(t *testing.T) {
	client, project := liveClient(t)

	versions, err := client.ListVersions(context.Background(), project)
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(versions) == 0 {
		t.Fatal("no version returned")
	}

	for _, version := range versions {
		if version.ID == "" {
			t.Errorf("version with no identifier: %+v", version)
		}
		if version.Name == "" {
			t.Errorf("version %s with no name", version.ID)
		}
		switch version.Status {
		case "open", "locked", "closed":
		default:
			t.Errorf("version %s: statut inattendu %q", version.Name, version.Status)
		}
	}
	t.Logf("%d versions decoded", len(versions))
}

// Pagination must bring back the whole set, and with no duplicate. It is the property that breaks
// silently when the sort is not stable.
func TestLiveTheRequestsPaginateWithNoDuplicate(t *testing.T) {
	client, project := liveClient(t)

	issues, err := client.ListIssues(context.Background(), provider.IssueQuery{Project: project})
	if err != nil {
		t.Fatalf("issues: %v", err)
	}
	if len(issues) == 0 {
		t.Fatal("no request returned")
	}

	seen := map[string]int{}
	withVersion := 0
	for _, issue := range issues {
		seen[issue.ID]++
		if issue.VersionID != nil {
			withVersion++
		}
		if issue.Tracker == "" || issue.Status == "" {
			t.Errorf("request %s incomplete: %+v", issue.ID, issue)
		}
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("issue %s returned %d times", id, count)
		}
	}
	t.Logf("%d requests, %d distinct, %d attached to a version",
		len(issues), len(seen), withVersion)
}

// The incremental filter must return less than everything: that is what makes the synchronisation
// worthwhile, and a value Redmine ignored would go unnoticed.
func TestLiveTheIncrementalFilterReducesTheVolume(t *testing.T) {
	client, project := liveClient(t)
	ctx := context.Background()

	all, err := client.ListIssues(ctx, provider.IssueQuery{Project: project})
	if err != nil {
		t.Fatalf("all: %v", err)
	}

	// A bound in the near future: nothing can have been modified after it.
	future, err := client.ListIssues(ctx, provider.IssueQuery{
		Project: project,
		Since:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("filtered: %v", err)
	}

	if len(future) >= len(all) {
		t.Errorf("the `updated_on` filter seems ignored: %d requests against %d with no filter",
			len(future), len(all))
	}
	t.Logf("with no filter %d, since tomorrow %d", len(all), len(future))
}
