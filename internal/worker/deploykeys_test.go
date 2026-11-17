package worker

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/deploykeys"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

type fakeKeyStore struct {
	pending   []store.PendingKey
	saved     map[int64]string
	secrets   map[int64]string
	errors    map[int64]string
	orphans   []string
	forgotten []string
	audits    int
}

func (f *fakeKeyStore) ProjectsAwaitingKey(context.Context) ([]store.PendingKey, error) {
	return f.pending, nil
}

func (f *fakeKeyStore) SetDeployKey(_ context.Context, id int64, public, secret string) error {
	if f.saved == nil {
		f.saved, f.secrets = map[int64]string{}, map[int64]string{}
	}
	f.saved[id], f.secrets[id] = public, secret
	return nil
}

func (f *fakeKeyStore) SetDeployKeyError(_ context.Context, id int64, reason string) error {
	if f.errors == nil {
		f.errors = map[int64]string{}
	}
	f.errors[id] = reason
	return nil
}

func (f *fakeKeyStore) SecretsToDelete(context.Context) ([]string, error) { return f.orphans, nil }

func (f *fakeKeyStore) SecretDeleted(_ context.Context, name string) error {
	f.forgotten = append(f.forgotten, name)
	return nil
}

func (f *fakeKeyStore) Audit(context.Context, string, string, string, map[string]any) error {
	f.audits++
	return nil
}

type fakeKeyIssuer struct {
	// failures returns an error for the slugs listed in it.
	failures map[string]error
	issued   []string
	known    map[string]bool
	revoked  []string
}

func (f *fakeKeyIssuer) Issue(_ context.Context, uid, slug string, known bool) (string, string, error) {
	if err := f.failures[slug]; err != nil {
		return "", "", err
	}
	f.issued = append(f.issued, slug)
	if f.known == nil {
		f.known = map[string]bool{}
	}
	f.known[slug] = known
	return deploykeys.KindGit.Name(uid), "ssh-ed25519 AAAA " + slug, nil
}

func (f *fakeKeyIssuer) Revoke(_ context.Context, name string) error {
	f.revoked = append(f.revoked, name)
	return nil
}

// The nominal case: every waiting project receives its key, and the public half is recorded.
func TestEachWaitingProjectReceivesItsKey(t *testing.T) {
	projects := &fakeKeyStore{pending: []store.PendingKey{
		{ID: 1, Slug: "mstore", Known: false},
		{ID: 2, Slug: "hydra-sql", Known: false},
	}}
	issuer := &fakeKeyIssuer{}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if len(issuer.issued) != 2 {
		t.Fatalf("%d key(s) set, expected 2", len(issuer.issued))
	}
	if projects.saved[1] == "" || projects.saved[2] == "" {
		t.Errorf("keys recorded: %v", projects.saved)
	}
}

// A rotation differs from a first setting, and the issuer must know it: that is what allows
// it to replace an existing Secret rather than refuse to adopt it.
func TestARotationIsReportedToTheIssuer(t *testing.T) {
	projects := &fakeKeyStore{pending: []store.PendingKey{
		{ID: 1, Slug: "mstore", Known: true},
	}}
	issuer := &fakeKeyIssuer{}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if !issuer.known["mstore"] {
		t.Error("the rotation was not reported: the issuer will refuse to adopt")
	}
}

// A project whose name is already taken does not block the others.
//
// It is a situation a human must settle — a Secret of the same name the dashboard did not
// create — and stopping on it would deprive of a key every project coming after it in the
// list.
func TestANameAlreadyTakenDoesNotBlockTheOthers(t *testing.T) {
	projects := &fakeKeyStore{pending: []store.PendingKey{
		{ID: 1, Slug: "occupe"},
		{ID: 2, Slug: "libre"},
	}}
	issuer := &fakeKeyIssuer{
		failures: map[string]error{"occupe": deploykeys.ErrNameTaken},
	}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if len(issuer.issued) != 1 || issuer.issued[0] != "libre" {
		t.Errorf("keys set %v — the next project must be served", issuer.issued)
	}
	// And above all: nothing is recorded for the one that failed. Recording it would assert an
	// ownership the dashboard does not have, and a later rotation would overwrite someone else's
	// Secret.
	if _, recorded := projects.saved[1]; recorded {
		t.Error("a key that was not set was recorded: the property would be false")
	}
}

// A cluster outage records nothing either, for the same reason.
func TestAKeyNotSetIsNotRecorded(t *testing.T) {
	projects := &fakeKeyStore{pending: []store.PendingKey{{ID: 1, Slug: "mstore"}}}
	issuer := &fakeKeyIssuer{
		failures: map[string]error{"mstore": errors.New("API injoignable")},
	}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if len(projects.saved) != 0 {
		t.Errorf("records %v — no key was set", projects.saved)
	}
	if projects.audits != 0 {
		t.Error("a failed setting must not be logged as done")
	}
}

// A key that could not be set says why on its project, and the name of a key set is recorded.
func TestWhyAKeyWasNotSetIsRecorded(t *testing.T) {
	projects := &fakeKeyStore{pending: []store.PendingKey{
		{ID: 1, Slug: "occupe", UID: "u1"},
		{ID: 2, Slug: "libre", UID: "u2"},
	}}
	issuer := &fakeKeyIssuer{failures: map[string]error{"occupe": deploykeys.ErrNameTaken}}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if projects.errors[1] == "" {
		t.Error("the refusal was not recorded for the project's screen")
	}
	if projects.secrets[2] != deploykeys.KindGit.Name("u2") {
		t.Errorf("secret recorded %q", projects.secrets[2])
	}
}

// A key named from its slug, before projects had an id, moves under the id's name at its rotation,
// and the former Secret is taken back.
func TestARotationTakesBackAKeyNamedFromItsSlug(t *testing.T) {
	projects := &fakeKeyStore{pending: []store.PendingKey{
		{ID: 1, Slug: "mstore", UID: "u1", Known: true, Secret: "arkhitekton-project-mstore-git"},
		{ID: 2, Slug: "shop", UID: "u2", Known: true, Secret: deploykeys.KindGit.Name("u2")},
	}}
	issuer := &fakeKeyIssuer{}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if len(issuer.revoked) != 1 || issuer.revoked[0] != "arkhitekton-project-mstore-git" {
		t.Errorf("revoked %v, expected the slug's name alone", issuer.revoked)
	}
}

// The Secrets of a deleted project are deleted, then forgotten.
func TestADeletedProjectsSecretsAreDeleted(t *testing.T) {
	projects := &fakeKeyStore{orphans: []string{"arkhitekton-project-u1-git", "arkhitekton-project-u1-review"}}
	issuer := &fakeKeyIssuer{}

	NewKeyReconciler(projects, issuer, quietLogger()).once(context.Background())

	if len(issuer.revoked) != 2 || len(projects.forgotten) != 2 {
		t.Errorf("revoked %v, forgotten %v", issuer.revoked, projects.forgotten)
	}
}
