package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
)

// redmineVersion writes a real version, as synchronisation would.
func redmineVersion(t *testing.T, db *DB, redmineID int64, name, status string) int64 {
	t.Helper()
	id, err := db.UpsertTrackedVersion(context.Background(), TrackedVersion{
		ProjectID: testProjectID, Tool: "redmine", ExternalID: fmt.Sprint(redmineID), Name: name,
		Normalized: release.NormalizeVersionName(name),
		Status:     status,
	})
	if err != nil {
		t.Fatalf("version %s: %v", name, err)
	}
	return id
}

func issueOf(t *testing.T, db *DB, redmineID int64, versionRedmineID int64, tracker, status, bucket string, doneRatio int, closed bool) {
	t.Helper()
	if err := db.UpsertTrackedIssue(context.Background(), TrackedIssue{
		ProjectID: testProjectID, Tool: "redmine", ExternalID: fmt.Sprint(redmineID),
		VersionExternalID: externalRef(versionRedmineID),
		Tracker:           tracker, Subject: "subject", StatusName: status, StatusBucket: bucket,
		DoneRatio: doneRatio, IsClosed: closed,
	}); err != nil {
		t.Fatalf("issue %d: %v", redmineID, err)
	}
}

// releaseOn attaches a release to a version, by the normal route.
func releaseOn(t *testing.T, db *DB, versionID int64, tag string) {
	t.Helper()
	if _, err := db.CreateRelease(context.Background(), Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: tag, Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: tag, CreatedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("release %s: %v", tag, err)
	}
}

// The case that justifies the whole mechanism: a release created before the tracker
// was known must end up attached to the real version, and not stay on a provisional
// one nobody looks at.
func TestAProvisionalVersionIsAdoptedByTheRealOne(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	// A release is created while synchronisation has never run.
	pendingID, err := db.EnsureVersion(ctx, testProjectID, "3.2.0")
	if err != nil {
		t.Fatalf("provisional version: %v", err)
	}
	releaseOn(t, db, pendingID, "3.2.0-rc1")

	// Synchronisation then discovers the version, under its real prefixed name.
	redmineVersion(t, db, 805, "v3.2.0", "open")

	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 1 || report[0].AdoptedInto != "v3.2.0" || report[0].Releases != 1 {
		t.Fatalf("adoption report: %+v", report)
	}

	// The provisional one has disappeared, and the release followed.
	pendings, err := db.PendingVersions(ctx, testProjectID)
	if err != nil {
		t.Fatalf("provisional: %v", err)
	}
	if len(pendings) != 0 {
		t.Errorf("provisional left: %v", pendings)
	}

	summaries, err := db.ListVersionSummaries(ctx, testProjectID)
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("%d versions, expected 1", len(summaries))
	}
	if summaries[0].Name != "v3.2.0" || summaries[0].Releases != 1 {
		t.Errorf("version after adoption: %+v", summaries[0])
	}
	if summaries[0].Pending {
		t.Error("the real version must not be marked provisional")
	}
}

// A reused number: one is closed — an older numbering scheme — and the other open. A
// release aiming at that number aims at the active version, so adoption must succeed
// rather than declare an ambiguity.
func TestAClosedNamesakeVersionDoesNotBlockAdoption(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	pendingID, err := db.EnsureVersion(ctx, testProjectID, "3.1.2")
	if err != nil {
		t.Fatalf("provisional version: %v", err)
	}
	releaseOn(t, db, pendingID, "3.1.2")

	redmineVersion(t, db, 803, "3.1.2", "closed")
	redmineVersion(t, db, 804, "v3.1.2", "open")

	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 1 {
		t.Fatalf("report: %+v", report)
	}
	if report[0].Ambiguous {
		t.Errorf("only one active version carries this number: no ambiguity expected, %+v", report[0])
	}
	if report[0].AdoptedInto != "v3.1.2" {
		t.Errorf("adopted by %q, expected the active version v3.1.2", report[0].AdoptedInto)
	}

	// The provisional one has disappeared and the release follows the open version.
	pendings, err := db.PendingVersions(ctx, testProjectID)
	if err != nil {
		t.Fatalf("provisional: %v", err)
	}
	if len(pendings) != 0 {
		t.Errorf("provisional left: %v", pendings)
	}
}

// `locked` is not `closed`: a locked version accepts no new issue but is not
// finished, and therefore stays the candidate.
func TestALockedVersionStaysACandidate(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	pendingID, _ := db.EnsureVersion(ctx, testProjectID, "3.1.3")
	releaseOn(t, db, pendingID, "3.1.3")

	redmineVersion(t, db, 810, "3.1.3", "closed")
	redmineVersion(t, db, 811, "v3.1.3", "locked")

	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 1 || report[0].AdoptedInto != "v3.1.3" {
		t.Errorf("report: %+v", report)
	}
}

