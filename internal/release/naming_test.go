package release

import (
	"testing"

	versionpkg "forge.cadoles.com/vcarroy/arkhitekton/internal/version"
)

func TestNormalizeVersionName(t *testing.T) {
	cases := []struct {
		name, want, why string
	}{
		{"v3.2.0", "3.2.0", "convention courante"},
		{"V3.2.0", "3.2.0", "majuscule admise"},
		{"3.2.0", "3.2.0", "déjà normalisé"},
		{"  v3.2.0  ", "3.2.0", "espaces autour"},

		// `v` n'est retiré que devant un chiffre : sinon on amputerait un nom.
		{"validation", "validation", "v non suivi d'un chiffre"},
		{"vNext", "vNext", "v non suivi d'un chiffre"},
		{"v", "v", "préfixe seul"},
		{"", "", "vide"},

		// Plus aucun autre préfixe n'est retiré (doc 16 §16.3). Ces noms existent
		// dans le suivi du MSE, sur des versions closes : ils restent intacts, et
		// ne se rattachent donc à aucun tag.
		{"MSE 3.1.2", "MSE 3.1.2", "préfixe client, retiré de la règle"},
		{"MSE3-1.5", "MSE3-1.5", "convention historique"},
	}

	for _, c := range cases {
		if got := NormalizeVersionName(c.name); got != c.want {
			t.Errorf("%s: NormalizeVersionName(%q) = %q, attendu %q", c.why, c.name, got, c.want)
		}
	}
}

// La normalisation n'est pas injective : tout rapprochement doit traiter ce cas au
// lieu de supposer une correspondance unique.
func TestLaNormalisationNestPasInjective(t *testing.T) {
	if NormalizeVersionName("3.1.2") != NormalizeVersionName("v3.1.2") {
		t.Error("ces deux noms doivent se réduire au même numéro")
	}
}

// L'égalité stricte, telle que B8 et B17 la posaient, rejetait toute release réelle.
func TestUneReleaseSeRattacheAUneVersionPrefixee(t *testing.T) {
	version, err := versionpkg.SemVer{}.Parse("3.2.0-rc1")
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}

	if err := CheckTrackedVersion(version, "v3.2.0"); err != nil {
		t.Errorf("une RC 3.2.0-rc1 doit se rattacher à la version v3.2.0: %v", err)
	}
	// La normalisation assouplit la forme, pas le fond.
	if err := CheckTrackedVersion(version, "v3.3.0"); err == nil {
		t.Error("3.2.0-rc1 ne doit pas se rattacher à v3.3.0")
	}
}
