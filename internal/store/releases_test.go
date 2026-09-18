package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEnsureVersionCreatesThenReuses(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	// Without tracker synchronisation the table is empty, and no release could be created
	// while the rest of the chain works.
	first, err := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("creation: %v", err)
	}

	second, err := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if first != second {
		t.Errorf("two versions created for one name: %d then %d", first, second)
	}

	// The provisional row must be recognisable, so synchronisation knows which one to
	// replace.
	var origin, status string
	var externalID *string
	if err := db.Pool().QueryRow(ctx,
		`select origin, external_id, status from versions where id = $1`, first).
		Scan(&origin, &externalID, &status); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if origin != VersionOriginPending {
		t.Errorf("origin = %q, expected %q for a provisional version", origin, VersionOriginPending)
	}
	// A provisional version has nothing to name in the tracker: that is precisely what it is
	// waiting for.
	if externalID != nil {
		t.Errorf("external_id = %q, expected null", *externalID)
	}
	if status != "en-attente-de-synchronisation" {
		t.Errorf("statut = %q", status)
	}
}

func TestCreateAndGetRelease(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, err := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	id, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0-rc1", Kind: "pre", State: "draft",
		GitRef: "abc1234", ImageVersion: "1.8.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("creation: %v", err)
	}

	release, err := db.GetRelease(ctx, testProjectID, "1.8.0-rc1")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if release.ID != id || release.Kind != "pre" || release.ImageVersion != "1.8.0-rc1" {
		t.Errorf("release = %+v", release)
	}
}

func TestGetReleaseDistinguishesAbsence(t *testing.T) {
	db := migrated(t)

	if _, err := db.GetRelease(context.Background(), testProjectID, "9.9.9"); !errors.Is(err, ErrReleaseNotFound) {
		t.Errorf("error = %v, expected ErrReleaseNotFound", err)
	}
}

// The re-tag: the final release carries its own tag but its candidate's image. That is
// the gap `image_version` absorbs, and without which any comparison with the running
// version would fail on a final release.
func TestFinalReleaseKeepsTheSourceImageVersion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")

	sourceID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0-rc3", Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("RC: %v", err)
	}

	if _, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", SourceReleaseID: &sourceID,
		CreatedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("finale: %v", err)
	}

	final, err := db.GetRelease(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if final.ImageVersion != "1.8.0-rc3" {
		t.Errorf("image version = %q, expected the candidate's", final.ImageVersion)
	}
	if final.SourceReleaseID == nil || *final.SourceReleaseID != sourceID {
		t.Errorf("source release = %v, expected %d", final.SourceReleaseID, sourceID)
	}
}

func TestManualActionsCarryTheirAuthorAndDate(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	if _, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", CreatedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("release: %v", err)
	}

	if err := db.SetManualActions(ctx, "1.8.0", "- cache purge\n- DNS switchover", "vcarroy"); err != nil {
		t.Fatalf("actions: %v", err)
	}

	release, err := db.GetRelease(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if release.ManualActions == "" {
		t.Error("texte perdu")
	}
	// This text ends up in a contractual document: its provenance is part of it.
	if release.ManualActionsUpdatedBy != "vcarroy" || release.ManualActionsUpdatedAt == nil {
		t.Errorf("auteur = %q, date = %v", release.ManualActionsUpdatedBy, release.ManualActionsUpdatedAt)
	}
}

func TestSetManualActionsOnAnUnknownRelease(t *testing.T) {
	db := migrated(t)

	if err := db.SetManualActions(context.Background(), "9.9.9", "texte", "vcarroy"); !errors.Is(err, ErrReleaseNotFound) {
		t.Errorf("error = %v, expected ErrReleaseNotFound", err)
	}
}

// buildJob queues an image build and concludes it.
func buildJob(t *testing.T, db *DB, releaseID int64, image, state string) int64 {
	t.Helper()
	ctx := context.Background()

	id, err := db.Enqueue(ctx, JobRequest{
		Kind:        "build",
		Params:      map[string]string{"image-name": image, "target": "web"},
		RequestedBy: "vcarroy",
		ReleaseID:   &releaseID,
	})
	if err != nil {
		t.Fatalf("queueing %s: %v", image, err)
	}
	if state != JobQueued {
		if err := db.FinishJob(ctx, id, state, ""); err != nil {
			t.Fatalf("concluding %s: %v", image, err)
		}
	}
	return id
}

/*
 * Le décompte des constructions.
 *
 * Une release en lance plusieurs — le pipeline ne construit qu'une image par
 * exécution — et n'est construite que lorsque toutes ont abouti. Compter en base
 * plutôt que retenir un état en mémoire est ce qui rend la conclusion correcte
 * après un redémarrage du worker : les runs continuent pendant qu'il ne tourne
 * pas.
 */
