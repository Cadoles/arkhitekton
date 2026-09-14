package worker

import (
	"context"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/catalog"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La porte de doc 15.
 *
 * La question est unique : **construire puis vérifier entre-t-il dans un plan
 * unique ?** Ces tests y répondent sur la forme du plan composé, avant qu'une
 * ligne d'éditeur ait été écrite.
 *
 * Ce qu'ils tiennent n'est pas la mise en file — le runner s'en charge — mais la
 * structure : deux étapes, leurs rôles, et la séparation entre ce que le geste
 * fixe et ce que le contexte lie.
 */

// catalogueDeTest rend un catalogue portant une construction et deux
// vérifications.
func catalogueDeTest(t *testing.T) *catalog.Catalog {
	t.Helper()

	const source = `
pipelines:
  - kind: build
    pipelineRef: build-image
    label: Construction
    produces: [image]
    # Les paramètres réels de la famille, et c'est ce que la tranche 4 exige : le
    # plan ne pose plus la révision ni le tag en clair, il lit ce que le catalogue
    # déclare vouloir. Un catalogue de test muet ferait passer cette lecture pour
    # correcte alors qu'elle ne rendrait rien.
    params:
      revision:   { source: input, required: true, suggest: git-ref }
      version:    { source: input, required: true, suggest: release-tag }
      tag-name:   { source: input, required: true, suggest: release-tag }
      image-name: { source: input, required: true, suggest: image-name, label: Dépôt publié }
      target:     { source: input, required: true, suggest: build-target }
      dockerfile: { source: input }
      context:    { source: input }
  - kind: security-audit
    pipelineRef: audit
    label: Audit de sécurité
    verification: true
    produces: [results]
    params:
      # Une **référence** d'image : l'analyse porte sur ce que la construction a
      # publié, et le geste peut viser l'artefact plutôt que la recomposer.
      portal-image: { source: input, required: true, suggest: image, image: portal }
    uploads:
      results:
        param: results-upload-url
        artifact: audit.json
        family: security
  - kind: unit-tests
    pipelineRef: unit-tests
    label: Tests unitaires
    verification: true
    produces: [results]
    params:
      revision: { source: input, required: true, suggest: git-ref }
    uploads:
      results:
        param: results-upload-url
        artifact: unit-tests.xml
        family: unit
        format: junit
  - kind: e2e-tests
    pipelineRef: e2e-tests
    label: Tests E2E
    verification: true
    produces: [results]
    params:
      revision: { source: input, required: true, suggest: git-ref }
    uploads:
      results:
        param: results-upload-url
        artifact: e2e-tests.xml
        family: e2e
        format: junit
`
	registry, err := catalog.Parse([]byte(source))
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	return registry
}

// pipelineDeTest rend une famille du catalogue de test.
func pipelineDeTest(t *testing.T, kind string) catalog.Pipeline {
	t.Helper()
	pipeline, found := catalogueDeTest(t).Find(kind)
	if !found {
		t.Fatalf("famille %q absente du catalogue de test", kind)
	}
	return pipeline
}

func projetDeuxImages() store.Project {
	return store.Project{
		ID: 1, Slug: "mse", Repository: "CNOUS/mse",
		Dockerfile: "Dockerfile", BuildContext: ".",
		MultiStage: true,
		Targets: []store.Target{
			{Stage: "web", Image: "portal"},
			{Stage: "cli", Image: "portal-cli"},
		},
	}
}

// La porte : les deux moitiés de la chaîne entrent dans un plan unique.
func TestConstruireEtVerifierEntrentDansUnPlanUnique(t *testing.T) {
	sujet := DefaultAutomation(catalogueDeTest(t), projetDeuxImages(), nil)

	if err := sujet.Validate(); err != nil {
		t.Fatalf("le plan composé doit être valide: %v", err)
	}
	if len(sujet.Stages) != 2 {
		t.Fatalf("%d étape(s), attendu construire puis vérifier", len(sujet.Stages))
	}

	// Les rôles sont l'ancrage du cycle de vie : sans eux, rien ne ferait avancer
	// la release une fois `release.verified` retiré.
	if sujet.Stages[0].Role != plan.RoleBuild {
		t.Errorf("rôle de la première étape = %q", sujet.Stages[0].Role)
	}
	if sujet.Stages[1].Role != plan.RoleTest {
		t.Errorf("rôle de la seconde étape = %q", sujet.Stages[1].Role)
	}
	if sujet.LastWithRole(plan.RoleBuild) != 0 || sujet.LastWithRole(plan.RoleTest) != 1 {
		t.Errorf("ancrages = %d et %d",
			sujet.LastWithRole(plan.RoleBuild), sujet.LastWithRole(plan.RoleTest))
	}

	// Un geste par image : les trois images de MSE cessent d'être une boucle dans
	// l'API pour devenir des gestes lisibles et relançables.
	if len(sujet.Stages[0].Gestes) != 2 {
		t.Fatalf("%d geste(s) de construction, attendu un par cible",
			len(sujet.Stages[0].Gestes))
	}
	libelles := []string{sujet.Stages[0].Gestes[0].Label, sujet.Stages[0].Gestes[1].Label}
	if libelles[0] != "portal" || libelles[1] != "portal-cli" {
		t.Errorf("libellés = %v — ils distinguent deux gestes de même référence", libelles)
	}

	// La vérification part **si la construction a réussi**. C'est cette condition
	// qui remplace le fait `release.built` : la même chose, sans aller-retour par
	// le fil des faits.
	for _, geste := range sujet.Stages[1].Gestes {
		if geste.Condition() != plan.WhenSuccess {
			t.Errorf("geste %q conditionné en %q, attendu %q",
				geste.Ref, geste.Condition(), plan.WhenSuccess)
		}
	}
}

// Le geste porte ce que la déclaration fixe, le contexte ce qui dépend du fait.
//
// C'est la séparation qui rend un plan réutilisable d'une release à l'autre : un
// plan qui figerait la révision ne servirait qu'une fois.
func TestLeGesteFixeLimageEtLeContexteLieLaRevision(t *testing.T) {
	sujet := DefaultAutomation(catalogueDeTest(t), projetDeuxImages(), nil)
	geste := sujet.Stages[0].Gestes[0]

	// Rien de ce qui dépend de la release n'est dans le geste.
	for _, interdit := range []string{"revision", "version", "tag-name"} {
		if _, present := geste.Params[interdit]; present {
			t.Errorf("le geste fige %q, qui dépend de la release", interdit)
		}
	}
	if geste.Params["image-name"] != "portal" || geste.Params["target"] != "web" {
		t.Errorf("paramètres du geste = %v", geste.Params)
	}

	contexte := releaseContext(
		store.Release{ID: 42, Tag: "3.2.0-rc8"}, "c1f8f8cd", "reg.example.fr/mse")
	params, err := gesteParams(pipelineDeTest(t, buildKind), geste, contexte)
	if err != nil {
		t.Fatalf("%v", err)
	}

	if params["revision"] != "c1f8f8cd" {
		t.Errorf("révision liée = %q", params["revision"])
	}
	if params["version"] != "3.2.0-rc8" || params["tag-name"] != "3.2.0-rc8" {
		t.Errorf("tag lié = %v", params)
	}
	// Et ce que le geste fixait est toujours là.
	if params["image-name"] != "portal" {
		t.Errorf("l'image du geste a été perdue: %v", params)
	}
}

// Un geste ne peut pas redéfinir la révision construite.
//
// Sans cette règle, deux gestes d'une même release pourraient viser deux commits
// et publier deux images sous le même tag — l'invariant de version tomberait sans
// le moindre message (B17).
func TestUnGesteNePeutPasRedefinirLaRevision(t *testing.T) {
	geste := plan.Geste{
		Kind: plan.GestePipeline, Ref: buildKind,
		Params: map[string]string{
			"image-name": "portal", "target": "web",
			"revision": "un-autre-commit",
		},
	}
	contexte := releaseContext(store.Release{ID: 1, Tag: "1.0.0"}, "le-bon-commit", "reg")

	params, err := gesteParams(pipelineDeTest(t, buildKind), geste, contexte)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := params["revision"]; got != "le-bon-commit" {
		t.Errorf("révision = %q — le contexte doit l'emporter sur le geste", got)
	}
}

// Ce que le projet ne vérifie pas ne s'enchaîne pas.
//
// Un projet sans campagne E2E voyait le pipeline s'enchaîner, échouer faute de
// scénario, et laisser sa release attendre un verdict qui ne viendrait jamais.
func TestUneFamilleNonActiveeNestPasEnchainee(t *testing.T) {
	sujet := DefaultAutomation(catalogueDeTest(t), projetDeuxImages(),
		func(kind string) bool { return kind != "e2e-tests" })

	if len(sujet.Stages) != 2 {
		t.Fatalf("%d étape(s)", len(sujet.Stages))
	}
	for _, geste := range sujet.Stages[1].Gestes {
		if geste.Ref == "e2e-tests" {
			t.Error("une famille non activée par le projet a été enchaînée")
		}
	}
	// Les deux autres familles du catalogue restent, et c'est ce qui prouve que le
	// filtre porte sur une famille et non sur l'étape entière.
	if len(sujet.Stages[1].Gestes) != 2 {
		t.Errorf("%d vérification(s), attendu les deux activées", len(sujet.Stages[1].Gestes))
	}
}

// Un projet qui ne construit rien et ne vérifie rien rend un plan vide.
//
// C'est une configuration légitime, distincte d'un plan invalide : l'appelant
// conclut tout de suite plutôt que d'attendre un verdict que personne ne rendra.
func TestUnProjetSansRienARendUnPlanVide(t *testing.T) {
	sujet := DefaultAutomation(catalogueDeTest(t),
		store.Project{ID: 2, Slug: "vide"},
		func(string) bool { return false })

	if !sujet.Empty() {
		t.Errorf("plan = %+v, attendu vide", sujet)
	}
}

// avancementsDeTest retient les transitions demandées.
type avancementsDeTest struct {
	demandes []string
	etat     string
}

func (a *avancementsDeTest) AdvanceReleaseState(
	_ context.Context, _ int64, from, to string,
) (bool, error) {
	a.demandes = append(a.demandes, from+"->"+to)
	if a.etat != from {
		// Conditionnée à l'état de départ, comme en base : une répétition
		// n'écrit rien.
		return false, nil
	}
	a.etat = to
	return true, nil
}

func executionDeRelease() store.PlanRun {
	id := int64(7)
	return store.PlanRun{ID: 1, ReleaseID: &id}
}

// Le règlement d'une étape avance la release, et remplace le décompte des builds.
func TestLeReglementDuneEtapeAvanceLaRelease(t *testing.T) {
	suivi := &avancementsDeTest{etat: string(release.StateBuilding)}
	cycle := NewReleaseLifecycle(suivi, quietLogger())
	ctx := context.Background()

	cycle.StageSettled(ctx, executionDeRelease(), plan.RoleBuild, plan.Succeeded)
	if suivi.etat != string(release.StateVerifying) {
		t.Fatalf("état = %q, attendu verifying", suivi.etat)
	}

	cycle.StageSettled(ctx, executionDeRelease(), plan.RoleTest, plan.Succeeded)
	if suivi.etat != string(release.StateReady) {
		t.Errorf("état = %q, attendu ready", suivi.etat)
	}
}

// La même étape annoncée dix fois n'avance la release qu'une.
//
// Le moteur ne tient aucune marque de « déjà annoncé » : il réannonce toutes les
// étapes réglées à chaque avancement. L'idempotence vient de l'écriture
// conditionnelle, et c'est elle qu'on éprouve ici.
func TestUneEtapeReannonceeNavancePasDeuxFois(t *testing.T) {
	suivi := &avancementsDeTest{etat: string(release.StateBuilding)}
	cycle := NewReleaseLifecycle(suivi, quietLogger())
	ctx := context.Background()

	for range 10 {
		cycle.StageSettled(ctx, executionDeRelease(), plan.RoleBuild, plan.Succeeded)
	}

	if suivi.etat != string(release.StateVerifying) {
		t.Errorf("état = %q", suivi.etat)
	}
	// Dix demandes, un seul avancement : c'est la base qui tranche, pas le moteur.
	if len(suivi.demandes) != 10 {
		t.Errorf("%d demandes, attendu 10", len(suivi.demandes))
	}
}

// Une étape sautée ne touche à rien.
//
// Le cas se produit quand la construction a échoué : les tests sont sautés, et la
// release est déjà `failed` par l'étape d'avant. La faire retomber ailleurs
// écraserait un état déjà juste.
func TestUneEtapeSauteeNeTouchePasALaRelease(t *testing.T) {
	suivi := &avancementsDeTest{etat: string(release.StateFailed)}
	cycle := NewReleaseLifecycle(suivi, quietLogger())

	cycle.StageSettled(context.Background(), executionDeRelease(),
		plan.RoleTest, plan.Skipped)

	if len(suivi.demandes) != 0 {
		t.Errorf("transitions demandées = %v, attendu aucune", suivi.demandes)
	}
}

// Un plan sans release ne touche à rien : une réaction déclarée n'a pas à
// déplacer un cycle de vie qui ne la concerne pas.
func TestUnPlanSansReleaseNavanceRien(t *testing.T) {
	suivi := &avancementsDeTest{etat: string(release.StateBuilding)}
	NewReleaseLifecycle(suivi, quietLogger()).StageSettled(
		context.Background(), store.PlanRun{ID: 2}, plan.RoleBuild, plan.Succeeded)

	if len(suivi.demandes) != 0 {
		t.Errorf("transitions demandées = %v", suivi.demandes)
	}
}

// Une étape de construction encore en cours n'avance pas la release.
//
// # La propriété que le décompte des builds tenait
//
// Une release construit plusieurs images. Tant que la dernière n'a pas conclu,
// elle reste en construction — l'observateur le tenait par `CountBuilds`, un
// décompte en base relu à chaque conclusion.
//
// L'étape le tient sans requête : elle n'est **réglée** que lorsque tous ses
// gestes le sont, et [plan.Progress.StageOutcome] ne rend rien avant.
func TestUneConstructionPartielleNAvancePasLaRelease(t *testing.T) {
	sujet := DefaultAutomation(catalogueDeTest(t), projetDeuxImages(), nil)
	progress := plan.Start(sujet)

	// La première image a conclu, la seconde tourne encore.
	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 0}, plan.Succeeded)
	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 1}, plan.Running)

	if _, settled := progress.StageOutcome(0); settled {
		t.Fatal("une étape dont un geste tourne encore ne doit pas être réglée")
	}

	// La seconde conclut : l'étape est réglée, et seulement alors.
	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 1}, plan.Succeeded)
	outcome, settled := progress.StageOutcome(0)
	if !settled || outcome != plan.Succeeded {
		t.Errorf("issue = %q, réglée = %v", outcome, settled)
	}
}

