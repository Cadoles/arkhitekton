package plan

import (
	"errors"
	"strings"
	"testing"
)

// pipeline compose un geste de type pipeline, forme la plus courante.
func pipeline(ref string, when When) Geste {
	return Geste{Kind: GestePipeline, Ref: ref, When: when}
}

// avance déroule un plan en marquant chaque geste partant du sort qu'on lui
// donne, jusqu'à ce qu'il n'y ait plus rien à décider.
//
// C'est le moteur, réduit à ce qu'il fait vraiment : demander, exécuter, écrire.
// L'écrire ici plutôt que de simuler à la main rend les tests lisibles et éprouve
// la boucle elle-même — qu'elle termine, notamment.
func avance(t *testing.T, progress Progress, sorts map[string]State) Progress {
	t.Helper()

	for tour := 0; ; tour++ {
		if tour > 100 {
			t.Fatal("la boucle d'avancement ne termine pas")
		}
		steps := progress.Next()
		if len(steps) == 0 {
			return progress
		}
		for _, step := range steps {
			state := Skipped
			if step.Run {
				state, _ = sorts[step.Geste.Ref]
				if state == "" {
					state = Succeeded
				}
			}
			var err error
			if progress, err = progress.Mark(step.Position, state); err != nil {
				t.Fatalf("marquage: %v", err)
			}
		}
	}
}

/*
 * La forme du modèle : des étapes, des gestes parallèles.
 */

// L'exemple qui a motivé le chantier, déroulé en entier.
//
// « Poser un statut, puis trois constructions en parallèle, puis quatre tests si
// les constructions ont réussi, puis poser le statut final. » S'il n'entre pas
// dans le modèle, le modèle est faux.
func TestLExempleDemandeSeDerouleEnQuatreEtapes(t *testing.T) {
	sujet := Plan{Stages: []Stage{
		{Gestes: []Geste{{Kind: GesteAction, Ref: "forge.status"}}},
		{Gestes: []Geste{
			pipeline("build-web", ""), pipeline("build-cli", ""), pipeline("build-worker", ""),
		}},
		{Gestes: []Geste{
			pipeline("unit-tests", ""), pipeline("static-tests", ""),
			pipeline("e2e-tests", ""), pipeline("security-audit", ""),
		}},
		{Gestes: []Geste{{Kind: GesteAction, Ref: "forge.status", When: WhenAlways}}},
	}}

	if err := sujet.Validate(); err != nil {
		t.Fatalf("plan refusé: %v", err)
	}

	progress := Start(sujet)

	// Première étape : un seul geste proposé, et rien d'autre.
	premiers := progress.Next()
	if len(premiers) != 1 || premiers[0].Geste.Ref != "forge.status" {
		t.Fatalf("première étape: %+v", premiers)
	}

	// L'étape suivante n'ouvre pas tant que la première n'est pas réglée.
	progress, _ = progress.Mark(premiers[0].Position, Running)
	if steps := progress.Next(); len(steps) != 0 {
		t.Errorf("une étape en cours a laissé passer la suivante: %+v", steps)
	}

	progress, _ = progress.Mark(premiers[0].Position, Succeeded)

	// Deuxième étape : les trois constructions **ensemble**.
	builds := progress.Next()
	if len(builds) != 3 {
		t.Fatalf("%d constructions proposées, attendu 3 en parallèle", len(builds))
	}
	for _, step := range builds {
		if !step.Run {
			t.Errorf("construction %q non lancée après une étape réussie", step.Geste.Ref)
		}
	}

	progress = avance(t, progress, nil)

	if !progress.Done() {
		t.Error("le plan n'est pas réglé")
	}
	if progress.Outcome() != Succeeded {
		t.Errorf("issue = %q", progress.Outcome())
	}
}

