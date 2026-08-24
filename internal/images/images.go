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
)

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
