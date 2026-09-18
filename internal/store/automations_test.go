package store

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

/*
 * L'automatisation d'une release, et ses **trois** états.
 *
 * Pas de ligne, ligne au plan vide, ligne au plan garni. Le premier autorise la
 * reprise à composer depuis `targets` ; le deuxième dit que quelqu'un a décidé
 * que ce projet ne construit plus.
 *
 * Les confondre ferait réapparaître au redémarrage suivant une automatisation
 * qu'on vient d'effacer — et rien ne dirait pourquoi.
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

	sans, err := db.ProjectsWithoutAutomation(ctx)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(sans) != 1 || sans[0].ID != testProjectID {
		t.Fatalf("projects without an automation = %+v, expected the single project", sans)
	}
	// The project is returned in full: the catch-up needs its targets and its build context to
	// compose anything at all.
	if sans[0].BuildContext == "" || sans[0].Slug == "" {
		t.Errorf("project returned incomplete: %+v", sans[0])
	}

	// Un plan vide suffit à le retirer de la reprise : quelqu'un a décidé.
	if err := db.SaveReleaseAutomation(ctx,
		ReleaseAutomation{ProjectID: testProjectID, Plan: plan.Plan{}}, "vcarroy"); err != nil {
		t.Fatalf("%v", err)
	}

	sans, err = db.ProjectsWithoutAutomation(ctx)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(sans) != 0 {
		t.Errorf("%d project(s) still caught up — an emptied plan would reappear", len(sans))
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
		ReleaseAutomation{ProjectID: testProjectID, Plan: plan.Plan{}}, "autre"); err != nil {
		t.Fatalf("%v", err)
	}
	entry, _ = db.ReleaseAutomation(ctx, testProjectID)
	if len(entry.Plan.Stages) != 0 || entry.UpdatedBy != "autre" {
		t.Errorf("after replacement: %+v", entry)
	}
}
