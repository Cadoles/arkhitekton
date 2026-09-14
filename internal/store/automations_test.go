package store

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
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

func planDeConstruction() plan.Plan {
	return plan.Plan{Stages: []plan.Stage{{
		Role: plan.RoleBuild, Label: "Construire",
		Gestes: []plan.Geste{{
			Kind: plan.GestePipeline, Ref: "build", Label: "portal",
			Params: map[string]string{"image-name": "portal", "target": "web"},
		}},
	}}}
}

// Un projet sans ligne se distingue nommément d'un projet au plan vide.
func TestUnProjetSansAutomatisationSeDistingueDUnPlanVide(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.ReleaseAutomation(ctx, testProjectID); !errors.Is(err, ErrNoAutomation) {
		t.Fatalf("lecture sans ligne: %v, attendu ErrNoAutomation", err)
	}

	// Vider l'automatisation **écrit** une ligne. C'est ce qui la distingue de
	// l'absence, et ce qui empêche la reprise de la recomposer.
	if err := db.SaveReleaseAutomation(ctx,
		ReleaseAutomation{ProjectID: testProjectID, Plan: plan.Plan{}}, "vcarroy"); err != nil {
		t.Fatalf("enregistrement du plan vide: %v", err)
	}

	entry, err := db.ReleaseAutomation(ctx, testProjectID)
	if err != nil {
		t.Fatalf("lecture après vidage: %v — l'absence et le vide se confondent", err)
	}
	if len(entry.Plan.Stages) != 0 {
		t.Errorf("plan = %+v, attendu vide", entry.Plan)
	}
}

// La reprise ne voit que les projets qui n'ont jamais rien eu.
func TestLaRepriseNeVoitQueLesProjetsSansLigne(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	sans, err := db.ProjectsWithoutAutomation(ctx)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(sans) != 1 || sans[0].ID != testProjectID {
		t.Fatalf("projets sans automatisation = %+v, attendu le seul projet", sans)
	}
	// Le projet est rendu **entier** : la reprise a besoin de ses cibles et de son
	// contexte de build pour composer quoi que ce soit.
	if sans[0].BuildContext == "" || sans[0].Slug == "" {
		t.Errorf("projet rendu incomplet: %+v", sans[0])
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
		t.Errorf("%d projet(s) encore repris — un plan vidé réapparaîtrait", len(sans))
	}
}

// Le plan traverse la base sans se déformer, et un second enregistrement remplace.
func TestLePlanTraverseEtLeSecondEnregistrementRemplace(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.SaveReleaseAutomation(ctx,
		ReleaseAutomation{ProjectID: testProjectID, Plan: planDeConstruction()},
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
	// Le rôle en particulier : c'est lui qui fait avancer l'état de la release, et
	// un rôle perdu à l'écriture laisserait une release éternellement en
	// construction (doc 15 §15.3).
	if stage.Role != plan.RoleBuild {
		t.Errorf("rôle = %q, attendu %q", stage.Role, plan.RoleBuild)
	}
	if stage.Gestes[0].Params["image-name"] != "portal" {
		t.Errorf("paramètres = %v", stage.Gestes[0].Params)
	}

	if err := db.SaveReleaseAutomation(ctx,
		ReleaseAutomation{ProjectID: testProjectID, Plan: plan.Plan{}}, "autre"); err != nil {
		t.Fatalf("%v", err)
	}
	entry, _ = db.ReleaseAutomation(ctx, testProjectID)
	if len(entry.Plan.Stages) != 0 || entry.UpdatedBy != "autre" {
		t.Errorf("après remplacement: %+v", entry)
	}
}