// The real ambiguity remains when two active versions carry the same number: there,
// choosing would attach releases to the wrong version.
func TestTwoActiveVersionsOfTheSameNameStayUndecidable(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	pendingID, err := db.EnsureVersion(ctx, testProjectID, "3.1.2")
	if err != nil {
		t.Fatalf("provisional version: %v", err)
	}
	releaseOn(t, db, pendingID, "3.1.2")

	redmineVersion(t, db, 803, "3.1.2", "open")
	redmineVersion(t, db, 804, "v3.1.2", "open")

	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 1 || !report[0].Ambiguous {
		t.Fatalf("report: %+v", report)
	}
	if len(report[0].Candidates) != 2 {
		t.Errorf("candidates: %v", report[0].Candidates)
	}

	// The provisional version remains, and is therefore still reported.
	pendings, err := db.PendingVersions(ctx, testProjectID)
	if err != nil {
		t.Fatalf("provisional: %v", err)
	}
	if len(pendings) != 1 || pendings[0] != "3.1.2" {
		t.Errorf("provisional: %v", pendings)
	}

	ambiguous, err := db.AmbiguousVersions(ctx, testProjectID)
	if err != nil {
		t.Fatalf("ambiguities: %v", err)
	}
	if len(ambiguous) != 1 || ambiguous[0].Normalized != "3.1.2" {
		t.Errorf("ambiguities: %+v", ambiguous)
	}
}

// Adoption must not depend on the order versions are read in.
func TestAdoptionDoesNotDependOnReadOrder(t *testing.T) {
	for _, order := range [][]string{{"3.1.2", "v3.1.2"}, {"v3.1.2", "3.1.2"}} {
		db := migrated(t)
		ctx := context.Background()

		pendingID, _ := db.EnsureVersion(ctx, testProjectID, "3.1.2")
		releaseOn(t, db, pendingID, "3.1.2")

		// Both active: the only case still undecidable, and therefore the only one where read
		// order could make a difference.
		for i, name := range order {
			redmineVersion(t, db, int64(900+i), name, "open")
		}

		report, err := db.AdoptPendingVersions(ctx)
		if err != nil {
			t.Fatalf("%v: adoption: %v", order, err)
		}
		if len(report) != 1 || !report[0].Ambiguous {
			t.Errorf("order %v: the ambiguity must be observed whatever the order, report %+v",
				order, report)
		}
	}
}

// A provisional version the tracker does not know stays visible: it is the symptom of
// a typo in a tag, and hiding it would leave a screen showing "awaiting
// synchronisation" indefinitely.
func TestAnUnmatchedProvisionalVersionStaysReported(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.EnsureVersion(ctx, testProjectID, "9.9.9"); err != nil {
		t.Fatalf("provisional: %v", err)
	}
	redmineVersion(t, db, 805, "v3.2.0", "open")

	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 0 {
		t.Errorf("no adoption expected: %+v", report)
	}

	pendings, err := db.PendingVersions(ctx, testProjectID)
	if err != nil {
		t.Fatalf("provisional: %v", err)
	}
	if len(pendings) != 1 || pendings[0] != "9.9.9" {
		t.Errorf("provisional: %v", pendings)
	}
}

// Rejected issues leave the computation: counting them in the denominator would lower
// a version's completion because an issue was dropped.
func TestRejectedIssuesLeaveTheComputation(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Évolution", "Fermé", "done", 100, true)
	issueOf(t, db, 2, 805, "Évolution", "En cours", "doing", 50, false)
	issueOf(t, db, 3, 805, "Évolution", "Rejeté", "rejected", 0, true)

	summary, err := db.ListVersionSummaries(ctx, testProjectID)
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(summary) != 1 {
		t.Fatalf("%d versions", len(summary))
	}

	// Two counted out of three issues, one done.
	if summary[0].Counted != 2 || summary[0].Done != 1 {
		t.Errorf("counts: counted=%d done=%d, expected 2 and 1",
			summary[0].Counted, summary[0].Done)
	}
	// The weighted measure covers the counted ones: 100 + 50, and not 150 over three.
	if summary[0].WeightedDone != 150 {
		t.Errorf("weighting %d, expected 150", summary[0].WeightedDone)
	}
}

// Anomalies are counted separately and do not enter completion: a version may be
// functionally complete and carry some.
func TestAnomaliesAreCountedSeparately(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Évolution", "Fermé", "done", 100, true)
	// The instance's two anomaly trackers. Comparing for equality with one name would
	// miss half of them.
	issueOf(t, db, 2, 805, "Anomalie", "Nouveau", "todo", 0, false)
	issueOf(t, db, 3, 805, "Anomalie (SDN4)", "Nouveau", "todo", 0, false)
	// A closed anomaly does not count as open.
	issueOf(t, db, 4, 805, "Anomalie", "Fermé", "done", 100, true)

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if summary.OpenAnomalies != 2 {
		t.Errorf("%d open anomalies, expected 2 (both trackers)", summary.OpenAnomalies)
	}

	issues, err := db.ListVersionIssues(ctx, summary.ID)
	if err != nil {
		t.Fatalf("issues: %v", err)
	}
	anomalies := 0
	for _, issue := range issues {
		if issue.Anomaly {
			anomalies++
		}
	}
	if anomalies != 3 {
		t.Errorf("%d issues marked as anomalies, expected 3", anomalies)
	}
}