// La chaîne existante : une étape, N vérifications en parallèle. C'est la forme
// que la tranche doit reproduire à l'identique.
func TestLaChaineDeVerificationEstUnPlanDUneSeuleEtape(t *testing.T) {
	sujet := Plan{Stages: []Stage{{Gestes: []Geste{
		pipeline("unit-tests", ""), pipeline("static-tests", ""),
		pipeline("integration-tests", ""), pipeline("security-audit", ""),
	}}}}

	if err := sujet.Validate(); err != nil {
		t.Fatalf("plan refusé: %v", err)
	}

	steps := Start(sujet).Next()
	if len(steps) != 4 {
		t.Fatalf("%d vérifications proposées, attendu 4", len(steps))
	}
	for _, step := range steps {
		if step.Geste.Kind != GestePipeline || !step.Run {
			t.Errorf("geste %+v", step)
		}
	}
}

/*
 * Les conditions.
 */

// Un échec ferme la suite conditionnée au succès, ouvre celle conditionnée à
// l'échec, et laisse passer ce qui doit toujours partir.
func TestUnEchecOrienteLEtapeSuivante(t *testing.T) {
	sujet := Plan{Stages: []Stage{
		{Gestes: []Geste{pipeline("build", "")}},
		{Gestes: []Geste{
			pipeline("tests", WhenSuccess),
			{Kind: GesteAction, Ref: "prevenir", When: WhenFailure},
			{Kind: GesteAction, Ref: "statut", When: WhenAlways},
		}},
	}}

	progress := Start(sujet)
	premiers := progress.Next()
	progress, _ = progress.Mark(premiers[0].Position, Failed)

	suite := progress.Next()
	if len(suite) != 3 {
		t.Fatalf("%d gestes proposés", len(suite))
	}

	choix := map[string]bool{}
	for _, step := range suite {
		choix[step.Geste.Ref] = step.Run
	}
	if choix["tests"] {
		t.Error("les tests sont partis après une construction en échec")
	}
	if !choix["prevenir"] {
		t.Error("la réaction à l'échec n'est pas partie")
	}
	if !choix["statut"] {
		t.Error("le geste inconditionnel n'est pas parti")
	}
}

// Une étape entièrement sautée n'a pas échoué : elle n'a rien tenté.
//
// Sans cette distinction, un geste conditionné à l'échec partirait derrière une
// étape que rien n'a exécutée — et préviendrait d'une panne qui n'a pas eu lieu.
func TestUneEtapeSauteeNaPasEchoue(t *testing.T) {
	sujet := Plan{Stages: []Stage{
		{Gestes: []Geste{pipeline("build", "")}},
		{Gestes: []Geste{{Kind: GesteAction, Ref: "reparer", When: WhenFailure}}},
		{Gestes: []Geste{{Kind: GesteAction, Ref: "alerter", When: WhenFailure}}},
	}}

	progress := Start(sujet)
	premiers := progress.Next()
	// La construction réussit : l'étape 2 est donc sautée en entier.
	progress, _ = progress.Mark(premiers[0].Position, Succeeded)

	deuxieme := progress.Next()
	if len(deuxieme) != 1 || deuxieme[0].Run {
		t.Fatalf("deuxième étape: %+v", deuxieme)
	}
	progress, _ = progress.Mark(deuxieme[0].Position, Skipped)

	troisieme := progress.Next()
	if len(troisieme) != 1 {
		t.Fatalf("troisième étape: %+v", troisieme)
	}
	if troisieme[0].Run {
		t.Error("un geste d'échec est parti derrière une étape sautée, qui n'a rien raté")
	}
}

// L'issue d'un plan est l'échec dès qu'un geste a échoué, où qu'il soit.
//
// Un geste de conclusion en `always` qui réussit ne rachète pas l'échec qui l'a
// fait partir : le plan rapporterait « réussi » sur une chaîne dont la
// construction est tombée.
func TestUnGesteDeConclusionNeRachetePasUnEchec(t *testing.T) {
	sujet := Plan{Stages: []Stage{
		{Gestes: []Geste{pipeline("build", "")}},
		{Gestes: []Geste{{Kind: GesteAction, Ref: "statut", When: WhenAlways}}},
	}}

	progress := avance(t, Start(sujet), map[string]State{"build": Failed})

	if !progress.Done() {
		t.Fatal("plan non réglé")
	}
	if progress.Outcome() != Failed {
		t.Errorf("issue = %q, attendue %q", progress.Outcome(), Failed)
	}
}

