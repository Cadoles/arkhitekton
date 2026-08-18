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
 * chose : `GITEA_REPO`, que le dashboard interroge par l'API pour proposer les
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
// Construite depuis l'hôte de `GITEA_URL` et le chemin de `GITEA_REPO`, pour que
// désigner un dépôt dans la configuration suffise à ce que les pipelines le
// construisent. Rend une chaîne vide si la forge n'est pas configurée : l'appelant
// retombe alors sur sa valeur par défaut, ce qui préserve le fonctionnement d'un
// déploiement qui n'a jamais renseigné la forge.
func (c GiteaConfig) CloneURL(sshPort string) string {
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
