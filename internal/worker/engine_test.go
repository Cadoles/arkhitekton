package worker

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * L'ordre entre l'enregistrement d'un run et la conclusion de son geste.
 */

// magasinOrdonne retient l'ordre des écritures.
type magasinOrdonne struct {
	*fakePlanStore
	ordre     []string
	resultats map[int64]map[string]string
	produits  []store.PlanArtifact
}

func (m *magasinOrdonne) RunResults(_ context.Context, jobID int64) (map[string]string, error) {
	m.ordre = append(m.ordre, "lecture")
	return m.resultats[jobID], nil
}

func (m *magasinOrdonne) RecordPlanArtifacts(
	_ context.Context, _ int64, _ *int64, artifacts []store.PlanArtifact,
) error {
	m.ordre = append(m.ordre, "relevé")
	m.produits = append(m.produits, artifacts...)
	return nil
}

func (m *magasinOrdonne) PlanArtifacts(context.Context, int64) ([]store.PlanArtifact, error) {
	return m.produits, nil
}

// Les résultats d'un geste conclu deviennent ses artefacts.
//
// # Ce que ce test tient, et que rien ne tenait
//
// Le relevé **relit** les résultats du run par son job. Il faut donc qu'ils
// soient écrits avant que le geste ne soit conclu. Ils ne l'étaient pas :
// l'observation concluait le job *puis* enregistrait le run, et le relevé lisait
// une ligne vide. Trois constructions réussies, aucun artefact retenu — constaté
// sur le cluster, pas en test.
//
// Ce test tient la moitié qui dépend du moteur : donné un run dont les résultats
// sont lisibles, la conclusion du geste les retient.
func TestLesResultatsDunGesteConcluDeviennentSesArtefacts(t *testing.T) {
	base := &magasinOrdonne{
		fakePlanStore: newFakePlanStore(),
		resultats: map[int64]map[string]string{
			77: {"image": "reg.example.fr/portal:1.0.0", "tag": "1.0.0"},
		},
	}

	sujet := plan.Plan{Stages: []plan.Stage{{
		Role:   plan.RoleBuild,
		Gestes: []plan.Geste{{Kind: plan.GestePipeline, Ref: "build", Label: "portal"}},
	}}}

	engine := NewEngine(base, gesteRunnerFunc(
		func(context.Context, store.PlanRun, plan.Geste) (*int64, error) {
			id := int64(77)
			return &id, nil
		}), quietLogger()).
		WithArtifacts(base, func(string) string { return "image" })

	engine.Trigger(context.Background(), store.PlanRun{
		Trigger: "essai", EventKey: "essai:1", Plan: sujet,
	})
	engine.Advance(context.Background(), 77, plan.Succeeded)

	if len(base.produits) != 2 {
		t.Fatalf("%d artefact(s) retenus, attendu image et tag: %+v",
			len(base.produits), base.produits)
	}
	parNom := map[string]store.PlanArtifact{}
	for _, a := range base.produits {
		parNom[a.Name] = a
	}
	if parNom["image"].Reference != "reg.example.fr/portal:1.0.0" {
		t.Errorf("image = %q", parNom["image"].Reference)
	}
	// La nature vient du `produces` du catalogue, pas du nom du résultat.
	if parNom["image"].Kind != "image" {
		t.Errorf("nature = %q", parNom["image"].Kind)
	}
}