// An unmapped status falls into `todo` and is reported: tracker statuses change
// without warning, and a silent drift of the completion rate would be invisible.
func TestAnUnmappedStatusIsReported(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Évolution", "Unheard-of Status", "todo", 0, false)

	unmapped, err := db.UnmappedStatuses(ctx, testProjectID)
	if err != nil {
		t.Fatalf("unmapped statuses: %v", err)
	}
	if len(unmapped) != 1 || unmapped[0].StatusName != "Unheard-of Status" || unmapped[0].Issues != 1 {
		t.Errorf("unmapped: %+v", unmapped)
	}

	// Once declared, it leaves the list and reapplying moves it.
	if err := db.SetStatusBucket(ctx, testProjectID, "Unheard-of Status", "done"); err != nil {
		t.Fatalf("projection: %v", err)
	}
	moved, err := db.ReprojectIssues(ctx, testProjectID)
	if err != nil {
		t.Fatalf("reapplying: %v", err)
	}
	if moved != 1 {
		t.Errorf("%d issues moved, expected 1", moved)
	}

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if summary.Done != 1 {
		t.Errorf("the issue should count as done after reapplying: %+v", summary)
	}
}

// The instance's statuses are all mapped by the migration: none must fall into the
// default bucket without having been decided.
func TestEveryInstanceStatusIsMapped(t *testing.T) {
	db := migrated(t)

	mapping, err := db.StatusMapping(context.Background(), testProjectID)
	if err != nil {
		t.Fatalf("projection: %v", err)
	}

	// Read from the real instance through /issue_statuses.json.
	actual := []string{
		"Brouillon", "Nouveau", "Analyse", "Pris en charge", "En attente", "Rejeté",
		"Ready", "En cours", "Développement", "En revue", "Livré DEV", "A livrer usine",
		"Test usine", "Prêt à livrer", "Livré", "Test", "Test en échec", "Testé",
		"Commentaire", "Installation", "Documentation", "Done", "Résolu", "Fermé",
		"Exécuté avec succès", "Non exécuté", "Exécuté en échec", "En Standby",
		"En StandBy", "Ré-ouvert",
	}
	for _, observed := range actual {
		if _, ok := mapping[observed]; !ok {
			t.Errorf("status %q with no declared mapping", observed)
		}
	}

	// Only the rejected one is out of scope.
	if mapping["Rejeté"] != "rejected" {
		t.Errorf("the rejected status maps to %q", mapping["Rejeté"])
	}
	// One status is declared closed by the tracker; the mapping says the opposite, and
	// that is deliberate — a reopened issue is in progress.
	if mapping["Ré-ouvert"] != "doing" {
		t.Errorf("the reopened status maps to %q, expected doing", mapping["Ré-ouvert"])
	}
}

