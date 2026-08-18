package config

import "testing"

// Désigner un dépôt dans la configuration doit suffire à ce que les pipelines le
// construisent. C'est la propriété qui manquait : `GITEA_REPO` basculait sur le
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
		cfg := GiteaConfig{URL: c.url, Token: "x", Repository: c.repo}
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
	for _, cfg := range []GiteaConfig{
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
