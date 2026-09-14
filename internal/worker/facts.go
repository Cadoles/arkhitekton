package worker

import (
	"context"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * L'émission des faits, côté observation.
 *
 * Personne ne les écoute encore. C'est le sujet de cette tranche : poser les
 * points d'émission et éprouver qu'ils tiennent à la redélivrance, pendant que
 * l'erreur ne coûte rien (doc 14).
 *
 * # Où ils sont posés, et pourquoi là
 *
 * Chaque fait est émis **après** l'écriture qui l'établit, jamais avant. Le
 * mécanisme d'idempotence de l'observateur est une écriture conditionnelle — une
 * seule livraison la gagne — et s'y adosser fait que le fait hérite de sa
 * garantie. La clé de déduplication reste une seconde ceinture, pour les chemins
 * qui ne passent pas par une transition d'état.
 *
 * # L'échec n'interrompt rien
 *
 * Un fait qu'on n'a pas su écrire est journalisé et le traitement continue. C'est
 * la même règle que pour les ingestions, et elle a ici un filet : l'observateur
 * redélivre les runs terminés, si bien qu'un fait perdu au redémarrage sera
 * réémis au passage suivant.
 *
 * Ce filet n'existe **pas** pour les faits émis par l'`api` — une release
 * déclarée, une release livrée. Un fait perdu l'est définitivement. C'est
 * acceptable tant que rien ne réagit ; ce ne le sera plus, et c'est un écart à
 * traiter quand le moteur arrivera.
 */

// EventRecorder enregistre un fait, ou constate qu'il est déjà connu.
//
// Interface étroite plutôt que le magasin entier : l'émission n'a besoin que de
// cette réponse, et donner accès au reste inviterait à y lire autre chose.
type EventRecorder interface {
	// RecordEvent rend `false` quand le fait existait déjà — le cas nominal d'une
	// redélivrance, et non une erreur.
	RecordEvent(ctx context.Context, fact event.Event) (bool, error)
}

// WithEvents branche l'émission des faits.
//
// Optionnelle comme les ingestions : sans elle, l'observation continue, et le fil
// des faits reste vide. Personne ne réagit encore, donc rien n'est perdu de plus
// qu'une trace.
func (s *runSink) WithEvents(recorder EventRecorder) *runSink {
	s.events = recorder
	return s
}

// at date un fait.
//
// Retombe sur l'horloge quand rien n'a été injecté. Le repli n'est pas une
// coquetterie : `runSink` se construit littéralement dans plusieurs tests, et
// une horloge absente y produirait un déréférencement nul loin de la ligne
// fautive — sur un chemin qui, de surcroît, n'émet rien.
func (s *runSink) at() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// emit enregistre un fait sans interrompre ce qui l'a produit.
func (s *runSink) emit(ctx context.Context, fact event.Event) {
	if s.events == nil {
		return
	}

	recorded, err := s.events.RecordEvent(ctx, fact)
	if err != nil {
		s.logger.Error("émission d'un fait",
			"type", fact.Type, "sujet", fact.Subject, "error", err)
		return
	}
	if !recorded {
		// Le cas nominal d'une redélivrance. Journalisé en `Debug` et non tu :
		// c'est ce qui permet de vérifier que la déduplication travaille, sans
		// polluer un journal d'exploitation à chaque redémarrage.
		s.logger.Debug("fait déjà connu, non réémis",
			"type", fact.Type, "sujet", fact.Subject, "cle", fact.DedupKey)
		return
	}
	s.logger.Info("fait constaté", "type", fact.Type, "sujet", fact.Subject)

	// L'aiguillage n'est appelé **que sur un fait neuf**. C'est la première des
	// deux bornes qui empêchent une redélivrance de rejouer des appels sortants ;
	// la seconde est l'unicité par (déclencheur, fait) de `plan_runs`.
	//
	// Deux bornes pour la même garantie, parce que celle-ci décide d'appels qui
	// sortent du cluster au nom de l'instance.
	s.dispatcher.Dispatch(ctx, fact)
}

// WithDispatcher branche l'aiguillage des faits vers les réactions déclarées.
//
// Optionnel comme le reste : sans lui, les faits sont constatés et personne n'y
// réagit — l'état des tranches 2 à 5.
func (s *runSink) WithDispatcher(dispatcher *Dispatcher) *runSink {
	s.dispatcher = dispatcher
	return s
}

// PromotionFacts compose le fait d'une promotion aboutie.
//
// Apporté par le plugin qui promeut : le noyau sait qu'un job rattaché à un
// environnement a conclu, pas ce que cela signifie (doc 16 §16.7).
type PromotionFacts interface {
	Promoted(at time.Time, linked store.JobReleaseContext) event.Event
}

// WithPromotionFacts branche la constatation des promotions.
//
// Sans elle, aucune promotion n'est constatée : c'est l'état d'une instance sans
// le plugin MSE, où rien ne promeut.
func (s *runSink) WithPromotionFacts(facts PromotionFacts) *runSink {
	s.promotions = facts
	return s
}

func (s *runSink) emitReleasePromoted(ctx context.Context, linked store.JobReleaseContext, jobState string) {
	if s.promotions == nil || jobState != store.JobSucceeded || linked.Environment == "" {
		return
	}
	s.emit(ctx, s.promotions.Promoted(s.at(), linked))
}

// WithEngine branche le moteur d'exécution des plans.
//
// C'est lui qui enchaîne les vérifications d'une construction réussie, et qui
// conclut leurs gestes à la fin de leur job. Sans lui, rien ne s'enchaîne — et
// `launchVerification` le dit plutôt que de rendre zéro en silence.
func (s *runSink) WithEngine(engine *Engine) *runSink {
	s.engine = engine
	return s
}

// NewVerificationEngine assemble le moteur qui enchaîne les vérifications.
//
// Le runner est lié au sink : il a besoin du catalogue et de la file, que le sink
// porte déjà. Les assembler ici plutôt que dans `main` évite d'exposer ces deux
// dépendances une seconde fois.
func NewVerificationEngine(
	sink *runSink, plans PlanStore, actions GesteRunner, logger *slog.Logger,
) *Engine {
	// L'aiguillage entre les deux natures de geste vit hors du moteur : celui-ci
	// n'a pas à connaître la différence entre un pipeline, qui devient un job, et
	// une action, qui sort du cluster.
	return NewEngine(plans, gesteRouter{
		pipelines: NewReleaseRunner(sink.catalog, sink.verification, logger),
		actions:   actions,
	}, logger)
}

// planStateOf traduit l'issue d'un job dans le vocabulaire des plans.
//
// Une annulation vaut un échec pour un plan : ce qui suivait ne doit pas partir,
// et il n'y a pas d'état « interrompu » dans le modèle. La nuance subsiste sur le
// job, qui la porte et l'affiche ; la perdre ici ne coûte rien, parce que la seule
// question posée à cet endroit est « la suite part-elle ? ».
func planStateOf(jobState string) plan.State {
	if jobState == store.JobSucceeded {
		return plan.Succeeded
	}
	return plan.Failed
}