// Une image en échec conclut l'étape sans attendre les autres.
//
// Attendre ferait tourner des constructions dont on sait déjà que le résultat ne
// servira pas, et retarderait d'autant le moment où l'écran dit ce qui ne va pas.
func TestUneImageEnEchecFaitEchouerLetapeDesQuelleEstReglee(t *testing.T) {
	sujet := DefaultAutomation(catalogueDeTest(t), projetDeuxImages(), nil)
	progress := plan.Start(sujet)

	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 0}, plan.Failed)
	progress, _ = progress.Mark(plan.Position{Stage: 0, Geste: 1}, plan.Succeeded)

	outcome, settled := progress.StageOutcome(0)
	if !settled {
		t.Fatal("l'étape doit être réglée")
	}
	// Un échec l'emporte sur une réussite : l'image manquante manque, quelle que
	// soit la réussite des autres.
	if outcome != plan.Failed {
		t.Errorf("issue = %q, attendu échouée", outcome)
	}
}

/*
 * Le passage d'un artefact d'une étape à l'autre.
 *
 * La seconde porte de doc 15. La question : un geste de test peut-il viser ce que
 * la construction vient de publier, sans langage d'expressions ?
 */

// Un gabarit d'artefact est lié au contexte, comme les autres.
func TestUnGesteViseLartefactDunePrecedenteEtape(t *testing.T) {
	geste := plan.Geste{
		Kind: plan.GestePipeline, Ref: "security-audit",
		Params: map[string]string{"portal-image": "{artifacts.portal.image}"},
	}
	contexte := releaseContext(store.Release{ID: 1, Tag: "1.0.0"}, "abc", "reg")
	contexte["artifacts.portal.image"] = "reg.example.fr/portal:1.0.0"

	params, err := gesteParams(pipelineDeTest(t, "security-audit"), geste, contexte)
	if err != nil {
		t.Fatalf("%v", err)
	}
	// L'artefact l'emporte sur le repli : sans déclaration, l'exécution aurait
	// recomposé `reg/portal:1.0.0`, ce qui vaut même quand personne ne l'a publié.
	if params["portal-image"] != "reg.example.fr/portal:1.0.0" {
		t.Errorf("image = %q", params["portal-image"])
	}
}

