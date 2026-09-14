package worker

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La reprise, et la seule propriété qui compte : elle ne casse rien en passant.
 *
 * Sans elle, tous les projets cesseraient de construire au déploiement de cette
 * version. Ce qui vient d'arriver pour une autre raison, et qu'on ne va pas
 * refaire (doc 15 §15.8).
 */

// repriseStore retient ce que la reprise a écrit.
type repriseStore struct {
	sans    []store.Project
	ecrits  map[int64]plan.Plan
	acteurs map[int64]string
	eteints map[string]bool
	err     error
}

func nouveauRepriseStore(projects ...store.Project) *repriseStore {
	return &repriseStore{
		sans: projects, ecrits: map[int64]plan.Plan{},
		acteurs: map[int64]string{}, eteints: map[string]bool{},
	}
}

func (r *repriseStore) ProjectsWithoutAutomation(context.Context) ([]store.Project, error) {
	return r.sans, nil
}

func (r *repriseStore) SaveReleaseAutomation(
	_ context.Context, entry store.ReleaseAutomation, actor string,
) error {
	if r.err != nil {
		return r.err
	}
	r.ecrits[entry.ProjectID] = entry.Plan
	r.acteurs[entry.ProjectID] = actor
	return nil
}

func (r *repriseStore) PipelineEnabled(_ context.Context, _ int64, kind string) (bool, error) {
	return !r.eteints[kind], nil
}

func reprendre(t *testing.T, db *repriseStore) {
	t.Helper()
	if err := NewAutomationReprise(db, catalogueDeTest(t), quietLogger()).
		Run(context.Background()); err != nil {
		t.Fatalf("reprise: %v", err)
	}
}

// La reprise rend ce qui tournait la veille.
//
// C'est son contrat entier : le plan repris est celui que le code composait, par
// construction et non par relecture.
func TestLaRepriseRendCeQuiTournaitLaVeille(t *testing.T) {
	db := nouveauRepriseStore(projetDeuxImages())
	reprendre(t, db)

	repris, present := db.ecrits[1]
	if !present {
		t.Fatal("le projet n'a pas été repris")
	}
	attendu := DefaultAutomation(catalogueDeTest(t), projetDeuxImages(), nil)
	if len(repris.Stages) != len(attendu.Stages) {
		t.Fatalf("%d étape(s), attendu %d", len(repris.Stages), len(attendu.Stages))
	}
	if repris.Stages[0].Role != plan.RoleBuild || repris.Stages[1].Role != plan.RoleTest {
		t.Errorf("rôles = %q / %q", repris.Stages[0].Role, repris.Stages[1].Role)
	}
	if len(repris.Stages[0].Gestes) != 2 {
		t.Errorf("%d construction(s), attendu une par cible", len(repris.Stages[0].Gestes))
	}
	// L'auteur est lisible à l'écran : quelqu'un qui ouvre l'onglet doit pouvoir
	// dire « ceci n'a pas été saisi, ça vient de mes cibles ».
	if db.acteurs[1] != repriseActor {
		t.Errorf("auteur = %q", db.acteurs[1])
	}
}

// Un pipeline vise l'artefact d'une construction, quand le projet la fait.
//
// C'est ce qui relie les deux étapes autrement que par le hasard d'une référence
// recomposée : l'audit analyse l'image que la construction vient de publier.
func TestLaRepriseViseLartefactQuandLeProjetConstruitLimage(t *testing.T) {
	db := nouveauRepriseStore(projetDeuxImages())
	reprendre(t, db)

	audit := gesteNomme(t, db.ecrits[1], "security-audit")
	if audit.Params["portal-image"] != "{artifacts.portal.image}" {
		t.Errorf("paramètre = %q, attendu l'artefact de la construction",
			audit.Params["portal-image"])
	}
}

// Un projet qui ne construit pas cette image garde le repli, et continue de
// tourner.
//
// Viser un artefact que personne ne produit ferait échouer le geste en le
// nommant — ce qui est juste, mais une reprise ne doit rien casser en passant.
// Le repli recompose la référence depuis le registre, comme la veille.
func TestLaRepriseNeVisePasUnArtefactQueLeProjetNeConstruitPas(t *testing.T) {
	autre := projetDeuxImages()
	autre.Targets = []store.Target{{Stage: "app", Image: "autre-chose"}}

	db := nouveauRepriseStore(autre)
	reprendre(t, db)

	audit := gesteNomme(t, db.ecrits[1], "security-audit")
	if _, present := audit.Params["portal-image"]; present {
		t.Errorf("l'artefact %q est visé alors que le projet ne le construit pas",
			audit.Params["portal-image"])
	}
}

// Une famille éteinte sur le projet ne revient pas par la reprise.
func TestLaRepriseNeRallumePasUneFamilleEteinte(t *testing.T) {
	db := nouveauRepriseStore(projetDeuxImages())
	db.eteints["e2e-tests"] = true
	reprendre(t, db)

	for _, geste := range db.ecrits[1].Stages[1].Gestes {
		if geste.Ref == "e2e-tests" {
			t.Error("une famille éteinte a été reprise")
		}
	}
}

// Un projet qui ne compose rien reçoit tout de même sa ligne.
//
// Sans elle, la reprise le réexaminerait à chaque démarrage. Et la ligne est
// vraie : il ne construisait rien la veille non plus.
func TestUnProjetSansRienAComposerRecoitQuandMemeSaLigne(t *testing.T) {
	vide := store.Project{ID: 2, Slug: "vide"}
	db := nouveauRepriseStore(vide)
	db.eteints["security-audit"] = true
	db.eteints["unit-tests"] = true
	db.eteints["e2e-tests"] = true

	reprendre(t, db)

	repris, present := db.ecrits[2]
	if !present {
		t.Fatal("le projet n'a pas de ligne — il serait réexaminé à chaque démarrage")
	}
	if len(repris.Stages) != 0 {
		t.Errorf("plan = %+v, attendu vide", repris)
	}
}

// Un projet qu'on n'a pas su reprendre n'arrête pas les suivants.
//
// Il sera revu au démarrage suivant, puisqu'il n'a toujours pas de ligne.
// Interrompre la passe priverait tous les autres de leur automatisation.
func TestUnProjetNonReprisNarretePasLesSuivants(t *testing.T) {
	db := nouveauRepriseStore(projetDeuxImages(), store.Project{ID: 2, Slug: "second"})
	db.err = errors.New("écriture refusée")

	// L'erreur est consignée et non rendue : la reprise va au bout.
	if err := NewAutomationReprise(db, catalogueDeTest(t), quietLogger()).
		Run(context.Background()); err != nil {
		t.Fatalf("la reprise s'est interrompue: %v", err)
	}
}

// gesteNomme rend le geste d'un plan qui porte cette référence.
func gesteNomme(t *testing.T, sujet plan.Plan, ref string) plan.Geste {
	t.Helper()
	for _, stage := range sujet.Stages {
		for _, geste := range stage.Gestes {
			if geste.Ref == ref {
				return geste
			}
		}
	}
	t.Fatalf("aucun geste %q dans %+v", ref, sujet)
	return plan.Geste{}
}
