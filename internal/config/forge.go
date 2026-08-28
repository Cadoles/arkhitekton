package config

import (
	"fmt"
	"net/url"
	"strings"
)

/*
 * Dérivation de l'URL de clonage à partir de la configuration de la forge.
 *
 * Le dépôt de l'application est nommé à deux endroits qui doivent dire la même
 * chose : `FORGE_REPO`, que le dashboard interroge par l'API pour proposer les
 * branches, et l'URL de clonage que les Tasks reçoivent en `repo-url`. Les tenir
 * séparés laissait basculer l'un sans l'autre — on désignait alors un dépôt de
 * test dans le formulaire pendant que les pipelines continuaient de construire
 * l'application réelle, sans qu'aucun message ne le signale.
 *
 * L'URL est donc dérivée, et `FORGE_MSE_REPO_URL` ne subsiste que comme
 * dérogation explicite pour les cas que la dérivation ne couvre pas.
 */

// DefaultForgeSSHPort est le port SSH de la forge.
//
// 2222 et non 22 : Gitea y est exposé derrière un ingress qui réserve 22 à
// l'hôte. Le port n'est pas déductible de l'URL HTTPS, d'où la constante.
const DefaultForgeSSHPort = "2222"

// CloneURL rend l'URL SSH de clonage du dépôt applicatif.
//
// Construite depuis l'hôte de `FORGE_URL` et le chemin de `FORGE_REPO`, pour que
// désigner un dépôt dans la configuration suffise à ce que les pipelines le
// construisent. Rend une chaîne vide si la forge n'est pas configurée : l'appelant
// retombe alors sur sa valeur par défaut, ce qui préserve le fonctionnement d'un
// déploiement qui n'a jamais renseigné la forge.
func (c ForgeConfig) CloneURL(sshPort string) string {
	if !c.Configured() {
		return ""
	}
	if sshPort == "" {
		sshPort = DefaultForgeSSHPort
	}

	parsed, err := url.Parse(c.URL)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}

	repo := strings.TrimSuffix(strings.Trim(c.Repository, "/"), ".git")

	return fmt.Sprintf("ssh://git@%s:%s/%s.git", parsed.Hostname(), sshPort, repo)
}

// RepositoryFromCloneURL retrouve `owner/nom` et la forge depuis une URL de
// clonage.
//
// L'opération inverse de CloneURL, et elle sert au même souci de cohérence : les
// dépôts que les pipelines clonent sont écrits dans la configuration sous forme
// d'URL SSH, alors que l'API Gitea et l'écran des projets les nomment
// `owner/nom`. Les ressaisir à la main ferait diverger les deux formes au premier
// changement — on déclarerait un dépôt tout en clonant l'autre.
//
// Rend deux chaînes vides quand l'URL ne porte pas de dépôt : l'appelant écarte
// alors l'entrée plutôt que d'en déclarer une qui ne désigne rien.
func RepositoryFromCloneURL(clone string) (repository, forgeURL string) {
	clone = strings.TrimSpace(clone)
	if clone == "" {
		return "", ""
	}

	// Deux formes circulent : `ssh://git@hote:2222/owner/nom.git`, que la
	// configuration emploie, et `git@hote:owner/nom.git`, que Git accepte aussi.
	// La seconde n'est pas une URL au sens de `net/url` — son `:` sépare l'hôte
	// du chemin, pas un port — d'où le traitement séparé.
	if !strings.Contains(clone, "://") {
		_, rest, found := strings.Cut(clone, "@")
		if !found {
			return "", ""
		}
		host, path, found := strings.Cut(rest, ":")
		if !found {
			return "", ""
		}
		return normaliseRepository(path), "https://" + host
	}

	parsed, err := url.Parse(clone)
	if err != nil || parsed.Hostname() == "" {
		return "", ""
	}

	repository = normaliseRepository(parsed.Path)
	if repository == "" {
		return "", ""
	}
	return repository, "https://" + parsed.Hostname()
}

// normaliseRepository ramène un chemin de clonage à `owner/nom`.
//
// Un chemin plus profond est écarté plutôt que tronqué : `owner/groupe/nom` ne
// désigne pas un dépôt Gitea, et en fabriquer un donnerait un test d'accès qui
// échoue sans qu'on sache que la saisie était fautive.
func normaliseRepository(path string) string {
	trimmed := strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	owner, name, found := strings.Cut(trimmed, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return owner + "/" + name
}
