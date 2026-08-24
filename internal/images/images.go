// Package images nomme les images qu'une release publie.
//
// Ces noms étaient écrits en clair à trois endroits — l'enchaînement automatique
// des vérifications, la relance manuelle depuis l'écran d'une release, et les
// suggestions du formulaire de lancement. La migration de `mse` vers FrankenPHP
// les a renommées, et il a fallu les retrouver un par un : c'est exactement le
// genre de fait qui doit vivre en un seul point.
//
// Un paquet plutôt que des constantes dans l'un des trois : `internal/http` et
// `internal/worker` en ont tous deux besoin, et faire dépendre l'un de l'autre
// pour deux chaînes de caractères créerait un lien qui n'existe pas.
//
// Le préfixe du registre, lui, reste un paramètre : il change d'un déploiement à
// l'autre — `cnous-mse` en production, `vcarroy` en local — alors que les noms
// des images, eux, sont ceux que `mse-kustom` déploie et que `build-image`
// publie. Les rendre configurables sans que le catalogue ni les pipelines ne le
// soient donnerait trois vérités possibles pour une seule.
package images

// Noms des dépôts publiés par la construction d'une release.
//
// Ils suivent les cibles du Dockerfile de `mse` : la cible `web` alimente
// `portal`, la cible `cli` alimente `portal-cli`.
const (
	// Web est le runtime HTTP — front office, back office, web services.
	Web = "portal"
	// CLI sert les consumers Messenger, les cronjobs et les jobs. Nommée
	// `portal-consumer` jusqu'à la migration FrankenPHP, où elle a cessé de ne
	// servir que les consumers.
	CLI = "portal-cli"
	// Dev est l'image « grasse » : xdebug, xhprof, client PostgreSQL. Elle fait
	// tourner le job de bootstrap des overlays à fausses données, et permet de
	// reproduire un défaut sur exactement la version livrée.
	Dev = "portal-dev"
)

// Buildable énumère ce qu'une release construit.
//
// La liste est ici parce que le dashboard doit la parcourir : le pipeline de
// construction ne traite plus qu'une image par exécution, et c'est l'appelant qui
// décide combien il en lance. Elle vit à côté des noms plutôt que dans le
// catalogue parce qu'elle est indissociable des cibles du Dockerfile de `mse` —
// une image sans cible n'est pas constructible, et le catalogue ne saurait pas le
// dire.
var Buildable = []Build{
	{Name: Web, Target: "web"},
	{Name: CLI, Target: "cli"},
	{Name: Dev, Target: "dev"},
}

// Build associe un dépôt publié à la cible du Dockerfile qui l'alimente.
//
// Deux champs et non un : la correspondance appartient au dashboard, et une
// cible renommée côté `mse` ne doit pas obliger à renommer un dépôt du registre.
type Build struct {
	Name   string
	Target string
}

// Promotable énumère ce qu'un re-tag propage.
//
// `portal-dev` en est absente, et c'est la seule différence avec Buildable :
// aucun environnement du flux de promotion ne la déploie, et la re-taguer en
// préproduction reviendrait à annoncer comme livrable une image qui ne l'est pas.
var Promotable = []string{Web, CLI}

// Repository compose le dépôt complet, sans tag.
//
// Vide quand le projet l'est : mieux vaut ne rien afficher qu'un nom tronqué qui
// ressemblerait à une référence tirable sans l'être.
func Repository(project, name string) string {
	if project == "" {
		return ""
	}
	return project + "/" + name
}

// Reference compose la référence complète d'une image publiée.
func Reference(project, name, tag string) string {
	repository := Repository(project, name)
	if repository == "" || tag == "" {
		return ""
	}
	return repository + ":" + tag
}