/*
 * L'idempotence, qui est la propriété dont le moteur dépend.
 */

// Rappeler `Next` sans rien exécuter rend la même chose ; le rappeler après avoir
// marqué les gestes partants ne rend plus rien tant que l'étape n'est pas réglée.
func TestNextEstRappelableSansEffet(t *testing.T) {
	sujet := Plan{Stages: []Stage{{Gestes: []Geste{
		pipeline("unit-tests", ""), pipeline("static-tests", ""),
	}}}}

	progress := Start(sujet)
	premier, second := progress.Next(), progress.Next()
	if len(premier) != 2 || len(second) != 2 {
		t.Fatalf("appels successifs: %d puis %d gestes, attendu 2 à chaque fois",
			len(premier), len(second))
	}
	for index := range premier {
		if premier[index].Position != second[index].Position {
			t.Errorf("geste %d: positions %+v puis %+v — deux appels doivent rendre "+
				"la même chose", index, premier[index].Position, second[index].Position)
		}
	}

	for _, step := range progress.Next() {
		progress, _ = progress.Mark(step.Position, Running)
	}
	if steps := progress.Next(); len(steps) != 0 {
		t.Errorf("des gestes déjà partis sont reproposés: %+v", steps)
	}
}

// Marquer hors du plan est refusé plutôt que silencieusement absorbé : c'est le
// symptôme d'un moteur qui a perdu la correspondance entre un job et son geste,
// et l'absorber laisserait le plan bloqué sans rien dire.
func TestMarquerHorsDuPlanEstRefuse(t *testing.T) {
	progress := Start(Plan{Stages: []Stage{{Gestes: []Geste{pipeline("build", "")}}}})

	for _, position := range []Position{{Stage: 1}, {Stage: 0, Geste: 3}, {Stage: -1}} {
		if _, err := progress.Mark(position, Succeeded); !errors.Is(err, ErrInvalid) {
			t.Errorf("position %+v acceptée: %v", position, err)
		}
	}
}

// Marquer rend une **copie** : un appelant qui tient l'avancement précédent ne
// doit pas le voir changer sous lui.
func TestMarquerNeMutePasLAvancementPrecedent(t *testing.T) {
	avant := Start(Plan{Stages: []Stage{{Gestes: []Geste{pipeline("build", "")}}}})

	apres, err := avant.Mark(Position{}, Succeeded)
	if err != nil {
		t.Fatalf("marquage: %v", err)
	}
	if avant.States[0][0] != Pending {
		t.Error("l'avancement précédent a été muté")
	}
	if apres.States[0][0] != Succeeded {
		t.Error("le marquage n'a pas pris")
	}
}

/*
 * Ce qu'un plan doit déclarer pour être exécutable.
 */

// Un plan vide se reconnaît sans être invalide : « ce projet n'enchaîne rien »
// est une configuration légitime, et l'appelant doit pouvoir conclure tout de
// suite plutôt qu'attendre un geste qui ne viendra pas.
func TestUnPlanVideSeReconnaitSansEtreInvalide(t *testing.T) {
	if !(Plan{}).Empty() {
		t.Error("un plan sans étape n'est pas vu comme vide")
	}
	if !(Plan{Stages: []Stage{{}}}).Empty() {
		t.Error("une étape sans geste ne rend pas le plan vide")
	}
	if (Plan{Stages: []Stage{{Gestes: []Geste{pipeline("build", "")}}}}).Empty() {
		t.Error("un plan pourvu est vu comme vide")
	}
}

// Une étape sans geste est refusée : elle ne fait rien et décale les conditions
// des suivantes, qui liraient l'issue d'une étape qui n'a rien tenté.
func TestUneEtapeSansGesteEstRefusee(t *testing.T) {
	err := Plan{Stages: []Stage{
		{Gestes: []Geste{pipeline("build", "")}},
		{},
	}}.Validate()

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("validation: %v", err)
	}
	if !strings.Contains(err.Error(), "étape 2") {
		t.Errorf("message %q — il doit désigner l'étape fautive", err)
	}
}

