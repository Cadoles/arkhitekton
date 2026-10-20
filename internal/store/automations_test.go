package store

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

/*
 * A release's automation, and its **three** states.
 *
 * No row, a row with an empty plan, a row with a filled plan. The first lets the catch-up compose
 * from `targets`; the second says someone decided this project no longer builds.
 *
 * Confusing them would make an automation one has just erased reappear at the next start-up —
 * and nothing would say why.
 */

func buildPlan() plan.Plan {
	return plan.Plan{Stages: []plan.Stage{{
		Role: plan.RoleBuild, Label: "Construire",
		Gestes: []plan.Geste{{
			Kind: plan.GestePipeline, Ref: "build", Label: "portal",
			Params: map[string]string{"image-name": "portal", "target": "web"},
		}},
	}}}
}

// A project with no row is told apart, by name, from a project with an empty plan.
func TestAProjectWithoutAnAutomationIsToldApartFromAnEmptyPlan(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.ReleaseAutomation(ctx, testProjectID); !errors.Is(err, ErrNoAutomation) {
		t.Fatalf("read with no row: %v, expected ErrNoAutomation", err)
	}

	// Emptying the automation writes a row. That is what distinguishes it from absence, and
	// what keeps the catch-up from recomposing it.
	if err := db.SaveReleaseAutomation(ctx,
		ReleaseAutomation{ProjectID: testProjectID, Plan: plan.Plan{}}, "vcarroy"); err != nil {
		t.Fatalf("recording the empty plan: %v", err)
	}

	entry, err := db.ReleaseAutomation(ctx, testProjectID)
	if err != nil {
		t.Fatalf("read after emptying: %v — absence and emptiness are conflated", err)
	}
	if len(entry.Plan.Stages) != 0 {
		t.Errorf("plan = %+v, expected empty", entry.Plan)
	}
}

// The catch-up sees only the projects that never had anything.
func TestTheCatchUpSeesOnlyProjectsWithNoRow(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	without, err := db.ProjectsWithoutAutomation(ctx)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(without) != 1 || without[0].ID != testProjectID {
		t.Fatalf("projects without an automation = %+v, expected the single project", without)
	}
	// The project is returned in full: the catch-up needs its targets and its build context to
	// compose anything at all.
	if without[0].BuildContext == "" || without[0].Slug == "" {
		t.Errorf("project returned incomplete: %+v", without[0])
	}

	// An empty plan is enough to take it out of the catch-up: someone decided.
	if err := db.SaveReleaseAutomation(ctx,
		ReleaseAutomation{ProjectID: testProjectID, Plan: plan.Plan{}}, "vcarroy"); err != nil {
		t.Fatalf("%v", err)
	}

	without, err = db.ProjectsWithoutAutomation(ctx)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(without) != 0 {
		t.Errorf("%d project(s) still caught up — an emptied plan would reappear", len(without))
	}
}

// The plan crosses the database without deforming, and a second save replaces.
func TestThePlanCrossesAndTheSecondSaveReplaces(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.SaveReleaseAutomation(ctx,
		ReleaseAutomation{ProjectID: testProjectID, Plan: buildPlan()},
		"vcarroy"); err != nil {
		t.Fatalf("%v", err)
	}

	entry, err := db.ReleaseAutomation(ctx, testProjectID)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(entry.Plan.Stages) != 1 {
		t.Fatalf("plan = %+v", entry.Plan)
	}
	stage := entry.Plan.Stages[0]
	// The role in particular: it is what advances the release's state, and a role lost on
	// write would leave a release building for ever.
	if stage.Role != plan.RoleBuild {
		t.Errorf("role = %q, expected %q", stage.Role, plan.RoleBuild)
	}
	if stage.Gestes[0].Params["image-name"] != "portal" {
		t.Errorf("parameters = %v", stage.Gestes[0].Params)
	}

	if err := db.SaveReleaseAutomation(ctx,
		ReleaseAutomation{ProjectID: testProjectID, Plan: plan.Plan{}}, "other"); err != nil {
		t.Fatalf("%v", err)
	}
	entry, _ = db.ReleaseAutomation(ctx, testProjectID)
	if len(entry.Plan.Stages) != 0 || entry.UpdatedBy != "other" {
		t.Errorf("after replacement: %+v", entry)
	}
}