// The cursor does not advance after a failure: advancing it would skip the unread
// window, and issues modified during it would never be picked up.
func TestAFailureDoesNotAdvanceTheCursor(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.RecordSyncSuccess(ctx, "redmine", testProjectID, "2026-08-01T00:00:00Z"); err != nil {
		t.Fatalf("success: %v", err)
	}
	if err := db.RecordSyncFailure(ctx, "redmine", testProjectID, "Redmine injoignable"); err != nil {
		t.Fatalf("failure: %v", err)
	}

	state, err := db.GetSyncState(ctx, "redmine", testProjectID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Cursor != "2026-08-01T00:00:00Z" {
		t.Errorf("cursor %q: it must not move after a failure", state.Cursor)
	}
	if state.LastError != "Redmine injoignable" {
		t.Errorf("error %q", state.LastError)
	}
	// `last_ok` distinct from `last_run`: the interface must be able to say "data this
	// old, last attempt failed".
	if state.LastOK == nil || state.LastRun == nil || !state.LastRun.After(*state.LastOK) {
		t.Errorf("timestamps: run=%v ok=%v", state.LastRun, state.LastOK)
	}
}

// Never synchronised is not an error: it is the state at first start-up, and it must
// lead to a full read.
func TestANeverSynchronisedSourceReturnsAnEmptyState(t *testing.T) {
	db := migrated(t)

	state, err := db.GetSyncState(context.Background(), "redmine", testProjectID)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if state.Cursor != "" || state.LastRun != nil || state.LastOK != nil {
		t.Errorf("unexpected state: %+v", state)
	}
}

func mustVersionID(t *testing.T, db *DB, name string) int64 {
	t.Helper()
	var id int64
	if err := db.pool.QueryRow(context.Background(),
		`select id from versions where name = $1`, name).Scan(&id); err != nil {
		t.Fatalf("version %q: %v", name, err)
	}
	return id
}

// The `client-test` bucket sits between `doing` and `done`, and does not count as
// done: it enters the completion denominator, never the numerator. Confusing
// "finished developing" with "accepted" is exactly what a completion rate must avoid.
func TestTheClientTestBucketCountsInTheDenominatorNotTheNumerator(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Évolution", "Fermé", BucketDone, 100, true)
	issueOf(t, db, 2, 805, "Évolution", "Livré", BucketClientTest, 100, false)
	issueOf(t, db, 3, 805, "Évolution", "En cours", BucketDoing, 30, false)
	issueOf(t, db, 4, 805, "Évolution", "Rejeté", BucketRejected, 0, true)

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	// Three counted — everything but the rejected one — of which only one is done.
	if summary.Counted != 3 {
		t.Errorf("denominator %d, expected 3: client test must appear in it", summary.Counted)
	}
	if summary.Done != 1 {
		t.Errorf("numerator %d, expected 1: client test must not appear in it", summary.Done)
	}
	// The weighted measure covers the counted ones: 100 + 100 + 30.
	if summary.WeightedDone != 230 {
		t.Errorf("weighting %d, expected 230", summary.WeightedDone)
	}
}

// The database constraint accepts the five buckets, and nothing else. An invented
// bucket must be refused rather than silently filed.
func TestTheDatabaseAcceptsTheFiveBucketsAndNothingElse(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")

	for i, bucket := range []string{BucketTodo, BucketDoing, BucketClientTest, BucketDone, BucketRejected} {
		if err := db.UpsertTrackedIssue(ctx, TrackedIssue{
			ProjectID: testProjectID,
			Tool:      "redmine", ExternalID: fmt.Sprint(100 + i), Tracker: "Évolution", Subject: "s",
			StatusName: "x", StatusBucket: bucket,
		}); err != nil {
			t.Errorf("bucket %q refused by the database: %v", bucket, err)
		}
	}

	if err := db.UpsertTrackedIssue(ctx, TrackedIssue{
		ProjectID: testProjectID, Tool: "redmine", ExternalID: "200", Tracker: "Évolution", Subject: "s",
		StatusName: "x", StatusBucket: "invente",
	}); err == nil {
		t.Error("an invented bucket should be refused by the constraint")
	}
}

// The ordered list is the single source: the screen's columns, the administration
// selector and the database constraint must say the same thing.
func TestTheOrderedBucketListIsTheSingleSource(t *testing.T) {
	expected := []string{"todo", "doing", "client-test", "done", "rejected"}

	if len(Buckets) != 4 {
		t.Fatalf("%d progress buckets, expected 4 (rejected is outside progress)", len(Buckets))
	}
	// The order carries meaning: it is the columns' order.
	if Buckets[1] != BucketDoing || Buckets[2] != BucketClientTest || Buckets[3] != BucketDone {
		t.Errorf("order %v: client test must sit between doing and done", Buckets)
	}

	for _, bucket := range expected {
		if !ValidBucket(bucket) {
			t.Errorf("bucket %q should be accepted", bucket)
		}
	}
	for _, bucket := range []string{"", "invente", "DONE", "client_test"} {
		if ValidBucket(bucket) {
			t.Errorf("bucket %q should not be accepted", bucket)
		}
	}
}

// The case observed in practice: eight anomalies displayed as open, five of them in a
// status the tracker declares not closed while it maps to `done`.
//
// "Open" is read from the mapped bucket, never from `is_closed`. Using two notions of
// "closed" in the same screen is the underlying fault: the mapping is what the user
// sets and sees.
func TestAFinishedBugIsNotOpenEvenIfRedmineSaysItIsNotClosed(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")

	// The three really open ones.
	issueOf(t, db, 1, 805, "Anomalie", "Ready", BucketTodo, 0, false)
	issueOf(t, db, 2, 805, "Anomalie", "En cours", BucketDoing, 20, false)
	issueOf(t, db, 3, 805, "Anomalie", "En revue", BucketDoing, 80, false)

	// Five in a status mapped to `done`, but not closed according to the tracker. That is
	// precisely the combination that made them count as open.
	for i := 0; i < 5; i++ {
		issueOf(t, db, int64(10+i), 805, "Anomalie", "Testé", BucketDone, 100, false)
	}

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if summary.OpenAnomalies != 3 {
		t.Errorf("%d open anomalies, expected 3: the five in `done` are not",
			summary.OpenAnomalies)
	}
}

// An anomaly awaiting client approval is no longer for the team to handle: it does not
// count as open.
func TestABugInClientTestingIsNotOpen(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Anomalie", "En cours", BucketDoing, 0, false)
	issueOf(t, db, 2, 805, "Anomalie", "Livré", BucketClientTest, 100, false)
	issueOf(t, db, 3, 805, "Anomalie", "Rejeté", BucketRejected, 0, true)

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if summary.OpenAnomalies != 1 {
		t.Errorf("%d open anomalies, expected 1", summary.OpenAnomalies)
	}
}

// The reverse of the same divergence: one status is declared closed by the instance
// while it maps to `doing`. A reopened anomaly must count as open.
func TestAReopenedBugCountsAsOpen(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Anomalie", "Ré-ouvert", BucketDoing, 60, true)

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if summary.OpenAnomalies != 1 {
		t.Errorf("%d open anomalies, expected 1 despite is_closed", summary.OpenAnomalies)
	}
}

// The open buckets are todo and doing, and nothing else.
func TestOnlyTodoAndDoingAreOpen(t *testing.T) {
	for _, bucket := range []string{BucketTodo, BucketDoing} {
		if !BucketIsOpen(bucket) {
			t.Errorf("%q should be open", bucket)
		}
	}
	for _, bucket := range []string{BucketClientTest, BucketDone, BucketRejected, "invente"} {
		if BucketIsOpen(bucket) {
			t.Errorf("%q should not be open", bucket)
		}
	}
}

// Immediate attachment: when the tracker version already exists, a release tagged
// `3.2.0-rc1` — base `3.2.0` — must attach to `v3.2.0` directly. Searching the exact
// name alone created a phantom provisional version, adopted five minutes later;
// meanwhile the screen showed the release attached to a version "unknown to the
// tracker" that existed perfectly well.
func TestEnsureVersionAttachesToARealVersionByNormalisedName(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	realID := redmineVersion(t, db, 805, "v3.2.0", "open")

	id, err := db.EnsureVersion(ctx, testProjectID, "3.2.0")
	if err != nil {
		t.Fatalf("attachment: %v", err)
	}
	if id != realID {
		t.Errorf("attached to %d, expected the real version %d", id, realID)
	}

	// No provisional version must be born of that attachment.
	pendings, err := db.PendingVersions(ctx, testProjectID)
	if err != nil {
		t.Fatalf("provisional: %v", err)
	}
	if len(pendings) != 0 {
		t.Errorf("provisional versions wrongly created: %v", pendings)
	}
}

// The same preference as adoption: between a closed namesake and the active version,
// the release goes to the active one. Both paths — immediate attachment and deferred
// adoption — must name the same version, otherwise the result would depend on when the
// release was created.
func TestEnsureVersionPrefersTheActiveVersionOverTheClosedNamesake(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 803, "V3.1.2", "closed")
	activeID := redmineVersion(t, db, 804, "v3.1.2", "open")

	id, err := db.EnsureVersion(ctx, testProjectID, "3.1.2")
	if err != nil {
		t.Fatalf("attachment: %v", err)
	}
	if id != activeID {
		t.Errorf("attached to %d, expected the active version %d", id, activeID)
	}
}

