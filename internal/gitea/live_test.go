package gitea

import (
	"context"
	"os"
	"testing"
)

/*
 * Vérification contre la forge réelle, gardée par `GITEA_LIVE=1` comme celle de
 * Redmine : la suite ne doit pas solliciter la forge à chaque `make test`.
 *
 *   GITEA_LIVE=1 go test ./internal/gitea/ -run Live -v
 */
func liveClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("GITEA_LIVE") != "1" {
		t.Skip("GITEA_LIVE not set: check against the real forge skipped")
	}
	client, err := New(Options{
		URL:        os.Getenv("GITEA_URL"),
		Token:      os.Getenv("GITEA_TOKEN"),
		Repository: os.Getenv("GITEA_REPO"),
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return client
}

func TestLiveReferencesAreReadable(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	account, err := client.CheckAccess(ctx)
	if err != nil {
		t.Fatalf("access: %v", err)
	}
	t.Logf("token of account %s (admin=%v)", account.Login, account.Admin)

	branch, err := client.DefaultBranch(ctx)
	if err != nil {
		t.Fatalf("default branch: %v", err)
	}
	branches, truncatedBranches, err := client.ListBranches(ctx)
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	tags, truncatedTags, err := client.ListTags(ctx)
	if err != nil {
		t.Fatalf("tags: %v", err)
	}

	if len(branches) == 0 {
		t.Error("no branch returned")
	}
	t.Logf("default=%s, %d branches (truncated=%v), %d tags (truncated=%v)",
		branch, len(branches), truncatedBranches, len(tags), truncatedTags)
}

// Resolving a reference to a commit, against the real forge. It is the first link of
// the invariant: a release must name a commit, not a branch that moves.
func TestLiveResolutionReturnsACommit(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	branch, err := client.DefaultBranch(ctx)
	if err != nil {
		t.Fatalf("default branch: %v", err)
	}

	commit, err := client.ResolveCommit(ctx, branch)
	if err != nil {
		t.Fatalf("resolving %q: %v", branch, err)
	}
	if len(commit) < 40 {
		t.Errorf("commit %q: a full SHA is expected", commit)
	}
	t.Logf("%s -> %s", branch, commit)

	// A non-existent reference must be refused, not silently ignored.
	if _, err := client.ResolveCommit(ctx, "branch-that-does-not-exist-at-all"); err == nil {
		t.Error("a non-existent reference should be refused")
	}
}
