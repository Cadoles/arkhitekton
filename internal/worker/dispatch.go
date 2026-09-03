package worker

import (
	"context"
	"log/slog"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/reaction"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Le maillon entre un fait et les plans qu'il déclenche.
 *
 * Les tranches précédentes ont posé les faits, le moteur et les actions ; ce qui
 * manquait était l'aiguillage — trouver qui écoute, et ouvrir une exécution.
 *
 * # Appelé là où un fait est constaté, jamais par une horloge
 *
 * Comme le moteur (voir `engine.go`), et pour la même raison : les faits arrivent
 * par deux chemins connus — l'observateur et les sondes — qui savent tous deux
 * quand ils viennent d'en écrire un. Une boucle qui relirait le fil ajouterait un
 * curseur, une latence et une seconde source de vérité sur ce qui a été traité.
 *
 * # Ce qui n'est **pas** déclenché
 *
 * Un fait déjà connu. `RecordEvent` rend `false` sur une redélivrance, et
 * l'aiguillage n'est appelé que sur un fait neuf — première borne. L'unicité par
 * (déclencheur, fait) de `plan_runs` est la seconde : deux bornes pour la même
 * garantie, parce que celle-ci décide d'appels sortants.
 */

// ReactionStore est ce que l'aiguillage attend de la base.
//
// Interface étroite : il ne lit que les réactions actives d'un type, et lui
// donner le reste l'inviterait à décider de choses qui ne lui appartiennent pas.
type ReactionStore interface {
	ReactionsFor(ctx context.Context, projectID int64, eventType string) ([]reaction.Reaction, error)
}

// Dispatcher ouvre les exécutions que déclenche un fait.
type Dispatcher struct {
	store  ReactionStore
	engine *Engine
	logger *slog.Logger
}

func NewDispatcher(reactions ReactionStore, engine *Engine, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{store: reactions, engine: engine, logger: logger}
}

// Dispatch déclenche les réactions qui apparient ce fait.
//
// Sans effet quand le fait ne vise aucun projet : une réaction est déclarée par
// projet, et un fait qui n'en désigne aucun n'a personne pour l'écouter.
func (d *Dispatcher) Dispatch(ctx context.Context, fact event.Event) {
	if d == nil || d.engine == nil || fact.ProjectID == nil {
		return
	}

	reactions, err := d.store.ReactionsFor(ctx, *fact.ProjectID, string(fact.Type))
	if err != nil {
		d.logger.Error("réactions d'un fait",
			"type", fact.Type, "projet", *fact.ProjectID, "error", err)
		return
	}

	for _, declared := range reactions {
		if !declared.Matches(fact.Payload) {
			// Journalisé en `Debug` : c'est le cas courant — une réaction sur les
			// releases finales voit passer toutes les pré-releases — et le dire en
			// `Info` noierait le journal.
			d.logger.Debug("réaction non appariée",
				"reaction", declared.Name, "fait", fact.Type)
			continue
		}

		started := d.engine.Trigger(ctx, store.PlanRun{
			Trigger:   declared.Trigger(),
			EventKey:  fact.DedupKey,
			ProjectID: fact.ProjectID,
			Plan:      declared.Plan,
			// La charge du fait devient le contexte de l'exécution : c'est elle qui
			// lie les gabarits des gestes. Le plan **nomme**, l'exécution résout.
			Context: fact.Payload,
		})
		d.logger.Info("réaction déclenchée",
			"reaction", declared.Name, "fait", fact.Type,
			"sujet", fact.Subject, "gestes", started)
	}
}
