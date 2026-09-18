package store

import (
	"context"
	"errors"
	"testing"
)

/*
 * Ces tests existent pour une raison précise : les requêtes de note nommaient une
 * table qui n'existe pas — `redmine_issues` au lieu d'`issues`. Rien ne l'a vu,
 * parce que rien ne les exécutait contre une vraie base. Le défaut n'est apparu
 * qu'à l'écran, en 500, et le message d'erreur ne désignait pas la cause.
 *
 * Une requête SQL ne se relit pas : elle s'exécute.
 */

func noteRelease(t *testing.T, db *DB, versionID int64, tag string) int64 {
	t.Helper()
	id, err := db.CreateRelease(context.Background(), Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: tag, Kind: "pre", State: "ready",
		ImageVersion: tag, CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release %s: %v", tag, err)
	}
	return id
}

// Le parcours complet : rattacher, lire, retirer.
func TestAnIssueIsLinkedToAndRemovedFromANote(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")
	issueOf(t, db, 1234, 920, "Évolution", "En revue", BucketDoing, 100, false)

	if err := db.LinkNoteIssue(ctx, releaseID, "1234", NoteSourceAuto, "#42", "vcarroy"); err != nil {
		t.Fatalf("rattachement: %v", err)
	}

	entries, err := db.ListNoteEntries(ctx, releaseID)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entry(ies)", len(entries))
	}
	entry := entries[0]
	// The content comes from `issues` by join: that is what makes a subject corrected in the
	// tracker appear without regenerating the note.
	if !entry.Known || entry.Subject != "sujet" || entry.Tracker != "Évolution" {
		t.Errorf("joined entry: %+v", entry)
	}
	if entry.Source != NoteSourceAuto || entry.PullRequest != "#42" {
		t.Errorf("provenance: %+v", entry)
	}
	if entry.Anomaly {
		t.Error("an enhancement is not an anomaly")
	}

	if err := db.UnlinkNoteIssue(ctx, releaseID, "1234"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	if remaining, _ := db.ListNoteEntries(ctx, releaseID); len(remaining) != 0 {
		t.Errorf("%d entry(ies) after removal", len(remaining))
	}
}

// An issue appears only once: a second detection pass would double it, and the note would
// count the same thing twice.
func TestAnIssueIsNotLinkedTwice(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")

	if err := db.LinkNoteIssue(ctx, releaseID, "1234", NoteSourceAuto, "#42", "vcarroy"); err != nil {
		t.Fatalf("premier rattachement: %v", err)
	}
	err := db.LinkNoteIssue(ctx, releaseID, "1234", NoteSourceManual, "", "vcarroy")
	if !errors.Is(err, ErrNoteAlreadyLinked) {
		t.Errorf("second link: %v, expected ErrNoteAlreadyLinked", err)
	}
}

// A linked issue synchronisation has not yet seen stays listed, without its subject.
// Hiding it would make a manual addition disappear, which would read as a loss.
func TestAnUnknownIssueStaysListed(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")

	if err := db.LinkNoteIssue(ctx, releaseID, "9999", NoteSourceManual, "", "vcarroy"); err != nil {
		t.Fatalf("rattachement: %v", err)
	}

	entries, err := db.ListNoteEntries(ctx, releaseID)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entry(ies)", len(entries))
	}
	if entries[0].Known {
		t.Error("the issue should be marked unknown")
	}
}

// Anomalies are recognised by the tracker's prefix: the tracker carries several anomaly
// variants, all of them fixes.
func TestUneAnomalieEstReconnueAuPrefixeDuTracker(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")
	issueOf(t, db, 1, 920, "Anomalie bloquante", "En revue", BucketDoing, 100, false)

	if err := db.LinkNoteIssue(ctx, releaseID, "1", NoteSourceAuto, "#1", "vcarroy"); err != nil {
		t.Fatalf("rattachement: %v", err)
	}
	entries, _ := db.ListNoteEntries(ctx, releaseID)
	if len(entries) != 1 || !entries[0].Anomaly {
		t.Errorf("entries: %+v — a blocking anomaly is a fix", entries)
	}
}

