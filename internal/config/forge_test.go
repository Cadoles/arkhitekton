package config

import "testing"

// Désigner un dépôt dans la configuration doit suffire à ce que les pipelines le
// construisent. C'est la propriété qui manquait : `FORGE_REPO` basculait sur le
// dépôt de test pendant que les Tasks continuaient de cloner l'application réelle.
func TestCloneURLSuitLeDepotConfigure(t *testing.T) {
	cases := []struct {
		url, repo, port, want string
	}{
		{
			"https://forge.cadoles.com", "vcarroy/mse-test-project", "",
			"ssh://git@forge.cadoles.com:2222/vcarroy/mse-test-project.git",
		},
		{
			"https://forge.cadoles.com", "CNOUS/mse", "",
			"ssh://git@forge.cadoles.com:2222/CNOUS/mse.git",
		},
		// Le slash final et le suffixe `.git` sont tolérés : ils s'écrivent
		// naturellement en recopiant une URL de dépôt, et les refuser produirait
		// une URL à double `.git` que le clone rejetterait sans expliquer.
		{
			"https://forge.cadoles.com/", "vcarroy/mse-test-project.git", "",
			"ssh://git@forge.cadoles.com:2222/vcarroy/mse-test-project.git",
		},
		// Le port est configurable : une autre forge ne l'expose pas forcément
		// sur 2222.
		{
			"https://git.example.org", "equipe/appli", "22",
			"ssh://git@git.example.org:22/equipe/appli.git",
		},
	}

	for _, c := range cases {
		cfg := ForgeConfig{URL: c.url, Token: "x", Repository: c.repo}
		if got := cfg.CloneURL(c.port); got != c.want {
			t.Errorf("CloneURL(%q, %q, port=%q) = %q, attendu %q",
				c.url, c.repo, c.port, got, c.want)
		}
	}
}

// Sans forge configurée, la dérivation rend une chaîne vide plutôt qu'une URL
// bancale : l'appelant retombe sur sa valeur par défaut, et un déploiement qui
// n'a jamais renseigné la forge continue de fonctionner.
func TestSansForgeLaDerivationSabstient(t *testing.T) {
	for _, cfg := range []ForgeConfig{
		{},
		{URL: "https://forge.cadoles.com"},
		{Token: "x", Repository: "a/b"},
		{URL: "://cassé", Token: "x", Repository: "a/b"},
	} {
		if got := cfg.CloneURL(""); got != "" {
			t.Errorf("%+v: CloneURL rend %q, attendu vide", cfg, got)
		}
	}
}

// L'opération inverse : les dépôts que les pipelines clonent sont écrits en URL
// SSH, alors que l'API Gitea et l'écran des projets les nomment `owner/nom`.
func TestLeDepotSeRetrouveDepuisSonURLDeClonage(t *testing.T) {
	cases := []struct {
		clone      string
		repository string
		forge      string
	}{
		{"ssh://git@forge.cadoles.com:2222/CNOUS/mse.git", "CNOUS/mse", "https://forge.cadoles.com"},
		{"ssh://git@forge.cadoles.com:2222/vcarroy/mse-test-project.git",
			"vcarroy/mse-test-project", "https://forge.cadoles.com"},
		// La forme courte que Git accepte aussi : son `:` sépare l'hôte du
		// chemin, pas un port, et `net/url` s'y trompe.
		{"git@forge.cadoles.com:CNOUS/mse.git", "CNOUS/mse", "https://forge.cadoles.com"},
		{"https://forge.cadoles.com/CNOUS/mse", "CNOUS/mse", "https://forge.cadoles.com"},
		// Écartés plutôt que tronqués : en fabriquer un dépôt donnerait un test
		// d'accès qui échoue sans qu'on sache que la saisie était fautive.
		{"ssh://git@forge.cadoles.com:2222/mse.git", "", ""},
		{"ssh://git@forge.cadoles.com:2222/a/b/c.git", "", ""},
		{"", "", ""},
	}

	for _, tc := range cases {
		repository, forge := RepositoryFromCloneURL(tc.clone)
		if repository != tc.repository || forge != tc.forge {
			t.Errorf("%q → %q / %q, attendu %q / %q",
				tc.clone, repository, forge, tc.repository, tc.forge)
		}
	}
}

// Le dépôt d'un projet donne son URL de clonage : c'est de là que vient ce que
// ses pipelines clonent, et non d'une clé valable pour toute l'instance.
func TestCloneURLForSuitLeDepotDUnProjet(t *testing.T) {
	got := CloneURLFor("https://forge.example.fr", "vcarroy/mse-kustom", "2222")
	if got != "ssh://git@forge.example.fr:2222/vcarroy/mse-kustom.git" {
		t.Errorf("URL = %q", got)
	}

	// Le port SSH n'est pas déductible de l'URL HTTPS : sans lui, la constante.
	if got := CloneURLFor("https://forge.example.fr", "vcarroy/mse.git", ""); got !=
		"ssh://git@forge.example.fr:"+DefaultForgeSSHPort+"/vcarroy/mse.git" {
		t.Errorf("URL sans port = %q", got)
	}

	// Un projet déclaré avant que sa forge ne soit renseignée ne donne rien :
	// l'appelant retombe alors sur le dépôt de l'instance.
	for _, cas := range [][2]string{{"", "vcarroy/mse"}, {"https://forge.example.fr", "  "}} {
		if got := CloneURLFor(cas[0], cas[1], "2222"); got != "" {
			t.Errorf("forge %q dépôt %q: %q, attendu aucune URL", cas[0], cas[1], got)
		}
	}
}
