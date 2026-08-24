package store

import (
	"context"
	"errors"
	"fmt"
)

// SeedProjects déclare les dépôts qui n'existent pas encore.
//
// Le pont entre la configuration et la table. Les dépôts pilotés vivaient dans
// des variables d'environnement ; la table doit décrire ce que le dashboard fait
// **réellement**, pas ce qu'un administrateur aura pensé à y ressaisir. Les en
// déduire est donc la seule façon d'obtenir une liste juste dès le premier
// démarrage.
//
// Rien n'est écrasé : une déclaration existante l'emporte sur ce que la
// configuration suggère. C'est ce qui permet à un administrateur de corriger un
// libellé ou d'ajouter des étapes sans qu'un redémarrage ne les efface — et ce
// qui rend l'amorçage sûr à rejouer.
//
// Rend le nombre de dépôts effectivement déclarés, pour que l'appelant puisse le
// dire dans ses journaux plutôt que de laisser croire à une opération sans effet.
func (db *DB) SeedProjects(ctx context.Context, projects []Project) (int, error) {
	declared := 0
	for _, project := range projects {
		if project.Repository == "" {
			// Une URL de clonage dont on n'a pas su tirer `owner/nom`. L'écarter
			// vaut mieux que déclarer un dépôt qui ne désigne rien : le test
			// d'accès échouerait sans qu'on sache que la faute est en amont.
			continue
		}

		_, err := db.CreateProject(ctx, project)
		switch {
		case err == nil:
			declared++
		case errors.Is(err, ErrProjectExists):
			// Déjà déclaré : c'est le cas nominal après le premier démarrage.
		default:
			return declared, fmt.Errorf("amorçage du projet %q: %w", project.Repository, err)
		}
	}
	return declared, nil
}
