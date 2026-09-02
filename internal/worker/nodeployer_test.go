package worker

import (
	"context"
	"errors"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Le projet qui n'a pas de déployeur.
 *
 * C'est l'épreuve que le lot D existe pour passer : « le premier Deployer
 * alternatif — même trivial, "aucun déployeur" — vaut plus que dix pages de
 * conception » (doc 12 §12.9). Un contrat écrit sur une seule réalisation en
 * épouse la forme sans qu'on s'en aperçoive ; celui-ci doit se laisser réaliser
 * par l'absence.
 *
 * Ce que l'absence a fait tomber, concrètement : la validation du catalogue
 * **exigeait** un `promotionSuffix` non vide sur chaque environnement. Un projet
 * sans outil de déploiement ne pouvait donc pas déclarer d'environnement du
 * tout — le catalogue imposait Flux à qui n'en avait pas.
 */

// Un catalogue sans le moindre réglage de déployeur doit être valide. Il ne
// l'était pas : `promotionSuffix` était requis, et son absence faisait échouer
// la validation avant même qu'un déployeur ait son mot à dire.
//
// Passé par `Parse` et non par la validation seule : c'est le chemin réel, et
// il inclut le décodage strict — celui qui refusera un catalogue resté sur
// l'ancien vocabulaire au lieu d'en perdre les suffixes en silence.
func TestUnCatalogueSansReglageDeDeployeurEstValide(t *testing.T) {
	const catalogue = `
maxConcurrentRuns: 1
environments:
  - id: recette
    label: Recette
    rank: 1
    namespace: app-recette
  - id: prod
    label: Production
    rank: 2
    namespace: app-prod
pipelines:
  - kind: build
    pipelineRef: build-image
    label: Construction
    produces: [image]
`

	parsed, err := catalog.Parse([]byte(catalogue))
	if err != nil {
		t.Fatalf("un projet sans déployeur doit pouvoir déclarer ses environnements: %v", err)
	}
	for _, environment := range parsed.Environments {
		if len(environment.Deployer) != 0 {
			t.Errorf("environnement %q: réglages inventés %v",
				environment.ID, environment.Deployer)
		}
	}
}

// L'observateur doit accepter ces environnements et n'affirmer rien de leur
// état. « Non observable » et non « inconnu » : la nuance est ce qui empêche une
// carte figée de ressembler à une panne.
func TestSansDeployeurLesEnvironnementsSontAcceptesEtMuets(t *testing.T) {
	declared := []catalog.Environment{
		{ID: "recette", Label: "Recette", Rank: 1, Namespace: "app-recette"},
	}

	environments := &fakeEnvironmentStore{}
	observer := NewEnvironmentObserver(
		environments, provider.NoDeployer{}, provider.NoAttester{}, quietLogger())

	// Check accepte : sans outil de déploiement, aucun environnement ne peut être
	// en désaccord avec lui.
	if err := observer.SyncEnvironments(context.Background(), declared); err != nil {
		t.Fatalf("aucun environnement ne peut être en désaccord avec l'absence: %v", err)
	}

	got := observer.observe(context.Background(),
		store.Environment{ID: "recette", Namespace: "app-recette", Observable: true})

	if got.Resolution != string(provider.ResolutionUnobservable) {
		t.Errorf("résolution %q, attendu %q — rien ne peut être affirmé",
			got.Resolution, provider.ResolutionUnobservable)
	}
	if got.DeclaredState != "" {
		t.Errorf("état déclaré %q — aucun outil ne l'a déclaré", got.DeclaredState)
	}
}

// La promotion refuse, et **nomme** ce qui manque. Un échec générique ferait
// chercher une panne là où il n'y a qu'une absence de configuration.
func TestSansDeployeurLaPromotionRefuseEnLeDisant(t *testing.T) {
	_, err := provider.NoDeployer{}.Plan(context.Background(), provider.Deployment{
		Target: provider.Target{ID: "recette"}, Version: "1.0.0", Artifact: "1.0.0", Name: "app",
	})

	if !errors.Is(err, provider.ErrNoDeployment) {
		t.Fatalf("erreur %v, attendu ErrNoDeployment — l'appelant doit pouvoir "+
			"distinguer l'absence d'outil d'une panne", err)
	}
}
