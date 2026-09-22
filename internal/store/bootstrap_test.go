package store

import (
	"context"
	"testing"
)

/*
 * The seeding.
 *
 * What must be held: a step already applied is never replayed, even if the configuration has
 * changed. That is what makes the database sole master — a repository removed from the interface
 * must not come back at the next deployment, and a role taken from someone must not be granted
 * again.
 */

func spec() BootstrapSpec {
	return BootstrapSpec{
		Grants: []Grant{
			{Identifier: "vcarroy", Role: "admin"},
			{Identifier: "alice", Role: "releaser"},
		},
		Projects: []Project{
			{
				Repository: "CNOUS/mse", Label: "Application",
				ForgeURL: "https://forge.example.fr", BuildContext: ".",
				CreatedBy: "configuration",
			},
		},
	}
}

func TestTheBootstrapAppliesOnlyOnce(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	report, err := db.Bootstrap(ctx, spec())
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if report.GrantsApplied != 2 || report.ProjectsApplied != 1 {
		t.Fatalf("rapport = %+v", report)
	}
	if len(report.Skipped) != 0 {
		t.Errorf("steps skipped on the first pass: %v", report.Skipped)
	}

	// The database takes over: an administrator removes a repository and a declaration.
	projects, _ := db.ListProjects(ctx)
	if err := db.DeleteProject(ctx, projects[0].ID); err != nil {
		t.Fatalf("suppression: %v", err)
	}

	// Redeploying replays the bootstrap. Nothing must come back.
	report, err = db.Bootstrap(ctx, spec())
	if err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}
	if report.GrantsApplied != 0 || report.ProjectsApplied != 0 {
		t.Errorf("report = %+v: a step was replayed", report)
	}
	// Four steps: roles, repositories, attaching the trackers, and plugin settings. The last
	// two were added afterwards, precisely because a step already claimed is never replayed:
	// without them, an existing deployment would silently lose what the code had begun to read
	// elsewhere.
	if len(report.Skipped) != 4 {
		t.Errorf("steps skipped = %v, expected all four", report.Skipped)
	}

	remaining, _ := db.ListProjects(ctx)
	if len(remaining) != 0 {
		t.Errorf("%d repository(ies) resurrected by the bootstrap", len(remaining))
	}
}

// A declaration added to the configuration after the fact has no effect: that is the price
// of "the database prevails", and it beats an environment variable that grants a role
// indefinitely.
func TestADeclarationAddedAfterwardsIsNotPickedUp(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	if _, err := db.Bootstrap(ctx, spec()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	étendu := spec()
	étendu.Grants = append(étendu.Grants, Grant{Identifier: "bob", Role: "operator"})
	if _, err := db.Bootstrap(ctx, étendu); err != nil {
		t.Fatalf("second bootstrap: %v", err)
	}

	pending, err := db.PendingGrants(ctx)
	if err != nil {
		t.Fatalf("pending declarations: %v", err)
	}
	for _, grant := range pending {
		if grant.Identifier == "bob" {
			t.Error("a declaration added after the bootstrap was picked up")
		}
	}
	if len(pending) != 2 {
		t.Errorf("%d pending declaration(s), expected the initial 2", len(pending))
	}
}

// Pending declarations are what stays visible of a misspelled identifier: the user signs in
// with no permission, and nothing else says so.
func TestPendingDeclarationsAreListed(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	if _, err := db.Bootstrap(ctx, spec()); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	pending, err := db.PendingGrants(ctx)
	if err != nil {
		t.Fatalf("declarations: %v", err)
	}
	if len(pending) != 2 || pending[0].Identifier != "alice" || pending[0].Role != "releaser" {
		t.Errorf("declarations = %+v", pending)
	}
}
