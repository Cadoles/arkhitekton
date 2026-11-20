package store

import (
	"context"
	"testing"
	"time"
)

// The database wakes the key loop exactly when a key is awaited or a Secret is to be taken back,
// and not when a set fails: that would relaunch the failing set in a loop.
func TestTheDatabaseSignalsAwaitedKeys(t *testing.T) {
	db := migratedBare(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signals := make(chan struct{}, 16)
	go func() { _ = db.Listen(ctx, DeployKeysChannel, func() { signals <- struct{}{} }, discardLogger()) }()

	expect := func(what string) {
		t.Helper()
		select {
		case <-signals:
		case <-time.After(5 * time.Second):
			t.Fatalf("no signal %s", what)
		}
	}
	silence := func(what string) {
		t.Helper()
		select {
		case <-signals:
			t.Fatalf("a signal %s", what)
		case <-time.After(300 * time.Millisecond):
		}
	}

	expect("once listening, for what was missed before")

	id := project(t, db, "team/portail")
	expect("when a project is declared")

	if err := db.SetDeployKey(ctx, id, "ssh-ed25519 AAAA", "portail-git"); err != nil {
		t.Fatal(err)
	}
	silence("when the key is set")

	if err := db.SetDeployKeyError(ctx, id, "cluster unreachable"); err != nil {
		t.Fatal(err)
	}
	silence("when a set fails")

	if err := db.RequestKeyRotation(ctx, id); err != nil {
		t.Fatal(err)
	}
	expect("when a rotation is requested")

	if err := db.DeleteProject(ctx, id); err != nil {
		t.Fatal(err)
	}
	expect("when a project's Secret is to be taken back")
}