// Un geste d'échec sur la première étape ne partirait jamais : rien ne la
// précède. Refusé plutôt que laissé en place — une déclaration qui ne peut pas se
// déclencher se lit comme une déclaration qui ne se déclenche pas, et on cherche
// alors la panne ailleurs.
func TestUnGesteDEchecSurLaPremiereEtapeEstRefuse(t *testing.T) {
	err := Plan{Stages: []Stage{
		{Gestes: []Geste{{Kind: GesteAction, Ref: "prevenir", When: WhenFailure}}},
	}}.Validate()

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("validation: %v", err)
	}
	if !strings.Contains(err.Error(), "jamais") {
		t.Errorf("message %q — il doit dire que le geste ne partirait jamais", err)
	}
}

// Les incohérences de déclaration sont refusées, chacune nommée.
func TestLesDeclarationsIncoherentesSontRefusees(t *testing.T) {
	cas := map[string]Plan{
		"aucune étape": {},
		"type inconnu": {Stages: []Stage{{Gestes: []Geste{
			{Kind: "webhook", Ref: "x"},
		}}}},
		"référence absente": {Stages: []Stage{{Gestes: []Geste{
			{Kind: GestePipeline, Ref: "  "},
		}}}},
		"condition inconnue": {Stages: []Stage{{Gestes: []Geste{
			{Kind: GestePipeline, Ref: "build", When: "peut-être"},
		}}}},
	}

	for nom, sujet := range cas {
		if err := sujet.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: accepté (%v)", nom, err)
		}
	}
}

// Un geste sans libellé se nomme par sa référence : l'écran doit toujours avoir
// quelque chose à afficher.
func TestUnGesteSansLibelleSeNommeParSaReference(t *testing.T) {
	if got := (Geste{Ref: "unit-tests"}).Name(); got != "unit-tests" {
		t.Errorf("nom = %q", got)
	}
	if got := (Geste{Ref: "unit-tests", Label: "Tests unitaires"}).Name(); got != "Tests unitaires" {
		t.Errorf("nom = %q", got)
	}
}

// La condition absente vaut « succès » : c'est l'enchaînement qu'on écrit sans y
// penser, et exiger de le déclarer alourdirait chaque plan.
func TestLaConditionAbsenteVautSucces(t *testing.T) {
	if got := (Geste{}).Condition(); got != WhenSuccess {
		t.Errorf("condition = %q", got)
	}
}

/*
 * Le rôle d'une étape.
 *
 * C'est l'ancrage du cycle de vie d'une release dans le plan (doc 15 §15.3).
 */

// Une étape sans rôle ne participe pas au cycle de vie.
//
// C'est le défaut, et c'est ce qui rend une étape de notification inoffensive :
// un webhook injoignable ne doit pas faire échouer une release.
func TestUneEtapeSansRoleNestPasUnAncrage(t *testing.T) {
	sujet := Plan{Stages: []Stage{
		{Role: RoleBuild, Gestes: []Geste{{Kind: GestePipeline, Ref: "build"}}},
		{Label: "Prévenir", Gestes: []Geste{{Kind: GesteAction, Ref: "webhook"}}},
	}}
	if err := sujet.Validate(); err != nil {
		t.Fatalf("%v", err)
	}

	if got := sujet.LastWithRole(RoleBuild); got != 0 {
		t.Errorf("étape de construction = %d, attendu 0", got)
	}
	// Aucune étape de test : le cycle de vie n'a pas d'ancrage pour cette
	// transition, et l'appelant doit pouvoir le constater plutôt que de viser
	// l'étape 0 par défaut.
	if got := sujet.LastWithRole(RoleTest); got != -1 {
		t.Errorf("étape de test = %d, attendu -1", got)
	}
}

