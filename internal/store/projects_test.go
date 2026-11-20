package store

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/release"
)

/*
 * The driven repositories.
 *
 * What deserves to be held here: the repository's uniqueness — two entries for the same one would
 * describe the same thing with nothing saying which is authoritative — and the access
 * observation, whose absence must not read as a failure.
 */

func project(t *testing.T, db *DB, repository string) int64 {
	t.Helper()

	id, err := db.CreateProject(context.Background(), Project{
		Repository: repository, Label: "Portail", ForgeURL: "https://forge.example.fr",
		Dockerfile: "./misc/k8s/images/portal/Dockerfile", BuildContext: ".",
		MultiStage: true,
		Targets: []Target{
			{Stage: "web", Image: "portal"},
			{Stage: "cli", Image: "portal-cli"},
		},
		CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("declaring %s: %v", repository, err)
	}
	return id
}

func TestAProjectIsDeclaredAndReadBack(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id := project(t, db, "team/mstore")

	read, err := db.GetProject(ctx, id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if read.Repository != "team/mstore" || read.Label != "Portail" {
		t.Errorf("project = %+v", read)
	}
	if len(read.Targets) != 2 || read.Targets[1].Image != "portal-cli" {
		t.Errorf("stages = %+v", read.Targets)
	}
	// No access check has happened: the absence of a verdict must be told apart from a
	// check that failed.
	if read.CheckedAt != nil || read.CheckOK != nil {
		t.Errorf("verdict set with no check: %+v", read)
	}
}

// Two entries for one repository would describe the same thing, with nothing saying
// which prevails.
func TestARepositoryIsNotDeclaredTwice(t *testing.T) {
	db := migratedBare(t)

	project(t, db, "team/mstore")

	_, err := db.CreateProject(context.Background(), Project{
		Repository: "team/mstore", Label: "Doublon", CreatedBy: "vcarroy",
	})
	if !errors.Is(err, ErrProjectExists) {
		t.Errorf("second declaration: %v, expected ErrProjectExists", err)
	}
}

// A half-filled form row does not become a stage: it would have nothing to build, and
// nothing on screen would tell it from a valid stage.
func TestACorrectionDropsEmptyStages(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id := project(t, db, "team/mstore")

	if err := db.UpdateProject(ctx, Project{
		ID: id, Repository: "team/mstore", Label: "Portail Mstore",
		ForgeURL: "https://forge.example.fr", Dockerfile: "Dockerfile",
		BuildContext: ".", MultiStage: false,
		Targets: []Target{{Stage: "", Image: ""}, {Stage: "web", Image: "portal"}},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if read.Label != "Portail Mstore" || read.MultiStage {
		t.Errorf("project = %+v", read)
	}
	if len(read.Targets) != 1 || read.Targets[0].Stage != "web" {
		t.Errorf("stages = %+v", read.Targets)
	}
}

func recordCheck(db *DB, ctx context.Context, id int64, ok bool, note string) error {
	_, err := db.RecordProjectProbe(ctx, id, ProjectProbe{OK: ok, Note: note})
	return err
}

func TestTheAccessVerdictIsKept(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id := project(t, db, "team/mstore")

	if err := recordCheck(db, ctx, id, false, "404 on /repos/team/mstore"); err != nil {
		t.Fatalf("check: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if read.CheckedAt == nil || read.CheckOK == nil || *read.CheckOK {
		t.Fatalf("check = %+v", read)
	}
	if read.CheckNote == "" {
		t.Error("the failure reason must be kept: without it, \"access refused\" does not help")
	}

	// A second check replaces the first: it is a verdict, not a log.
	if err := recordCheck(db, ctx, id, true, "default branch: main"); err != nil {
		t.Fatalf("second check: %v", err)
	}
	read, _ = db.GetProject(ctx, id)
	if read.CheckOK == nil || !*read.CheckOK {
		t.Errorf("verdict after retry = %+v", read)
	}
}

func TestAMissingProjectIsToldApartFromAnError(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	if _, err := db.GetProject(ctx, 4242); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("reading a missing project: %v", err)
	}
	if err := db.DeleteProject(ctx, 4242); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("deleting a missing project: %v", err)
	}
	if err := recordCheck(db, ctx, 4242, true, ""); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("verdict on a missing project: %v", err)
	}
}

/*
 * Seeding from the configuration.
 *
 * It exists because the table must describe what the dashboard actually does, not what an
 * administrator will have thought to retype into it. Two properties make it safe: it is
 * replayable, and it overwrites nothing.
 */
func TestTheBootstrapIsReplayableAndOverwritesNothing(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	configured := []Project{
		{
			Repository: "team/mstore", Label: "Application",
			ForgeURL: "https://forge.example.fr", Dockerfile: "Dockerfile",
			BuildContext: ".", MultiStage: true,
			Targets:   []Target{{Stage: "web", Image: "portal"}},
			CreatedBy: "configuration",
		},
		{
			Repository: "team/mstore-deploy", Label: "Kustomizations",
			ForgeURL: "https://forge.example.fr", BuildContext: ".",
			CreatedBy: "configuration",
		},
		// A URL from which `owner/name` could not be derived: discarded rather than declared,
		// since a repository naming nothing would give a failed access check with no way to know
		// the fault was upstream.
		{Repository: "", Label: "Illisible"},
	}

	declared, err := db.SeedProjects(ctx, configured)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if declared != 2 {
		t.Errorf("%d project(s) declared, expected 2", declared)
	}

	// An administrator corrects a label.
	projects, _ := db.ListProjects(ctx)
	var mstore Project
	for _, project := range projects {
		if project.Repository == "team/mstore" {
			mstore = project
		}
	}
	if err := db.UpdateProject(ctx, Project{
		ID: mstore.ID, Repository: mstore.Repository, Label: "Portail Mstore",
		ForgeURL: mstore.ForgeURL, Dockerfile: mstore.Dockerfile,
		BuildContext: mstore.BuildContext, MultiStage: mstore.MultiStage,
		Targets: mstore.Targets,
	}); err != nil {
		t.Fatalf("correction: %v", err)
	}

	// A restart replays the bootstrap: it must declare nothing, and above all rewrite
	// nothing.
	declared, err = db.SeedProjects(ctx, configured)
	if err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if declared != 0 {
		t.Errorf("%d project(s) redeclared", declared)
	}

	read, _ := db.GetProject(ctx, mstore.ID)
	if read.Label != "Portail Mstore" {
		t.Errorf("label = %q: the bootstrap overwrote a correction", read.Label)
	}
}

// The repository is editable, and that is the main use case: bootstrapped projects carry
// the one the configuration named, and correcting it is exactly what this is for.
func TestCorrectingTheRepositoryClearsTheVerdict(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id := project(t, db, "vcarroy/mstore-test-project")
	if err := recordCheck(db, ctx, id, true, "default branch: main"); err != nil {
		t.Fatalf("check: %v", err)
	}

	corrected := Project{
		ID: id, Repository: "team/mstore", Label: "Portail",
		ForgeURL: "https://forge.example.fr", Dockerfile: "Dockerfile",
		BuildContext: ".", MultiStage: false,
	}
	if err := db.UpdateProject(ctx, corrected); err != nil {
		t.Fatalf("correction: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if read.Repository != "team/mstore" {
		t.Errorf("repository = %q", read.Repository)
	}
	// The verdict was about something no longer driven. Keeping it would display "access
	// verified" on a repository nobody tested.
	if read.CheckedAt != nil || read.CheckOK != nil || read.CheckNote != "" {
		t.Errorf("verdict kept after changing repository: %+v", read)
	}
}

// Changing anything other than the repository keeps the verdict: redoing it at every
// label correction would make it useless.
func TestACorrectionKeepingTheRepositoryKeepsTheCheck(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id := project(t, db, "team/mstore")
	if err := recordCheck(db, ctx, id, true, "default branch: main"); err != nil {
		t.Fatalf("check: %v", err)
	}

	if err := db.UpdateProject(ctx, Project{
		ID: id, Repository: "team/mstore", Label: "Portail Mstore",
		ForgeURL: "https://forge.example.fr", BuildContext: ".",
	}); err != nil {
		t.Fatalf("correction: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if read.CheckedAt == nil || read.CheckOK == nil || !*read.CheckOK {
		t.Errorf("verdict lost on a label correction: %+v", read)
	}
}

// The edit form does not carry the cycle: a correction that names none keeps the recorded one.
func TestACorrectionWithoutCycleKeepsTheRecordedKinds(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id := project(t, db, "team/mstore")
	cycle := release.Cycle{{ID: "rc", Label: "RC", Mode: release.ModeBuild}}
	base := Project{ID: id, Repository: "team/mstore", Label: "Portail", BuildContext: "."}

	declared := base
	declared.ReleaseKinds = cycle
	if err := db.UpdateProject(ctx, declared); err != nil {
		t.Fatalf("declaring the cycle: %v", err)
	}
	if err := db.UpdateProject(ctx, base); err != nil {
		t.Fatalf("correction: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if len(read.ReleaseKinds) != 1 || read.ReleaseKinds[0].ID != "rc" {
		t.Errorf("cycle after a correction = %+v, want the declared rc", read.ReleaseKinds)
	}
}

// Two entries for one repository would describe the same thing: the refusal holds for a
// correction too, and must be told apart from a write failure.
func TestCorrectingTowardsADeclaredRepositoryIsRefused(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	project(t, db, "team/mstore")
	id := project(t, db, "team/mstore-deploy")

	err := db.UpdateProject(ctx, Project{
		ID: id, Repository: "team/mstore", Label: "Doublon",
		ForgeURL: "https://forge.example.fr", BuildContext: ".",
	})
	if !errors.Is(err, ErrProjectExists) {
		t.Errorf("correcting towards a declared repository: %v, expected ErrProjectExists", err)
	}
}

// An empty repository is never an intention: it is a caller that forgot the field.
// Accepting it would clear it silently, and the project would become a row naming
// nothing.
func TestACorrectionWithoutRepositoryIsRefused(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id := project(t, db, "team/mstore")

	if err := db.UpdateProject(ctx, Project{ID: id, Label: "No repository"}); err == nil {
		t.Fatal("an empty repository should be refused")
	}

	read, _ := db.GetProject(ctx, id)
	if read.Repository != "team/mstore" {
		t.Errorf("repository = %q: it was cleared", read.Repository)
	}
}

// The full reference is what names an image. A short name alone looks like a pullable
// reference without being one.
func TestTheReferenceCombinesRegistryAndImage(t *testing.T) {
	project := Project{ImageRegistry: "reg.example.fr/mstore"}

	if got := project.Reference(Target{Stage: "cli", Image: "portal-cli"}); got != "reg.example.fr/mstore/portal-cli" {
		t.Errorf("reference = %q", got)
	}
	// Trailing slashes must not double up: `project//portal` is not the same image as
	// `project/portal` to a registry.
	sloppy := Project{ImageRegistry: "reg.example.fr/mstore/"}
	if got := sloppy.Reference(Target{Image: "portal"}); got != "reg.example.fr/mstore/portal" {
		t.Errorf("reference = %q", got)
	}
	// Empty rather than truncated: better to show nothing than a name that names nothing.
	if got := project.Reference(Target{Stage: "web"}); got != "" {
		t.Errorf("reference with no image = %q", got)
	}
	if got := (Project{}).Reference(Target{Image: "portal"}); got != "" {
		t.Errorf("reference with no registry = %q", got)
	}
}

// A single-image Dockerfile goes through the same list, with an entry that has no
// target: that is the case which had no image name at all.
func TestASingleImageIsDeclaredWithNoTarget(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id, err := db.CreateProject(ctx, Project{
		Repository: "vcarroy/tool", Label: "Tool",
		ForgeURL: "https://forge.example.fr", Dockerfile: "Dockerfile",
		BuildContext: ".", ImageRegistry: "reg.example.fr/mstore",
		MultiStage: false,
		Targets:    []Target{{Stage: "", Image: "tool"}},
		CreatedBy:  "vcarroy",
	})
	if err != nil {
		t.Fatalf("declaration: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if len(read.Targets) != 1 || read.Targets[0].Image != "tool" {
		t.Fatalf("stages = %+v", read.Targets)
	}
	if got := read.Reference(read.Targets[0]); got != "reg.example.fr/mstore/tool" {
		t.Errorf("reference = %q", got)
	}
}

// Two repository shapes coexist, and neither is a special case of the other.
//
// The model could only say "this stage of the project's Dockerfile produces that image"
// — one repository's shape, taken for a general truth. Another carries
// `Dockerfile.dev` and `Dockerfile.prod` at its root: two files, two images, no stage.
func TestAStageMayNameItsOwnDockerfile(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	// The first shape: one Dockerfile, several stages.
	mstore := Project{
		Slug: "mstore", Repository: "vcarroy/mstore", Label: "Mstore",
		ForgeURL: "https://forge.example", Dockerfile: "Dockerfile", BuildContext: ".",
		MultiStage: true,
		Targets: []Target{
			{Stage: "web", Image: "portal"},
			{Stage: "cli", Image: "portal-cli"},
		},
	}
	if _, err := db.CreateProject(ctx, mstore); err != nil {
		t.Fatalf("project with stages: %v", err)
	}

	// The themes shape: one Dockerfile per image, no stage.
	theme := Project{
		Slug: "theme", Repository: "team/mstore-admin-theme", Label: "Theme",
		ForgeURL: "https://forge.example", BuildContext: ".", MultiStage: true,
		Targets: []Target{
			{Image: "mstore-admin-theme", Dockerfile: "Dockerfile.prod"},
			{Image: "mstore-admin-theme-dev", Dockerfile: "Dockerfile.dev"},
		},
	}
	id, err := db.CreateProject(ctx, theme)
	if err != nil {
		t.Fatalf("project with Dockerfiles: %v", err)
	}

	stored, err := db.GetProject(ctx, id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored.Buildable()) != 2 {
		t.Fatalf("%d buildable stage(s), expected 2: %+v", len(stored.Buildable()), stored.Targets)
	}
	// The most specific wins: the stage names its file, the project has none.
	if got := stored.DockerfileFor(stored.Targets[0]); got != "Dockerfile.prod" {
		t.Errorf("Dockerfile of the first stage = %q", got)
	}

	// And the converse: a silent stage falls back to the project's.
	if got := (Project{Dockerfile: "Dockerfile"}).DockerfileFor(Target{Stage: "web"}); got != "Dockerfile" {
		t.Errorf("fallback to the project's Dockerfile = %q", got)
	}
}

// A stage with no image name publishes nothing: it is discarded on write, and the
// database would refuse it anyway.
func TestAStageWithNoImageIsDiscarded(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id, err := db.CreateProject(ctx, Project{
		Slug: "empty", Repository: "x/empty", Label: "Vide",
		ForgeURL: "https://forge.example", Dockerfile: "Dockerfile", BuildContext: ".",
		Targets: []Target{{Stage: "web"}, {Stage: "cli", Image: "utile"}},
	})
	if err != nil {
		t.Fatalf("creation: %v", err)
	}

	stored, err := db.GetProject(ctx, id)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored.Targets) != 1 || stored.Targets[0].Image != "utile" {
		t.Errorf("stages kept = %+v", stored.Targets)
	}
}

// A project's id is the database's, unique, and names its Secrets: a slug renamed keeps the key
// recorded, and a deleted project leaves the names of its Secrets for the worker to delete.
func TestAProjectsSecretsFollowItsIDNotItsSlug(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id := project(t, db, "team/mstore")
	other := project(t, db, "team/theme")
	read, _ := db.GetProject(ctx, id)
	second, _ := db.GetProject(ctx, other)
	if len(read.UID) != 36 || read.UID == second.UID {
		t.Fatalf("ids %q and %q", read.UID, second.UID)
	}

	if err := db.SetDeployKeyError(ctx, id, "a Secret already carries this name"); err != nil {
		t.Fatal(err)
	}
	if read, _ := db.GetProject(ctx, id); read.DeployKeyError == "" {
		t.Error("the key's error was not kept")
	}
	secret := "arkhitekton-project-" + read.UID + "-git"
	if err := db.SetDeployKey(ctx, id, "ssh-ed25519 AAAA", secret); err != nil {
		t.Fatal(err)
	}
	present := true
	if _, err := db.RecordProjectProbe(ctx, id, ProjectProbe{OK: true, KeyPresent: &present}); err != nil {
		t.Fatal(err)
	}
	read, _ = db.GetProject(ctx, id)
	if read.DeployKeyError != "" {
		t.Errorf("error kept after the key was set: %q", read.DeployKeyError)
	}

	// Renamed: the key recorded stays the one mounted.
	read.Slug = "portail"
	if err := db.UpdateProject(ctx, read); err != nil {
		t.Fatalf("renaming: %v", err)
	}
	if name, found, err := db.DeployKeySecret(ctx, id); err != nil || !found || name != secret {
		t.Errorf("secret after renaming = %q (%v, %v)", name, found, err)
	}

	if err := db.DeleteProject(ctx, id); err != nil {
		t.Fatal(err)
	}
	names, err := db.SecretsToDelete(ctx)
	if err != nil || len(names) != 2 {
		t.Fatalf("to delete %v, %v", names, err)
	}
	for _, name := range names {
		if err := db.SecretDeleted(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	if names, _ := db.SecretsToDelete(ctx); len(names) != 0 {
		t.Errorf("still to delete %v", names)
	}
}

// A job that failed says why, on the release that launched it.
func TestAReleasesFailedJobSaysWhy(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	releaseID := releaseFor(t, db, "2.0.0")
	id, err := db.Enqueue(ctx, JobRequest{Kind: "docker/build", RequestedBy: "vcarroy", ReleaseID: &releaseID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishJob(ctx, id, JobFailed, "the project has no active deploy key"); err != nil {
		t.Fatal(err)
	}
	jobs, err := db.ListReleaseJobs(ctx, releaseID)
	if err != nil || len(jobs) != 1 || jobs[0].Error != "the project has no active deploy key" {
		t.Errorf("jobs = %+v, %v", jobs, err)
	}
}

// A check says whether the access or the key changed; the key's state stays when the question
// could not be asked, and so does where to paste it when the forge did not say.
func TestACheckSaysWhetherAnythingChanged(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()
	id := project(t, db, "team/app")
	if err := db.SetDeployKey(ctx, id, "ssh-ed25519 AAAA", "arkhitekton-project-x-git"); err != nil {
		t.Fatal(err)
	}

	present, absent := true, false
	steps := []struct {
		probe   ProjectProbe
		changed bool
		active  bool
	}{
		{ProjectProbe{OK: true, KeyPresent: &absent, KeyNote: "absent", KeysURL: "https://forge/team/app/settings/keys"}, true, false},
		{ProjectProbe{OK: true, KeyPresent: &absent, KeyNote: "absent"}, false, false},
		{ProjectProbe{OK: true, KeyPresent: &present, KeyNote: "present"}, true, true},
		{ProjectProbe{OK: true, KeyNote: "keys unreadable"}, false, true},
	}
	for index, step := range steps {
		changed, err := db.RecordProjectProbe(ctx, id, step.probe)
		if err != nil {
			t.Fatal(err)
		}
		read, _ := db.GetProject(ctx, id)
		if changed != step.changed || read.DeployKeyActive != step.active || read.KeyNote != step.probe.KeyNote ||
			read.KeysURL != "https://forge/team/app/settings/keys" {
			t.Errorf("step %d: changed %v, project %+v", index, changed, read)
		}
	}
	if _, err := db.RecordProjectProbe(ctx, 4242, ProjectProbe{}); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("a missing project: %v", err)
	}
}