// Two active versions of the same name: undecidable, hence provisional — choosing at
// random would attach the release to the wrong version, and the provisional one stays
// visible.
func TestEnsureVersionFallsBackToAProvisionalWhenUndecidable(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 803, "V3.1.2", "open")
	redmineVersion(t, db, 804, "v3.1.2", "open")

	if _, err := db.EnsureVersion(ctx, testProjectID, "3.1.2"); err != nil {
		t.Fatalf("attachment: %v", err)
	}

	pendings, err := db.PendingVersions(ctx, testProjectID)
	if err != nil {
		t.Fatalf("provisional: %v", err)
	}
	if len(pendings) != 1 || pendings[0] != "3.1.2" {
		t.Errorf("provisional: %v, expected only 3.1.2", pendings)
	}
}

// The missing link: a build's conclusion must be able to advance the release, and the
// write must be conditioned on the state that was read.
func TestAJobCarriesItsReleaseAndItsState(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc1", Kind: "pre", State: "building",
		GitRef: "develop", ImageVersion: "3.2.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	jobID, err := db.Enqueue(ctx, JobRequest{
		Kind: "build", Params: map[string]string{"revision": "develop", "version": "3.2.0-rc1"},
		RequestedBy: "vcarroy", ReleaseID: &releaseID,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	linked, err := db.JobRelease(ctx, jobID)
	if err != nil {
		t.Fatalf("release of the job: %v", err)
	}
	if linked.Kind != "build" || linked.Tag != "3.2.0-rc1" || linked.State != "building" {
		t.Errorf("context: %+v", linked)
	}
}