// La **dernière** étape d'un rôle fait foi.
//
// Un plan peut construire en deux temps — une image de base, puis l'application
// au-dessus. C'est la conclusion du dernier temps qui dit que la construction est
// faite ; viser le premier avancerait la release trop tôt.
func TestLaDerniereEtapeDunRoleFaitFoi(t *testing.T) {
	sujet := Plan{Stages: []Stage{
		{Role: RoleBuild, Gestes: []Geste{{Kind: GestePipeline, Ref: "build-base"}}},
		{Role: RoleBuild, Gestes: []Geste{{Kind: GestePipeline, Ref: "build-app"}}},
		{Role: RoleTest, Gestes: []Geste{{Kind: GestePipeline, Ref: "unit-tests"}}},
	}}
	if err := sujet.Validate(); err != nil {
		t.Fatalf("%v", err)
	}

	if got := sujet.LastWithRole(RoleBuild); got != 1 {
		t.Errorf("étape de construction = %d, attendu la seconde", got)
	}
	if got := sujet.LastWithRole(RoleTest); got != 2 {
		t.Errorf("étape de test = %d", got)
	}
}

// Un rôle inventé est refusé à l'enregistrement.
//
// Ignoré, il donnerait une étape qui s'exécute sans jamais faire avancer la
// release — donc une release bloquée en construction sans que rien ne le dise.
func TestUnRoleInconnuEstRefuse(t *testing.T) {
	err := Plan{Stages: []Stage{
		{Role: "deploiement", Gestes: []Geste{{Kind: GestePipeline, Ref: "build"}}},
	}}.Validate()

	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("%v, attendu ErrInvalid", err)
	}
	// Le message nomme les rôles connus : les chercher dans le code source n'est
	// pas une réponse à donner à qui lit un message d'API.
	for _, attendu := range []string{"deploiement", "build", "test"} {
		if !strings.Contains(err.Error(), attendu) {
			t.Errorf("message %q — il doit porter %q", err, attendu)
		}
	}
}

/*
 * Le poids d'un geste (doc 15 §15.6).
 */

func poids(value float64) *float64 { return &value }

// Un poids négatif est refusé.
//
// Il ferait *baisser* le score quand le geste réussit, et le monter quand il
// échoue.
func TestUnPoidsNegatifEstRefuse(t *testing.T) {
	sujet := Plan{Stages: []Stage{{
		Role: RoleTest,
		Gestes: []Geste{{
			Kind: GestePipeline, Ref: "unit-tests", Weight: poids(-5),
		}},
	}}}

	err := sujet.Validate()
	if err == nil {
		t.Fatal("un poids négatif a été accepté")
	}
	if !strings.Contains(err.Error(), "négatif") {
		t.Errorf("motif %q — il doit nommer la faute", err)
	}
}

// Un poids posé hors d'une étape de vérification est refusé.
//
// Il serait enregistré, affiché, et sans effet : le score d'une release se
// calcule sur les gestes de son étape de vérification, la seule qui mesure
// quelque chose.
func TestUnPoidsHorsDuneEtapeDeVerificationEstRefuse(t *testing.T) {
	for _, role := range []Role{RoleBuild, ""} {
		sujet := Plan{Stages: []Stage{{
			Role: role,
			Gestes: []Geste{{
				Kind: GestePipeline, Ref: "build", Weight: poids(20),
			}},
		}}}

		err := sujet.Validate()
		if err == nil {
			t.Errorf("rôle %q: un poids a été accepté là où rien ne le lirait", role)
			continue
		}
		if !strings.Contains(err.Error(), string(RoleTest)) {
			t.Errorf("rôle %q: motif %q — il doit dire où un poids compte", role, err)
		}
	}
}

// Un poids nul est une décision, et se valide.
func TestUnPoidsNulSeValide(t *testing.T) {
	sujet := Plan{Stages: []Stage{{
		Role: RoleTest,
		Gestes: []Geste{{
			Kind: GestePipeline, Ref: "unit-tests", Weight: poids(0),
		}},
	}}}

	if err := sujet.Validate(); err != nil {
		t.Errorf("un poids nul a été refusé: %v", err)
	}
}
