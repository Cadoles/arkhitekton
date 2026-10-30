package gitea

import (
	"context"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
)

var received = time.Date(2026, 10, 30, 14, 0, 0, 0, time.UTC)

func receive(t *testing.T, event, body string) ([]capability.Observation, error) {
	t.Helper()
	return receiver{now: func() time.Time { return received }}.Receive(context.Background(),
		target(forgeConfig("https://forge.example.fr")), capability.Delivery{Event: event, Body: []byte(body)})
}

const branchPush = `{
  "ref": "refs/heads/k8s-unstable",
  "before": "1111111111111111111111111111111111111111",
  "after": "2222222222222222222222222222222222222222",
  "compare_url": "https://forge.example.fr/CNOUS/mse/compare/1111...2222",
  "total_commits": 2,
  "head_commit": {"message": "fix: the clone\n\nLonger body."},
  "repository": {"full_name": "CNOUS/mse"},
  "pusher": {"login": "vcarroy"}
}`

// A push becomes a fact carrying the pushed commit as the revision a pipeline step takes, keyed on
// what was pushed: a delivery replayed, by the forge or by anyone, is the same fact.
func TestAPushBecomesAFactKeyedOnWhatWasPushed(t *testing.T) {
	observed, err := receive(t, "push", branchPush)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 {
		t.Fatalf("%d observations", len(observed))
	}
	fact := observed[0]
	expected := map[string]string{
		"revision": "2222222222222222222222222222222222222222", "branch": "k8s-unstable",
		"ref": "refs/heads/k8s-unstable", "before": "1111111111111111111111111111111111111111",
		"pusher": "vcarroy", "commits": "2", "message": "fix: the clone",
		"compare_url": "https://forge.example.fr/CNOUS/mse/compare/1111...2222",
	}
	for key, value := range expected {
		if fact.Payload[key] != value {
			t.Errorf("%s = %q, expected %q", key, fact.Payload[key], value)
		}
	}
	if _, tagged := fact.Payload["tag"]; tagged {
		t.Error("a branch push carries a tag")
	}
	if fact.Suffix != push || fact.Subject != "k8s-unstable" || !fact.OccurredAt.Equal(received) {
		t.Errorf("fact %+v", fact)
	}
	if fact.Key != "CNOUS/mse:refs/heads/k8s-unstable:2222222222222222222222222222222222222222" {
		t.Errorf("key %q", fact.Key)
	}
}

func TestATagPushCarriesNoBranch(t *testing.T) {
	body := strings.Replace(branchPush, "refs/heads/k8s-unstable", "refs/tags/v3.4.0", 1)
	observed, err := receive(t, "push", body)
	if err != nil || len(observed) != 1 {
		t.Fatalf("%v, %v", observed, err)
	}
	if observed[0].Payload["tag"] != "v3.4.0" || observed[0].Subject != "v3.4.0" {
		t.Errorf("payload %v", observed[0].Payload)
	}
	if _, branch := observed[0].Payload["branch"]; branch {
		t.Error("a tag push carries a branch")
	}
}

func TestADeletionPushedNothing(t *testing.T) {
	body := strings.Replace(branchPush, "2222222222222222222222222222222222222222", zeroCommit, 1)
	if observed, err := receive(t, "push", body); err != nil || len(observed) != 0 {
		t.Errorf("%v, %v", observed, err)
	}
}

// A received opening and a polled one are the same fact: same suffix, same key, same payload.
func TestAReceivedOpeningIsThePolledOne(t *testing.T) {
	server := testForge(t, twoRequests)
	defer server.Close()
	polled, _, err := pullProbe{}.Poll(context.Background(), target(forgeConfig(server.URL)), "2026-09-01T00:00:00Z")
	if err != nil || len(polled) != 1 {
		t.Fatalf("polled %v, %v", polled, err)
	}

	observed, err := receive(t, "pull_request", `{
	  "action": "opened",
	  "repository": {"full_name": "CNOUS/mse"},
	  "pull_request": {"number": 412, "title": "Fix the clone",
	    "html_url": "https://forge.example.fr/CNOUS/mse/pulls/412",
	    "user": {"login": "vcarroy"}, "head": {"ref": "fix/clone"}, "base": {"ref": "main"},
	    "created_at": "2026-09-03T10:30:00Z"}
	}`)
	if err != nil || len(observed) != 1 {
		t.Fatalf("received %v, %v", observed, err)
	}
	got, want := observed[0], polled[0]
	if got.Suffix != want.Suffix || got.Key != want.Key || got.Subject != want.Subject ||
		!got.OccurredAt.Equal(want.OccurredAt) {
		t.Errorf("received %+v, polled %+v", got, want)
	}
	for key, value := range want.Payload {
		if got.Payload[key] != value {
			t.Errorf("%s = %q received, %q polled", key, got.Payload[key], value)
		}
	}
}

func TestWhatThePluginDoesNotNameIsIgnored(t *testing.T) {
	for event, body := range map[string]string{
		"issues":       `{"action": "opened"}`,
		"pull_request": `{"action": "closed", "repository": {"full_name": "CNOUS/mse"}}`,
	} {
		if observed, err := receive(t, event, body); err != nil || len(observed) != 0 {
			t.Errorf("%s: %v, %v", event, observed, err)
		}
	}
}

// A secret set for the instance signs every repository's hook alike: the signature does not say
// which project a delivery is for, the repository does.
func TestADeliveryAboutAnotherRepositoryIsRefused(t *testing.T) {
	body := strings.Replace(branchPush, `"CNOUS/mse"`, `"CNOUS/other"`, 1)
	if _, err := receive(t, "push", body); err == nil || !strings.Contains(err.Error(), "CNOUS/other") {
		t.Errorf("err = %v", err)
	}
}

func TestAFormEncodedDeliveryIsRefusedWithItsFix(t *testing.T) {
	if _, err := receive(t, "push", "payload=%7B%7D"); err == nil || !strings.Contains(err.Error(), "application/json") {
		t.Errorf("err = %v", err)
	}
}
