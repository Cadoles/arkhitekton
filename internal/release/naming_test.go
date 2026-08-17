package release

import "testing"

// Les conventions réellement présentes dans le projet, et ce qu'on en fait.
func TestNormalizeVersionName(t *testing.T) {
	cases := []struct {
		name, want, why string
	}{
		{"v3.2.0", "3.2.0", "convention courante"},
		{"V3.2.0", "3.2.0", "majuscule admise"},
		{"MSE 3.1.2", "3.1.2", "convention à espace"},
		{"mse 3.1.2", "3.1.2", "casse indifférente"},
		{"3.2.0", "3.2.0", "déjà normalisé"},
		{"  v3.2.0  ", "3.2.0", "espaces autour"},
		{"MSE 3.1.2 ", "3.1.2", "espace final"},

		// Laissé intact : « MSE3, version 1.5 » se lirait 3.1.5 ou 1.5 selon
		// l'intention, et rien dans les données ne tranche.
		{"MSE3-1.5", "MSE3-1.5", "convention historique, non devinée"},
		{"MSE3-1.21", "MSE3-1.21", "convention historique, non devinée"},

		// `v` n'est retiré que devant un chiffre : sinon on amputerait un nom.
		{"validation", "validation", "v non suivi d'un chiffre"},
		{"vNext", "vNext", "v non suivi d'un chiffre"},
		{"", "", "vide"},
	}

	for _, c := range cases {
		if got := NormalizeVersionName(c.name); got != c.want {
			t.Errorf("%s: NormalizeVersionName(%q) = %q, attendu %q", c.why, c.name, got, c.want)
		}
	}
}

// La normalisation n'est pas injective, et c'est un fait des données : `MSE 3.1.2`
// et `v3.1.2` coexistent dans le projet. Tout rapprochement doit traiter ce cas au
// lieu de supposer une correspondance unique.
func TestLaNormalisationNestPasInjective(t *testing.T) {
	if NormalizeVersionName("MSE 3.1.2") != NormalizeVersionName("v3.1.2") {
		t.Error("ces deux noms réels doivent bien se réduire au même numéro")
	}
}

// L'égalité stricte, telle que B8 et B17 la posaient, rejetait toute release réelle.
func TestUneReleaseSeRattacheAUneVersionPrefixee(t *testing.T) {
	version, err := ParseVersion("3.2.0-rc1")
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}

	if err := CheckRedmineVersion(version, "v3.2.0"); err != nil {
		t.Errorf("une RC 3.2.0-rc1 doit se rattacher à la version Redmine v3.2.0: %v", err)
	}
	if err := CheckRedmineVersion(version, "MSE 3.2.0"); err != nil {
		t.Errorf("et à MSE 3.2.0: %v", err)
	}

	// Une version réellement différente reste refusée : la normalisation assouplit
	// la forme, pas le fond.
	if err := CheckRedmineVersion(version, "v3.3.0"); err == nil {
		t.Error("3.2.0-rc1 ne doit pas se rattacher à v3.3.0")
	}
}