// The linking pool: the issues of the release's version.
func TestThePoolReturnsTheVersionsIssues(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")

	issueOf(t, db, 1, 920, "Évolution", "En revue", BucketDoing, 100, false)
	issueOf(t, db, 2, 920, "Anomalie", "Terminé", BucketDone, 100, true)
	// Rejected: it is not delivered, and offering it would invite announcing to the client
	// what was dropped.
	issueOf(t, db, 3, 920, "Évolution", "Rejeté", BucketRejected, 0, true)
	// From another version: outside the pool.
	otherVersion := redmineVersion(t, db, 921, "v3.3.0", "open")
	_ = otherVersion
	issueOf(t, db, 4, 921, "Évolution", "En revue", BucketDoing, 100, false)

	entries, err := db.ListReleaseVersionIssues(ctx, releaseID)
	if err != nil {
		t.Fatalf("vivier: %v", err)
	}

	got := map[string]bool{}
	for _, entry := range entries {
		got[entry.ExternalID] = true
	}
	if !got["1"] || !got["2"] {
		t.Errorf("issues returned: %v — the version carries two that are not rejected", got)
	}
	if got["3"] {
		t.Error("a rejected issue must not be offered")
	}
	if got["4"] {
		t.Error("an issue of another version must not be offered")
	}
}

// The detection candidates: those whose status makes them eligible.
func TestCandidatesAreFilteredByStatus(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")
	issueOf(t, db, 1, 920, "Évolution", "En revue", BucketDoing, 100, false)
	issueOf(t, db, 2, 920, "Évolution", "En cours", BucketDoing, 50, false)

	candidates, err := db.NoteCandidates(ctx, releaseID, []string{"En revue"})
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("%d candidate(s): %+v", len(candidates), candidates)
	}
	if _, ok := candidates["1"]; !ok {
		t.Error("the issue in review should be a candidate")
	}
}

// Two projects may number alike: that is the whole point of the
// `(project_id, tool, external_id)` key. A join or a pool that forgets the project raises
// no error — it shows another deployment's subject on its own note, and offers for linking
// an issue that does not exist here.
func TestANoteDoesNotFetchAnotherProjectsIssue(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	var second int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, dockerfile, build_context,
		                       tracker_project, created_by)
		 values ('autre', 'vcarroy/autre', 'Autre', 'https://forge.example', 'Dockerfile', '.',
		         'autre-suivi', 'test')
		 returning id`).Scan(&second); err != nil {
		t.Fatalf("second projet: %v", err)
	}

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")

	// Number 1234 exists on both sides, with two different subjects.
	issueOf(t, db, 1234, 920, "Évolution", "En revue", BucketDoing, 100, false)
	if err := db.UpsertRedmineIssue(ctx, TrackedIssue{
		ProjectID: second, Tool: "redmine", ExternalID: "1234",
		Tracker: "Anomalie", Subject: "the other deployment's subject",
		StatusName: "En revue", StatusBucket: BucketDoing,
	}); err != nil {
		t.Fatalf("issue of the second project: %v", err)
	}

	if err := db.LinkNoteIssue(ctx, releaseID, "1234", NoteSourceManual, "", "vcarroy"); err != nil {
		t.Fatalf("rattachement: %v", err)
	}

	// One row only, and it is the release's project's. Two rows would mean the join brought
	// back both issues.
	entries, err := db.ListNoteEntries(ctx, releaseID)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entry(ies) — the join brings back another project's issues: %+v",
			len(entries), entries)
	}
	if entries[0].Subject != "sujet" || entries[0].Anomaly {
		t.Errorf("joined entry: %+v — that is the other deployment's issue", entries[0])
	}

	// The detection pool is bounded the same way.
	candidates, err := db.NoteCandidates(ctx, releaseID, []string{"En revue"})
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates) != 1 || candidates["1234"].Subject != "sujet" {
		t.Errorf("candidates: %+v — the pool spills onto the other project", candidates)
	}
}