// Un artefact qui n'a pas été produit fait échouer le geste, en le nommant.
//
// Parti avec son gabarit littéral, le pipeline clonerait, échouerait au `pull`,
// et le motif parlerait d'un dépôt introuvable — à mille lieues de la cause. Doc
// 15 §15.4 : un test qui ne tourne pas en silence se lit comme un test passé.
func TestUnArtefactAbsentFaitEchouerLeGesteEnLeNommant(t *testing.T) {
	geste := plan.Geste{
		Kind: plan.GestePipeline, Ref: "security-audit",
		Params: map[string]string{"portal-image": "{artifacts.portal.image}"},
	}
	contexte := releaseContext(store.Release{ID: 1, Tag: "1.0.0"}, "abc", "reg")

	_, err := gesteParams(pipelineDeTest(t, "security-audit"), geste, contexte)
	if err == nil {
		t.Fatal("un artefact absent doit faire échouer le geste")
	}
	if !strings.Contains(err.Error(), "artifacts.portal.image") {
		t.Errorf("motif %q — il doit nommer l'artefact manquant", err)
	}
}

// Les autres variables non résolues restent littérales, et c'est délibéré.
//
// Un `{tag}` visible dans un message se remarque et se corrige. Un artefact, lui,
// désigne quelque chose qui devait exister : son absence est une erreur de plan.
func TestUneVariableOrdinaireNonResolueNeFaitPasEchouer(t *testing.T) {
	geste := plan.Geste{
		Kind: plan.GestePipeline, Ref: buildKind,
		Params: map[string]string{
			"note": "construit depuis {branche}",
			// Les requis, sans quoi le refus porterait sur eux et non sur la note.
			"image-name": "portal", "target": "web",
		},
	}
	contexte := releaseContext(store.Release{ID: 1, Tag: "1.0.0"}, "abc", "reg")

	params, err := gesteParams(pipelineDeTest(t, buildKind), geste, contexte)
	if err != nil {
		t.Fatalf("%v — seule une référence d'artefact doit être exigée", err)
	}
	if params["note"] != "construit depuis {branche}" {
		t.Errorf("note = %q", params["note"])
	}
}