// A job with no release is not an error: launching tests by hand from the queue names
// none, and treating it as a fault would fill the logs with false problems.
func TestAJobWithNoReleaseIsToldApartFromAnError(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	jobID, err := db.Enqueue(ctx, JobRequest{
		Kind: "static-tests", RequestedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	if _, err := db.JobRelease(ctx, jobID); !errors.Is(err, ErrJobWithoutRelease) {
		t.Errorf("error %v, expected ErrJobWithoutRelease", err)
	}
}

// The write is conditioned on the state that was read: between enqueueing and the end
// of the run, a manual retry may have moved the release, and overwriting it would erase
// that decision in favour of a pipeline that was no longer the last word.
func TestAdvancingDoesNotOverwriteAStateMovedMeanwhile(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc1", Kind: "pre", State: "building",
		GitRef: "develop", ImageVersion: "3.2.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	// Someone moves the release while the pipeline runs.
	if err := db.SetReleaseState(ctx, releaseID, "failed"); err != nil {
		t.Fatalf("move: %v", err)
	}

	// The conclusion arrives with the state it had read at the start.
	advanced, err := db.AdvanceReleaseState(ctx, releaseID, "building", "ready")
	if err != nil {
		t.Fatalf("avancement: %v", err)
	}
	if advanced {
		t.Error("the advance should not have succeeded on a moved state")
	}

	current, err := db.GetReleaseByID(ctx, releaseID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if current.State != "failed" {
		t.Errorf("state %q, expected failed to be kept", current.State)
	}

	// From the real state, the advance goes through.
	advanced, err = db.AdvanceReleaseState(ctx, releaseID, "failed", "building")
	if err != nil {
		t.Fatalf("avancement: %v", err)
	}
	if !advanced {
		t.Error("the advance from the real state should have succeeded")
	}
}

// A release leaves `verifying` only when every verification has returned its verdict.
// Without that count it stayed there indefinitely — observed on a release blocked while
// both its tests had concluded.
func TestAReleaseLeavesVerifyingOnlyWhenEverythingConcluded(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc1", Kind: "pre", State: "verifying",
		GitRef: "main", GitCommit: "abc123", ImageVersion: "3.2.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	kinds := []string{"static-tests", "security-audit"}
	var jobs []int64
	for _, kind := range kinds {
		id, err := db.Enqueue(ctx, JobRequest{
			Kind: kind, RequestedBy: "dashboard:verification",
			ReleaseID: &releaseID,
		})
		if err != nil {
			t.Fatalf("enqueue %s: %v", kind, err)
		}
		jobs = append(jobs, id)
	}

	// Both pending: nothing is concluded.
	tally, err := db.CountVerifications(ctx, releaseID, kinds, "build")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if tally.Done() || tally.Pending != 2 {
		t.Fatalf("initial count: %+v", tally)
	}

	// The first concludes: still not finished.
	if err := db.FinishJob(ctx, jobs[0], JobSucceeded, ""); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	tally, _ = db.CountVerifications(ctx, releaseID, kinds, "build")
	if tally.Done() {
		t.Error("one verification out of two does not conclude the release")
	}

	// The second fails: finished, but with a failure.
	if err := db.FinishJob(ctx, jobs[1], JobFailed, "clone refused"); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	tally, _ = db.CountVerifications(ctx, releaseID, kinds, "build")
	if !tally.Done() {
		t.Error("every verification concluded")
	}
	if tally.Failed != 1 || tally.Succeeded != 1 {
		t.Errorf("final count: %+v", tally)
	}
}

// Jobs of another release, or of another kind, do not count.
func TestTheCountLooksOnlyAtTheRightRelease(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	mine, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc1", Kind: "pre", State: "verifying",
		ImageVersion: "3.2.0-rc1", CreatedBy: "vcarroy",
	})
	other, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc2", Kind: "pre", State: "verifying",
		ImageVersion: "3.2.0-rc2", CreatedBy: "vcarroy",
	})

	// The build comes first, as in production: it is what bounds the series of
	// verifications that follows.
	if _, err := db.Enqueue(ctx, JobRequest{
		Kind: "build", RequestedBy: "x", ReleaseID: &mine,
	}); err != nil {
		t.Fatalf("building: %v", err)
	}
	if _, err := db.Enqueue(ctx, JobRequest{
		Kind: "static-tests", RequestedBy: "x", ReleaseID: &other,
	}); err != nil {
		t.Fatalf("job of another release: %v", err)
	}

	kinds := []string{"static-tests", "security-audit"}
	tally, err := db.CountVerifications(ctx, mine, kinds, "build")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if tally.Pending != 0 || tally.Failed != 0 || tally.Succeeded != 0 {
		t.Errorf("count: %+v — neither the build nor the neighbouring release counts", tally)
	}
	// And an empty series is not concluded: between writing `verifying` and queueing the
	// tests it momentarily is, and a late verdict redelivered at that instant would move
	// the release to `ready` without a single test having run.
	if tally.Done() {
		t.Error("an empty series concludes nothing")
	}
}

// A release retried after a failure drags the verifications of its previous attempts
// along. Counting them would fail a release whose second series passes entirely — and
// the first gesture the interface offers on a failed release is precisely "rebuild".
func TestARetryCountsOnlyItsOwnVerifications(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc1", Kind: "pre", State: "verifying",
		GitRef: "main", GitCommit: "abc123", ImageVersion: "3.2.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	kinds := []string{"static-tests", "security-audit"}

	enqueue := func(kind string) int64 {
		t.Helper()
		id, err := db.Enqueue(ctx, JobRequest{
			Kind: kind, RequestedBy: "dashboard", ReleaseID: &releaseID,
		})
		if err != nil {
			t.Fatalf("enqueue %s: %v", kind, err)
		}
		return id
	}

	// First series: the build passes, the audit fails.
	enqueue("build")
	first := []int64{enqueue("static-tests"), enqueue("security-audit")}
	if err := db.FinishJob(ctx, first[0], JobSucceeded, ""); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	if err := db.FinishJob(ctx, first[1], JobFailed, "clone refused"); err != nil {
		t.Fatalf("conclusion: %v", err)
	}

	// Second series, after a re-run: everything passes.
	enqueue("build")
	second := []int64{enqueue("static-tests"), enqueue("security-audit")}
	for _, id := range second {
		if err := db.FinishJob(ctx, id, JobSucceeded, ""); err != nil {
			t.Fatalf("conclusion: %v", err)
		}
	}

	tally, err := db.CountVerifications(ctx, releaseID, kinds, "build")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if !tally.Done() {
		t.Fatal("the second series concluded")
	}
	if tally.Failed != 0 || tally.Succeeded != 2 {
		t.Errorf("count: %+v — the first series' failure is still counted", tally)
	}
}

