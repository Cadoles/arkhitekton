// Package images compose les références des images publiées.
//
// Rien de plus : les **noms** des dépôts publiés vivaient ici, en constantes —
// `portal`, `portal-cli`, `portal-dev` — accompagnées des listes de ce qu'une
// release construit et de ce qu'un re-tag propage. Ils décrivaient un dépôt
// donné, pas le produit, et les tenir en Go voulait dire recompiler le dashboard
// pour publier une image de plus.
//
// Ils sont désormais dans la table `projects`, qui décrit déjà ce que chaque
// dépôt construit : registre, étapes du Dockerfile, dépôts d'image alimentés, et
// lesquels sont promus. L'amorçage l'installe, l'administration la modifie
// (doc 12 §12.9). Ce qui reste ici est la seule chose qui n'est ni une donnée ni
// une décision : la façon d'écrire `registre/nom:tag`.
//
// Le préfixe du registre reste un paramètre : il change d'un déploiement à
// l'autre — `cnous-mse` en production, `vcarroy` en local.
package images

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
