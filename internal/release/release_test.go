package release

import (
	versionpkg "forge.cadoles.com/vcarroy/mse-dashboard/internal/version"
	"strings"
	"testing"
)

// La forme et la nature sont désormais éprouvées dans `internal/version` et
// `cycle_test.go` : c'est là que vivent le schéma et le cycle. Les rejouer ici
// testerait deux fois la même règle depuis le paquet qui ne la possède plus.
func TestCheckRedmineVersionTiesPrereleaseToItsScope(t *testing.T) {
	rc, _ := versionpkg.SemVer{}.Parse("1.8.0-rc3")

	if err := DefaultNormalizer.CheckTrackedVersion(rc, "1.8.0"); err != nil {
		t.Errorf("une RC doit se rattacher à sa version de base: %v", err)
	}
	if err := DefaultNormalizer.CheckTrackedVersion(rc, "1.7.0"); err == nil {
		t.Error("un rattachement à la mauvaise version doit être refusé")
	}
}

func TestTransitions(t *testing.T) {
	allowed := [][2]State{
		{StateDraft, StateBuilding},
		{StateBuilding, StateReady},
		{StateBuilding, StateFailed},
		// Une reconstruction reste possible : un build peut échouer pour une
		// raison extérieure à la release.
		{StateFailed, StateBuilding},
		{StateReady, StateBuilding},
		// `draft → ready` est la porte du chemin **dérivé** : une release finale
		// est un re-tag, rien n'est construit ni exécuté, elle entre donc
		// directement en attente de décision (doc 04 §4.2, B17). Ce passage était
		// refusé tant que le code ne modélisait que le chemin construit.
		{StateDraft, StateReady},
		// `ready → failed` : le diagramme de doc 04 rend `failed` atteignable
		// depuis `building`, `verifying` **et** `ready` — une release en attente
		// de décision peut voir un pipeline obligatoire retomber en échec.
		{StateReady, StateFailed},
	}
	refused := [][2]State{
		// Rien ne revient à `draft` : cet état dit « pas encore construite », et y
		// retourner effacerait ce qui a été produit depuis.
		{StateReady, StateDraft},
		{StateBuilding, StateDraft},
		{StateReleased, StateDraft},
	}

	for _, pair := range allowed {
		if !CanTransition(pair[0], pair[1]) {
			t.Errorf("%s → %s devrait être permis", pair[0], pair[1])
		}
	}
	for _, pair := range refused {
		if CanTransition(pair[0], pair[1]) {
			t.Errorf("%s → %s devrait être refusé", pair[0], pair[1])
		}
	}
}

// Le re-tag : ce qui part en production est bit pour bit ce qui a été validé.
// L'image continue donc de porter la version de la RC, et c'est `image_version`
// qui absorbe l'écart — comparer PortalData au tag échouerait sur toute release
// finale.
func TestFinalFromSourceKeepsTheSourceImageVersion(t *testing.T) {
	imageVersion, warnings, err := FinalFromSource(Source{
		Tag: "1.8.0-rc3", ImageVersion: "1.8.0-rc3", State: StateReleased, TestsGreen: true,
	})
	if err != nil {
		t.Fatalf("re-tag refusé: %v", err)
	}
	if imageVersion != "1.8.0-rc3" {
		t.Errorf("version d'image = %q, attendu celle de la RC", imageVersion)
	}
	if len(warnings) != 0 {
		t.Errorf("avertissements inattendus: %v", warnings)
	}
}

// Livrer une RC dont un test est rouge appartient au client. Le taire serait en
// revanche une faute.
func TestFinalFromSourceWarnsOnRedTests(t *testing.T) {
	_, warnings, err := FinalFromSource(Source{
		Tag: "1.8.0-rc3", ImageVersion: "1.8.0-rc3", State: StateReleased, TestsGreen: false,
	})
	if err != nil {
		t.Fatalf("le re-tag ne doit pas être refusé: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "1.8.0-rc3") {
		t.Errorf("avertissement = %v", warnings)
	}
}

func TestFinalFromSourceRefusesUnusableSources(t *testing.T) {
	if _, _, err := FinalFromSource(Source{}); err == nil {
		t.Error("une release finale sans source doit être refusée")
	}

	if _, _, err := FinalFromSource(Source{
		Tag: "1.8.0-rc3", ImageVersion: "1.8.0-rc3", State: StateBuilding,
	}); err == nil {
		t.Error("une RC encore en construction ne doit pas être re-taguée")
	}

	if _, _, err := FinalFromSource(Source{
		Tag: "1.8.0-rc3", State: StateReleased,
	}); err == nil {
		t.Error("une RC sans version d'image ne doit pas être re-taguée")
	}
}

// La RC source doit être **validée**, pas seulement prête. C'est tout le sens du
// re-tag : il transporte la validation vers la release finale, et il n'y a rien à
// transporter tant que personne n'a validé.
func TestUneRCNonValideeNePeutPasEtreReTaguee(t *testing.T) {
	for _, state := range []State{StateDraft, StateBuilding, StateVerifying, StateReady, StateFailed} {
		_, _, err := FinalFromSource(Source{
			Tag: "1.8.0-rc3", ImageVersion: "1.8.0-rc3", State: state, TestsGreen: true,
		})
		if err == nil {
			t.Errorf("état %q: le re-tag devrait être refusé tant que la RC n'est pas validée", state)
		}
	}
}