// The defect that left a release failed while its three families were green: the count
// accumulated the attempts of one family. Three integration failures followed by a
// success counted three failures, and the release stayed failed over verdicts the last
// attempt had replaced — so the replay button could catch nothing up.
func TestOnlyTheLastAttemptOfAFamilyCounts(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc3", Kind: "pre", State: "failed",
		GitRef: "main", GitCommit: "abc123", ImageVersion: "3.2.0-rc3", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	kinds := []string{"static-tests", "security-audit", "integration-tests"}

	enqueue := func(kind string) int64 {
		t.Helper()
		id, err := db.Enqueue(ctx, JobRequest{
			Kind: kind, RequestedBy: "dashboard", ReleaseID: &releaseID,
		})
		if err != nil {
			t.Fatalf("enqueue %s: %v", kind, err)
		}
		return id
	}
	conclude := func(id int64, state string) {
		t.Helper()
		if err := db.FinishJob(ctx, id, state, ""); err != nil {
			t.Fatalf("conclusion: %v", err)
		}
	}

	enqueue("build")
	conclude(enqueue("static-tests"), JobSucceeded)
	conclude(enqueue("security-audit"), JobSucceeded)

	// Integration fails three times, then passes.
	for i := 0; i < 3; i++ {
		conclude(enqueue("integration-tests"), JobFailed)
	}
	conclude(enqueue("integration-tests"), JobSucceeded)

	tally, err := db.CountVerifications(ctx, releaseID, kinds, "build")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if !tally.Done() {
		t.Fatal("all three families concluded")
	}
	if tally.Failed != 0 {
		t.Errorf("%d failure(s) counted — the previous attempts still weigh", tally.Failed)
	}
	if tally.Succeeded != 3 {
		t.Errorf("%d success(es), expected one per family", tally.Succeeded)
	}
}

// And the converse: a failed last attempt counts, even preceded by successes.
func TestAFailedLastAttemptCounts(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc4", Kind: "pre", State: "verifying",
		ImageVersion: "3.2.0-rc4", CreatedBy: "vcarroy",
	})

	enqueue := func(kind string) int64 {
		id, err := db.Enqueue(ctx, JobRequest{
			Kind: kind, RequestedBy: "dashboard", ReleaseID: &releaseID,
		})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		return id
	}

	enqueue("build")
	first := enqueue("static-tests")
	if err := db.FinishJob(ctx, first, JobSucceeded, ""); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	second := enqueue("static-tests")
	if err := db.FinishJob(ctx, second, JobFailed, "regression"); err != nil {
		t.Fatalf("conclusion: %v", err)
	}

	tally, err := db.CountVerifications(ctx, releaseID, []string{"static-tests"}, "build")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if tally.Failed != 1 || tally.Succeeded != 0 {
		t.Errorf("count %+v — the last attempt must win", tally)
	}
}

// Two projects tracked together each carry their own `3.1.2`, and nothing must mix
// them.
//
// That is what the migration is for, and the property easiest to lose without
// noticing: matching is by normalised name, and a query that forgets the project
// silently attaches one's release to the other's version. Both exist, no error is
// raised, and the screen shows a scope that is not its own.
func TestTwoProjectsDoNotStealEachOthersVersions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	var second int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, dockerfile, build_context, created_by)
		 values ('autre', 'vcarroy/autre', 'Autre', 'https://forge.example', 'Dockerfile', '.', 'test')
		 returning id`).Scan(&second); err != nil {
		t.Fatalf("second project: %v", err)
	}

	// The same number, declared on both sides. The same tracker identifier too: two
	// projects may track two instances, or two projects of the same one.
	for _, projectID := range []int64{testProjectID, second} {
		if _, err := db.UpsertTrackedVersion(ctx, TrackedVersion{
			ProjectID: projectID, Tool: "redmine", ExternalID: fmt.Sprint(900), Name: "v3.1.2",
			Normalized: release.NormalizeVersionName("v3.1.2"), Status: "open",
		}); err != nil {
			t.Fatalf("version of project %d: %v", projectID, err)
		}
	}

	// A release of the first project must find its own version.
	versionID, err := db.EnsureVersion(ctx, testProjectID, "3.1.2")
	if err != nil {
		t.Fatalf("attachment: %v", err)
	}

	var owner int64
	if err := db.Pool().QueryRow(ctx,
		`select project_id from versions where id = $1`, versionID).Scan(&owner); err != nil {
		t.Fatalf("reading the project: %v", err)
	}
	if owner != testProjectID {
		t.Errorf("version attached to project %d, expected %d", owner, testProjectID)
	}

	// And the tag is unique only within a project: the same `1.0.0` must be able to exist
	// on both sides.
	for _, projectID := range []int64{testProjectID, second} {
		version, err := db.EnsureVersion(ctx, projectID, "1.0.0")
		if err != nil {
			t.Fatalf("version 1.0.0 of project %d: %v", projectID, err)
		}
		if _, err := db.CreateRelease(ctx, Release{
			ProjectID: projectID, VersionID: version, Tag: "1.0.0",
			Kind: "final", State: "draft", ImageVersion: "1.0.0", CreatedBy: "test",
		}); err != nil {
			t.Fatalf("release 1.0.0 of project %d: %v", projectID, err)
		}
	}

	first, err := db.GetRelease(ctx, testProjectID, "1.0.0")
	if err != nil {
		t.Fatalf("reading the first project's release: %v", err)
	}
	other, err := db.GetRelease(ctx, second, "1.0.0")
	if err != nil {
		t.Fatalf("reading the second project's release: %v", err)
	}
	if first.ID == other.ID {
		t.Error("both projects share the same release: tag uniqueness stayed global")
	}
}

// A project with no tracking tool produces definitive versions, never provisional
// ones.
//
// That is the case of projects versioned by git tags alone, with no tracker. The
// dashboard made them a provisional version awaiting an adoption that would never
// come — the anomaly state made permanent, and a screen announcing indefinitely a lag
// against a tool that does not exist.
func TestAProjectWithoutATrackerProducesDefinitiveVersions(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	var untracked int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, build_context, created_by)
		 values ('theme', 'CNOUS/hydra-sql-mse-theme', 'Theme', 'https://forge.example', '.', 'test')
		 returning id`).Scan(&untracked); err != nil {
		t.Fatalf("project with no tracker: %v", err)
	}

	versionID, err := db.EnsureVersion(ctx, untracked, "1.2.0")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	var origin, status string
	if err := db.Pool().QueryRow(ctx,
		`select origin, status from versions where id = $1`, versionID).Scan(&origin, &status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if origin != "own" {
		t.Errorf("origin = %q, expected own", origin)
	}
	// The status follows the origin: a version that stands on its own waits for nothing,
	// and displaying it as "pending" would be false.
	if status != "open" {
		t.Errorf("status = %q, expected open", status)
	}

	// And adoption must not look at it: there is nothing to match.
	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 0 {
		t.Errorf("adoption examined a version with no tracker: %+v", report)
	}
}

