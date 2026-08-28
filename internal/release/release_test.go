package release

import (
	"strings"
	"testing"
)

func TestParseVersion(t *testing.T) {
	cases := map[string]Version{
		"1.8.0":      {Raw: "1.8.0", Base: "1.8.0"},
		"1.8.0-rc1":  {Raw: "1.8.0-rc1", Base: "1.8.0", Prerelease: "rc1"},
		"3.1.0-rc.2": {Raw: "3.1.0-rc.2", Base: "3.1.0", Prerelease: "rc.2"},
		"10.0.12":    {Raw: "10.0.12", Base: "10.0.12"},
	}

	for tag, expected := range cases {
		got, err := ParseVersion(tag)
		if err != nil {
			t.Errorf("%s: %v", tag, err)
			continue
		}
		if got != expected {
			t.Errorf("%s: %+v, attendu %+v", tag, got, expected)
		}
	}
}

// Un tag hors forme casserait la jointure avec Redmine et la sélection Flux.
// Mieux vaut le dire à la création que le découvrir en constatant qu'un
// environnement ne bouge pas.
func TestParseVersionRejectsMalformedTags(t *testing.T) {
	invalid := []string{
		"", "1.8", "v1.8.0", "1.8.0.1", "1.8.0-", "latest",
		// L'ancien schéma mktools : il se parse comme du SemVer au sens large,
		// mais pas selon la forme retenue ici — et c'est voulu, une release du
		// dashboard ne doit jamais porter ce format.
		"2026.8.3-k8sunstable.1249.520c9a020+meta",
		// La métadonnée de build est exclue : le séparateur retenu est
		// l'underscore, `+` étant hors grammaire des tags OCI.
		"1.8.0+testing",
	}

	for _, tag := range invalid {
		if _, err := ParseVersion(tag); err == nil {
			t.Errorf("tag invalide accepté: %q", tag)
		}
	}
}

// L'erreur banale et coûteuse : créer `1.8.0-rc1` en déclarant une release
// finale, puis constater que la production ne la prend jamais.
func TestCheckKindCatchesMismatches(t *testing.T) {
	pre, _ := ParseVersion("1.8.0-rc1")
	final, _ := ParseVersion("1.8.0")

	if err := CheckKind(pre, KindPre); err != nil {
		t.Errorf("pré-release valide refusée: %v", err)
	}
	if err := CheckKind(final, KindFinal); err != nil {
		t.Errorf("release finale valide refusée: %v", err)
	}

	if err := CheckKind(pre, KindFinal); err == nil {
		t.Error("une pré-release déclarée finale doit être refusée")
	} else if !strings.Contains(err.Error(), "1.8.0") {
		t.Errorf("le message ne propose pas la forme correcte: %v", err)
	}

	if err := CheckKind(final, KindPre); err == nil {
		t.Error("une release finale déclarée pré-release doit être refusée")
	}
}

// `tag de release = nom de version Redmine [+ suffixe de pré-release]` : c'est ce
// qui relie une RC à son périmètre fonctionnel.
func TestCheckRedmineVersionTiesPrereleaseToItsScope(t *testing.T) {
	rc, _ := ParseVersion("1.8.0-rc3")

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
