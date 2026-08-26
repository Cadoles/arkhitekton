package http

import (
	"context"
	"fmt"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * D'où vient le projet dont un tag fait foi.
 *
 * Depuis la migration 0025, une release et une version n'existent que **dans** un
 * projet : le tag `1.8.0` n'est unique qu'en son sein, et le lire sans dire lequel
 * rendrait la release d'un autre dès qu'un second existera.
 *
 * Cette résolution est **la couture** que le découpage par projet remplacera. Les
 * routes deviendront `/projects/{projet}/releases/{tag}`, et le projet viendra de
 * là plutôt que d'être déduit. En attendant, il est déduit d'une seule façon et en
 * un seul endroit, ce qui rend le remplacement mécanique — c'est tout ce qu'on lui
 * demande (doc 12 §12.9, lot B).
 *
 * Elle est écrite ici, et non dupliquée dans chaque handler, précisément pour que
 * `grep currentProject` rende la liste exhaustive de ce qui reste à brancher.
 */

// projectSource est ce que les handlers savent tous faire : nommer le projet qui
// se construit.
type projectSource interface {
	BuildableProject(ctx context.Context) (store.Project, error)
}

// currentProject rend le projet dont les tags font foi.
//
// Un seul aujourd'hui : celui qui se construit. Plusieurs candidats sont refusés
// par le magasin plutôt que départagés — construire au hasard produirait une
// release dont personne ne pourrait dire ce qu'elle contient.
func currentProject(ctx context.Context, source projectSource) (int64, error) {
	project, err := source.BuildableProject(ctx)
	if err != nil {
		return 0, fmt.Errorf("projet courant: %w", err)
	}
	return project.ID, nil
}