// And the converse: a tracked project does produce a provisional version, which
// synchronisation can adopt.
func TestATrackedProjectProducesAProvisionalVersion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, err := db.EnsureVersion(ctx, testProjectID, "3.2.0")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	var origin string
	if err := db.Pool().QueryRow(ctx,
		`select origin from versions where id = $1`, versionID).Scan(&origin); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if origin != "pending" {
		t.Errorf("origin = %q, expected pending", origin)
	}
}

// externalRef turns a version's numeric identifier into the textual reference the
// column now carries.
func externalRef(id int64) *string {
	reference := fmt.Sprint(id)
	return &reference
}

// A version the dashboard gave itself carries no external identifier at all, and no
// longer a negative integer.
//
// The workaround existed to satisfy a not-null constraint on a column that had no
// value to carry: one had to read the sign of a number to know a nature. `origin` says
// the nature, `external_id` stays null.
func TestAnOwnVersionHasNoExternalIdentifier(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	var untracked int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, build_context, created_by)
		 values ('theme', 'CNOUS/theme', 'Theme', 'https://forge.example', '.', 'test')
		 returning id`).Scan(&untracked); err != nil {
		t.Fatalf("project: %v", err)
	}

	versionID, err := db.EnsureVersion(ctx, untracked, "1.2.0")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	var tool string
	var external *string
	if err := db.Pool().QueryRow(ctx,
		`select tool, external_id from versions where id = $1`, versionID).Scan(&tool, &external); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if tool != "" {
		t.Errorf("tool = %q, expected empty", tool)
	}
	if external != nil {
		t.Errorf("external identifier = %q, expected null", *external)
	}
}

// Two tools may number alike without clashing: uniqueness is on the triple project +
// tool + identifier.
func TestTwoToolsMayShareAnIdentifier(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.UpsertTrackedVersion(ctx, TrackedVersion{
		ProjectID: testProjectID, Tool: "redmine", ExternalID: "42",
		Name: "v1.0.0", Normalized: "1.0.0", Status: "open",
	}); err != nil {
		t.Fatalf("version Redmine: %v", err)
	}

	// The same number, in another tool. A single integer unique per project would have
	// refused it with no message saying why.
	if _, err := db.UpsertTrackedVersion(ctx, TrackedVersion{
		ProjectID: testProjectID, Tool: "jira", ExternalID: "42",
		Name: "MSE-42", Normalized: "MSE-42", Status: "open",
	}); err != nil {
		t.Fatalf("version Jira: %v", err)
	}

	var count int
	if err := db.Pool().QueryRow(ctx,
		`select count(*) from versions where external_id = '42'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("%d version(s) carrying identifier 42, expected 2", count)
	}
}

// The version list is bounded to its project.
//
// It was not: the `/projects/{p}/versions` route passed its project and the query
// ignored it. A project with no version displayed its neighbour's, with no error saying
// so — the worst failure mode of multi-project work, the one where a client's screen
// shows another's data.
func TestTheVersionListDoesNotSpillOntoAnotherProject(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	var second int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, dockerfile, build_context,
		                       tracker_project, created_by)
		 values ('autre', 'vcarroy/autre', 'Autre', 'https://forge.example', 'Dockerfile', '.',
		         'autre-suivi', 'test')
		 returning id`).Scan(&second); err != nil {
		t.Fatalf("second project: %v", err)
	}

	redmineVersion(t, db, 900, "v3.1.0", "open")
	if _, err := db.UpsertTrackedVersion(ctx, TrackedVersion{
		ProjectID: second, Tool: "redmine", ExternalID: "901", Name: "v9.9.9",
		Normalized: release.NormalizeVersionName("v9.9.9"), Status: "open",
	}); err != nil {
		t.Fatalf("version of the second project: %v", err)
	}

	summaries, err := db.ListVersionSummaries(ctx, testProjectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Name != "v3.1.0" {
		t.Fatalf("versions returned: %+v — another project's spill over", summaries)
	}
	if summaries[0].ProjectID != testProjectID {
		t.Errorf("project of the version = %d", summaries[0].ProjectID)
	}

	// And conversely: the second project sees only its own.
	others, _ := db.ListVersionSummaries(ctx, second)
	if len(others) != 1 || others[0].Name != "v9.9.9" {
		t.Errorf("versions of the second project: %+v", others)
	}
}
