package release

import (
	"errors"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

// Le cycle historique doit décrire exactement ce que l'énumération en base
// décrivait : un projet déclaré avant la bascule continue sans que personne
// n'ait rien à écrire.
func TestLeCycleParDefautReproduitLesTroisNatures(t *testing.T) {
	cycle := DefaultCycle()

	if err := cycle.Validate(version.SemVer{}); err != nil {
		t.Fatalf("le cycle par défaut doit être valide: %v", err)
	}

	// Le trait qui portait B17 : une finale re-tague, elle ne construit pas.
	final, err := cycle.Find("final")
	if err != nil {
		t.Fatalf("nature final: %v", err)
	}
	if final.Mode != ModeRetag || final.Source != "pre" {
		t.Errorf("final: %+v — B17 tient au mode, pas au nom", final)
	}

	// Et le défaut que la séparation révèle : `pre` et `patch` ne diffèrent que
	// par la forme du tag et la permission, leur comportement est le même.
	pre, _ := cycle.Find("pre")
	patch, _ := cycle.Find("patch")
	if pre.Mode != ModeBuild || patch.Mode != ModeBuild {
		t.Errorf("pre=%s patch=%s — les deux construisent", pre.Mode, patch.Mode)
	}
	if pre.Tag == patch.Tag && pre.Permission == patch.Permission {
		t.Error("pre et patch seraient alors indiscernables")
	}
}

// Une nature inconnue est nommée avec celles qui existent : chercher dans le
// code ce qu'on aurait pu écrire est une perte de temps évitable.
func TestUneNatureInconnueNommeCellesQuiExistent(t *testing.T) {
	_, err := DefaultCycle().Find("ga")
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("erreur %v, attendu ErrUnknownKind", err)
	}
	for _, name := range []string{"pre", "final", "patch"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("message %q — il doit nommer les natures déclarées", err)
		}
	}
}

// Le cycle qu'aucune des trois cases historiques ne pouvait décrire, et qui est
// tout l'objet de la bascule.
func TestUnCycleAlphaBetaGaEstDeclarable(t *testing.T) {
	cycle := Cycle{
		{ID: "alpha", Label: "Alpha", Mode: ModeBuild, Tag: TagPrereleaseRequired},
		{ID: "beta", Label: "Bêta", Mode: ModeBuild, Tag: TagPrereleaseRequired},
		{ID: "ga", Label: "Livraison", Mode: ModeRetag, Source: "beta",
			Tag: TagPrereleaseForbidden, Permission: "release:create:final"},
	}

	if err := cycle.Validate(version.SemVer{}); err != nil {
		t.Fatalf("cycle refusé: %v", err)
	}
}

// Un projet qui ne distingue rien — une seule sorte de release — devait
// auparavant en choisir une arbitrairement.
func TestUnCycleAUneSeuleNatureEstValide(t *testing.T) {
	cycle := Cycle{{ID: "release", Label: "Release", Mode: ModeBuild}}

	if err := cycle.Validate(version.SemVer{}); err != nil {
		t.Errorf("cycle à une nature refusé: %v", err)
	}
}

// Le couplage nature × schéma, refusé à la configuration.
//
// Sur un schéma opaque, `prerelease-required` n'est jamais vérifiable : une
// release créée en la croyant tenue partirait dans un environnement qui l'exclut,
// et rien ne le dirait.
func TestUneRegleDeTagSupposeUnSchemaQuiSaitRepondre(t *testing.T) {
	cycle := Cycle{{ID: "rc", Mode: ModeBuild, Tag: TagPrereleaseRequired}}

	err := cycle.Validate(version.Opaque{})
	if err == nil {
		t.Fatal("une règle que le schéma ne sait pas vérifier doit être refusée")
	}
	if !strings.Contains(err.Error(), "pré-release") {
		t.Errorf("message %q — il doit dire ce que le schéma ignore", err)
	}

	// Sans règle, le même schéma convient : un projet opaque reste utilisable.
	libre := Cycle{{ID: "release", Mode: ModeBuild}}
	if err := libre.Validate(version.Opaque{}); err != nil {
		t.Errorf("un cycle sans règle de tag doit convenir à un schéma opaque: %v", err)
	}
}

