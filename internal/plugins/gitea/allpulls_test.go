package gitea

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/provider"
)

func pullJSON(number int, state string, merged bool, updated time.Time) string {
	return fmt.Sprintf(`{"number":%d,"title":"PR %d","state":%q,"html_url":"https://forge.test/pulls/%d",
		"user":{"login":"dev"},"head":{"ref":"feature-%d","sha":"sha%d"},"base":{"ref":"main"},
		"merged":%t,"created_at":%q,"updated_at":%q}`,
		number, number, state, number, number, number, merged,
		updated.Add(-time.Hour).Format(time.RFC3339), updated.Format(time.RFC3339))
}

// Every state is read with its head, and the read stops at the first request the previous pass saw.
func TestPullRequestsAreReadUntilTheCursor(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Write([]byte("[" + strings.Join([]string{
			pullJSON(3, "open", false, now),
			pullJSON(2, "closed", true, now.Add(-time.Hour)),
			pullJSON(1, "closed", false, now.Add(-2*time.Hour)),
			pullJSON(0, "closed", false, now.Add(-3*time.Hour)),
		}, ",") + "]"))
	}))
	defer server.Close()

	pulls, truncated, err := clientFor(t, server.URL).ListPullRequests(context.Background(), now.Add(-150*time.Minute))
	if err != nil || truncated {
		t.Fatalf("pulls: %v, truncated %v", err, truncated)
	}
	if !strings.Contains(query, "state=all") || !strings.Contains(query, "sort=recentupdate") {
		t.Errorf("query = %s", query)
	}
	if len(pulls) != 3 {
		t.Fatalf("pulls = %+v, want the three updated after the cursor", pulls)
	}
	want := []string{provider.PullOpen, provider.PullMerged, provider.PullClosed}
	for index, pull := range pulls {
		if pull.State != want[index] {
			t.Errorf("pull #%d state = %s, want %s", pull.Number, pull.State, want[index])
		}
	}
	if pulls[0].HeadSHA != "sha3" || pulls[0].SourceBranch != "feature-3" || pulls[0].Author != "dev" ||
		pulls[0].URL != "https://forge.test/pulls/3" {
		t.Errorf("pull = %+v", pulls[0])
	}
}

// A repository with more requests than one read takes stops at the bound and says so.
func TestAFirstReadIsBoundedAndSaysSo(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries := make([]string, 0, pageSize)
		for index := range pageSize {
			entries = append(entries, pullJSON(index, "open", false, now.Add(-time.Duration(index)*time.Minute)))
		}
		w.Write([]byte("[" + strings.Join(entries, ",") + "]"))
	}))
	defer server.Close()

	pulls, truncated, err := clientFor(t, server.URL).ListPullRequests(context.Background(), time.Time{})
	if err != nil || !truncated || len(pulls) != maxFollowedPulls {
		t.Errorf("pulls = %d, truncated %v, %v", len(pulls), truncated, err)
	}
}
