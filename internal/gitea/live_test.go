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
		t.Skip("GITEA_LIVE non défini: vérification contre la forge réelle ignorée")
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

func TestLiveLesReferencesSeLisent(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	account, err := client.CheckAccess(ctx)
	if err != nil {
		t.Fatalf("accès: %v", err)
	}
	t.Logf("jeton du compte %s (admin=%v)", account.Login, account.Admin)

	branch, err := client.DefaultBranch(ctx)
	if err != nil {
		t.Fatalf("branche par défaut: %v", err)
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
		t.Error("aucune branche rendue")
	}
	t.Logf("défaut=%s, %d branches (tronqué=%v), %d tags (tronqué=%v)",
		branch, len(branches), truncatedBranches, len(tags), truncatedTags)
}

// La résolution d'une référence en commit, contre la forge réelle. C'est le
// premier maillon de l'invariant : une release doit désigner un commit, pas une
// branche qui bouge (B17).
func TestLiveLaResolutionRendUnCommit(t *testing.T) {
	client := liveClient(t)
	ctx := context.Background()

	branch, err := client.DefaultBranch(ctx)
	if err != nil {
		t.Fatalf("branche par défaut: %v", err)
	}

	commit, err := client.ResolveCommit(ctx, branch)
	if err != nil {
		t.Fatalf("résolution de %q: %v", branch, err)
	}
	if len(commit) < 40 {
		t.Errorf("commit %q: un SHA complet est attendu", commit)
	}
	t.Logf("%s -> %s", branch, commit)

	// Une référence inexistante doit être refusée, pas silencieusement ignorée.
	if _, err := client.ResolveCommit(ctx, "branche-qui-nexiste-pas-du-tout"); err == nil {
		t.Error("une référence inexistante devrait être refusée")
	}
}