func TestTheBuildCountWaitsForEveryImage(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 930, "v3.3.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.3.0-rc1", Kind: "pre", State: "building",
		ImageVersion: "3.3.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	buildJob(t, db, releaseID, "portal", JobSucceeded)
	buildJob(t, db, releaseID, "portal-cli", JobSucceeded)
	buildJob(t, db, releaseID, "portal-dev", JobQueued)

	tally, err := db.CountBuilds(ctx, releaseID, "build")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if tally.Done() {
		t.Errorf("series concluded while an image is still to build: %+v", tally)
	}
	if tally.Pending != 1 || tally.Succeeded != 2 {
		t.Errorf("count = %+v", tally)
	}

	if err := db.FinishJob(ctx, buildJob(t, db, releaseID, "portal-dev", JobQueued),
		JobSucceeded, ""); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	// The last attempt of each image, not all: the first queueing of one image stays `queued`
	// and must no longer count.
	tally, _ = db.CountBuilds(ctx, releaseID, "build")
	if !tally.Done() || tally.Succeeded != 3 {
		t.Errorf("count after the last image = %+v", tally)
	}
}

// One failure is enough to disqualify the series, even when the other images pass.
func TestAFailedImagePreventsTheConclusion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 931, "v3.4.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.4.0-rc1", Kind: "pre", State: "building",
		ImageVersion: "3.4.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	buildJob(t, db, releaseID, "portal", JobSucceeded)
	buildJob(t, db, releaseID, "portal-cli", JobFailed)

	tally, _ := db.CountBuilds(ctx, releaseID, "build")
	if tally.Done() || tally.Failed != 1 {
		t.Errorf("count = %+v", tally)
	}

	// The retry replaces the previous attempt: a release whose second attempt passes entirely
	// must not stay failed over a verdict the last attempt replaced.
	buildJob(t, db, releaseID, "portal-cli", JobSucceeded)
	tally, _ = db.CountBuilds(ctx, releaseID, "build")
	if !tally.Done() || tally.Failed != 0 {
		t.Errorf("count after the retry = %+v", tally)
	}
}

// An empty series is not concluded: between writing `building` and queueing the builds
// there is an instant when the count is zero, and a late redelivery would see "everything
// succeeded".
func TestAnEmptySeriesIsNotConcluded(t *testing.T) {
	db := migrated(t)

	versionID := redmineVersion(t, db, 932, "v3.5.0", "open")
	releaseID, err := db.CreateRelease(context.Background(), Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.5.0-rc1", Kind: "pre", State: "building",
		ImageVersion: "3.5.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	tally, err := db.CountBuilds(context.Background(), releaseID, "build")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if tally.Done() {
		t.Error("an empty series must not be concluded")
	}
}

// A release belongs to its project, and the database requires it.
//
// The creation handler did not set ProjectID after the migration attached releases to
// their project. An int64's zero value is not null: it is identifier 0, which names no
// project, and the foreign key refused the insert.
//
// Nobody saw it because the failure read as "this release may already exist" — the handler
// translated every database error into a conflict. The message sent someone looking for a
// duplicate while the real cause stayed invisible.
func TestAReleaseWithoutAProjectIsRefusedByTheDatabase(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, err := db.EnsureVersion(ctx, testProjectID, "3.2.1")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	_, err = db.CreateRelease(ctx, Release{
		VersionID: versionID, Tag: "3.2.1-rc1", Kind: "pre", State: "draft",
		ImageVersion: "3.2.1-rc1", CreatedBy: "vcarroy",
	})
	if err == nil {
		t.Fatal("a release with no project must be refused: nothing would say whom it belongs to")
	}

	// With its project it passes: that is the only difference.
	if _, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.1-rc1", Kind: "pre", State: "draft",
		ImageVersion: "3.2.1-rc1", CreatedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("release with a project refused: %v", err)
	}
}

// A duplicate is a uniqueness violation, and not just any error: that is what the handler
// must be able to tell apart in order to return a 409 rather than a mute 500.
func TestATagAlreadyTakenInTheProjectIsADuplicate(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "3.2.1")
	release := Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.1-rc1", Kind: "pre", State: "draft",
		ImageVersion: "3.2.1-rc1", CreatedBy: "vcarroy",
	}

	if _, err := db.CreateRelease(ctx, release); err != nil {
		t.Fatalf("first creation: %v", err)
	}

	_, err := db.CreateRelease(ctx, release)
	if err == nil {
		t.Fatal("the same tag twice within a project must be refused")
	}
	if !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error %v — expected a uniqueness violation", err)
	}
}
