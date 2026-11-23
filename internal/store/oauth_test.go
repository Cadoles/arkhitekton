package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func tokenHash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

// A grant's life: its code serves once, its refresh rotates, a replaced refresh token revokes it.
func TestAnOAuthGrantRotatesAndDiesOnReplay(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := db.CreateOAuthClient(ctx, OAuthClient{ID: "c1", Name: "Claude Code",
		RedirectURIs: []string{"http://127.0.0.1/callback"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateOAuthRequest(ctx, OAuthRequest{ID: "r1", ClientID: "c1",
		RedirectURI: "http://127.0.0.1/callback", CodeChallenge: "x", Scopes: []string{"dashboard:read"},
		ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	request, err := db.TakeOAuthRequest(ctx, "r1", now)
	if err != nil || request.ClientName != "Claude Code" {
		t.Fatalf("request %+v, %v", request, err)
	}
	if _, err := db.TakeOAuthRequest(ctx, "r1", now); !errors.Is(err, ErrOAuthNotFound) {
		t.Errorf("a request answered twice: %v", err)
	}

	id, err := db.CreateOAuthGrant(ctx, OAuthGrant{ClientID: "c1", Subject: "alice",
		Groups: []string{"dev"}, Scopes: []string{"dashboard:read"}},
		OAuthCode{Hash: tokenHash("code"), RedirectURI: "http://127.0.0.1/callback", CodeChallenge: "x",
			ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	code, client, err := db.TakeOAuthCode(ctx, tokenHash("code"), now)
	if err != nil || code.GrantID != id || client != "c1" {
		t.Fatalf("code %+v %s, %v", code, client, err)
	}
	if _, _, err := db.TakeOAuthCode(ctx, tokenHash("code"), now); !errors.Is(err, ErrOAuthNotFound) {
		t.Errorf("a code used twice: %v", err)
	}

	if err := db.IssueOAuthTokens(ctx, id, OAuthTokens{Access: tokenHash("a1"), AccessExpires: now.Add(time.Hour),
		Refresh: tokenHash("r1"), RefreshExpires: now.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	grant, err := db.OAuthGrantByAccess(ctx, tokenHash("a1"), now)
	if err != nil || grant.Subject != "alice" || grant.ClientName != "Claude Code" || grant.Groups[0] != "dev" {
		t.Fatalf("grant %+v, %v", grant, err)
	}

	if _, err := db.RefreshOAuthGrant(ctx, "c1", tokenHash("r1"), OAuthTokens{Access: tokenHash("a2"),
		AccessExpires: now.Add(time.Hour), Refresh: tokenHash("r2"), RefreshExpires: now.Add(24 * time.Hour)},
		now); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, err := db.OAuthGrantByAccess(ctx, tokenHash("a1"), now); !errors.Is(err, ErrOAuthNotFound) {
		t.Errorf("the replaced access token: %v", err)
	}
	listed, err := db.ListOAuthGrants(ctx, "alice")
	if err != nil || len(listed) != 1 {
		t.Fatalf("alice's agents %+v, %v", listed, err)
	}
	if others, _ := db.ListOAuthGrants(ctx, "bob"); len(others) != 0 {
		t.Errorf("bob sees alice's agents")
	}

	if _, err := db.RefreshOAuthGrant(ctx, "c1", tokenHash("r1"), OAuthTokens{Access: tokenHash("a3"),
		AccessExpires: now.Add(time.Hour), Refresh: tokenHash("r3"), RefreshExpires: now.Add(24 * time.Hour)},
		now); !errors.Is(err, ErrOAuthReplay) {
		t.Fatalf("replayed refresh: %v", err)
	}
	if _, err := db.OAuthGrantByAccess(ctx, tokenHash("a2"), now); !errors.Is(err, ErrOAuthNotFound) {
		t.Errorf("the grant survived a replay: %v", err)
	}
}

// What can no longer serve goes: a lapsed grant, then its client.
func TestThePurgeKeepsWhatServes(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, id := range []string{"idle", "used"} {
		if err := db.CreateOAuthClient(ctx, OAuthClient{ID: id, Name: id,
			RedirectURIs: []string{"http://127.0.0.1/cb"}}); err != nil {
			t.Fatal(err)
		}
	}
	id, err := db.CreateOAuthGrant(ctx, OAuthGrant{ClientID: "used", Subject: "alice",
		Scopes: []string{"dashboard:read"}}, OAuthCode{Hash: tokenHash("c"), RedirectURI: "x", CodeChallenge: "x",
		ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.IssueOAuthTokens(ctx, id, OAuthTokens{Access: tokenHash("a"), AccessExpires: now.Add(time.Hour),
		Refresh: tokenHash("r"), RefreshExpires: now.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	if _, err := db.PurgeOAuth(ctx, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.OAuthClient(ctx, "idle"); !errors.Is(err, ErrOAuthNotFound) {
		t.Errorf("an idle client survived: %v", err)
	}
	if _, err := db.OAuthGrantByAccess(ctx, tokenHash("a"), now); !errors.Is(err, ErrOAuthNotFound) {
		t.Errorf("a lapsed grant survived: %v", err)
	}
}

// An action an agent made says so in the journal.
func TestTheJournalNamesTheAgent(t *testing.T) {
	db := migrated(t)
	ctx := WithAgent(context.Background(), "Claude Code")
	if err := db.Audit(ctx, "alice", AuditJobEnqueue, "", nil); err != nil {
		t.Fatal(err)
	}
	entries, err := db.ListJournal(context.Background(), JournalQuery{Nature: JournalAction})
	if err != nil || len(entries) != 1 || entries[0].Agent != "Claude Code" {
		t.Fatalf("entries %+v, %v", entries, err)
	}
}
