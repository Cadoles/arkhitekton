package worker

import (
	"context"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
)

/*
 * La pose des bibliothèques de référence.
 *
 * Même forme que la boucle des clés de déploiement, et pour les mêmes raisons :
 * l'API déclare, le worker réalise sous le bail, et un redémarrage rattrape ce
 * qu'une panne a interrompu (doc 12 §12.7).
 *
 * Elle sert l'**autonomie** : une instance neuve, sans dépôt de pipelines
 * préexistant, reçoit de quoi construire et tester les piles que ses projets
 * déclarent. Sur une instance qui a déjà les siens, la règle de non-adoption
 * fait que le dashboard constate et se retire.
 */

// libraryInterval espace deux passages.
//
// Long : l'état ne change que lorsqu'un projet déclare une pile — un geste rare —
// ou lorsque le binaire change de version, ce qui passe par un redémarrage.
const libraryInterval = 10 * time.Minute

// StackStore rend les piles à pourvoir.
type StackStore interface {
	Stacks(ctx context.Context) ([]string, error)
}

// LibraryInstaller pose la bibliothèque d'une pile.
type LibraryInstaller interface {
	Install(ctx context.Context, stack string) (pipelines.Outcome, error)
}

// LibraryReconciler pose les bibliothèques que les projets réclament.
type LibraryReconciler struct {
	store     StackStore
	installer LibraryInstaller
	logger    *slog.Logger
}

func NewLibraryReconciler(
	projects StackStore, installer LibraryInstaller, logger *slog.Logger,
) *LibraryReconciler {
	return &LibraryReconciler{store: projects, installer: installer, logger: logger}
}

// Run fait tourner la boucle jusqu'à annulation.
func (r *LibraryReconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(libraryInterval)
	defer ticker.Stop()

	for {
		r.once(ctx)

		select {
		case <-ctx.Done():
			r.logger.Info("arrêt de la pose des bibliothèques de pipelines")
			return nil
		case <-ticker.C:
		}
	}
}

func (r *LibraryReconciler) once(ctx context.Context) {
	stacks, err := r.store.Stacks(ctx)
	if err != nil {
		r.logger.Error("piles déclarées", "error", err)
		return
	}

	for _, stack := range stacks {
		outcome, err := r.installer.Install(ctx, stack)
		if err != nil {
			// Une pile en échec n'empêche pas les autres : un projet dont la
			// bibliothèque manque le verra à son premier lancement, alors qu'une
			// boucle arrêtée priverait tous les suivants.
			r.logger.Error("pose de la bibliothèque", "pile", stack, "error", err)
		}

		// Journalisé seulement quand quelque chose bouge : la boucle repasse
		// toutes les dix minutes, et un compte-rendu identique à chaque tour
		// noierait ce qui compte.
		if len(outcome.Installed) > 0 || len(outcome.Updated) > 0 {
			r.logger.Info("bibliothèque de pipelines posée",
				"pile", stack, "posées", outcome.Installed, "mises_a_jour", outcome.Updated)
		}
		if len(outcome.Skipped) > 0 {
			// Pas une erreur : le refus d'adopter. Le dire évite qu'on cherche
			// pourquoi une définition du dashboard n'apparaît pas.
			r.logger.Info("définitions laissées à leur propriétaire",
				"pile", stack, "ressources", outcome.Skipped,
				"raison", "elles existent déjà et n'ont pas été créées par le dashboard")
		}
	}
}