// Une source inexistante ne se découvrirait qu'au moment de livrer, c'est-à-dire
// au pire moment.
func TestUneSourceInexistanteEstRefusee(t *testing.T) {
	cycle := Cycle{
		{ID: "rc", Mode: ModeBuild},
		{ID: "ga", Mode: ModeRetag, Source: "beta"},
	}

	err := cycle.Validate(version.SemVer{})
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Errorf("erreur %v — la source manquante doit être nommée", err)
	}
}

// Une source déclarée **après** celle qui la cite reste valide : l'ordre de la
// liste est celui de l'affichage, pas une dépendance.
func TestUneSourceDeclareePlusBasResteValide(t *testing.T) {
	cycle := Cycle{
		{ID: "ga", Mode: ModeRetag, Source: "rc"},
		{ID: "rc", Mode: ModeBuild},
	}

	if err := cycle.Validate(version.SemVer{}); err != nil {
		t.Errorf("cycle refusé: %v", err)
	}
}

// Les incohérences structurelles.
func TestUnCycleIncoherentEstRefuse(t *testing.T) {
	cases := map[string]Cycle{
		"vide":           {},
		"sans id":        {{Mode: ModeBuild}},
		"mode inconnu":   {{ID: "rc", Mode: "deploy"}},
		"règle inconnue": {{ID: "rc", Mode: ModeBuild, Tag: "prerelease-maybe"}},
		"nature doublée": {{ID: "rc", Mode: ModeBuild}, {ID: "rc", Mode: ModeRetag}},
	}

	for name, cycle := range cases {
		if err := cycle.Validate(version.SemVer{}); err == nil {
			t.Errorf("%s: accepté", name)
		}
	}
}

// La vérification de forme, et son silence délibéré sur un schéma qui ne sait
// pas : la configuration a déjà interdit ce cas, et échouer ici sur un cycle
// valide bloquerait une création pour une raison incorrigible.
func TestLaVerificationDeFormeSeTaitQuandLeSchemaIgnore(t *testing.T) {
	spec := KindSpec{ID: "rc", Tag: TagPrereleaseRequired}

	opaque, _ := version.Opaque{}.Parse("build-4711")
	if err := CheckTag(spec, opaque); err != nil {
		t.Errorf("le schéma ne sait pas répondre: %v", err)
	}

	finale, _ := version.SemVer{}.Parse("1.8.0")
	if err := CheckTag(spec, finale); err == nil {
		t.Error("une nature exigeant une pré-release doit refuser une finale")
	}

	rc, _ := version.SemVer{}.Parse("1.8.0-rc1")
	if err := CheckTag(spec, rc); err != nil {
		t.Errorf("une RC doit être acceptée: %v", err)
	}
}

// Une nature vide n'est pas une nature mal orthographiée : c'est un appelant qui
// n'a rien choisi, et le message doit le dire plutôt que de citer des guillemets
// vides.
//
// Le cas s'est produit : le formulaire de création soumettait son état initial
// — la chaîne vide — plutôt que la nature qu'il affichait cochée. L'erreur
// désignait une valeur que l'utilisateur n'avait jamais vue.
func TestUneNatureVideEstDistingueeDUneNatureInconnue(t *testing.T) {
	_, err := DefaultCycle().Find("")
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("erreur %v, attendu ErrUnknownKind", err)
	}
	if !strings.Contains(err.Error(), "aucune nature") {
		t.Errorf("message %q — il doit dire que rien n'a été choisi", err)
	}
	// Et il nomme quand même ce qui existe : c'est ce qui permet de corriger.
	if !strings.Contains(err.Error(), "pre") {
		t.Errorf("message %q — il doit nommer les natures déclarées", err)
	}
}
