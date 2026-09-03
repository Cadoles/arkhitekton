package worker

import (
	"context"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/tekton"
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
}

// factOutcome traduit l'issue d'un run dans le vocabulaire des faits.
//
// Une traduction et non un emprunt : les états de Tekton décrivent un objet du
// cluster, les issues d'un fait décrivent ce qui est arrivé au projet. Les
// confondre ferait entrer `PipelineRunTimeout` dans une charge que des réactions
// appareillent.
func factOutcome(status tekton.Status) event.Outcome {
	switch status {
	case tekton.StatusSucceeded:
		return event.Succeeded
	case tekton.StatusCancelled:
		return event.Cancelled
	default:
		return event.Failed
	}
}

// jobOutcome traduit l'issue d'un job.
func jobOutcome(state string) event.Outcome {
	switch state {
	case store.JobSucceeded:
		return event.Succeeded
	case store.JobCancelled:
		return event.Cancelled
	default:
		return event.Failed
	}
}

// emitPipelineFinished constate qu'un run s'est terminé.
//
// Le fait générique, émis pour **tout** run terminé — y compris ceux qui ne
// visent aucune release. Sa clé porte le run et non le job : un job en échec est
// remis en file, et ses tentatives sont autant de runs distincts.
func (s *runSink) emitPipelineFinished(ctx context.Context, runID int64, run tekton.Run, releaseID *int64) {
	if s.events == nil || !run.Status.Terminal() || run.JobID == 0 {
		return
	}

	job, err := s.db.GetJob(ctx, run.JobID)
	if err != nil {
		s.logger.Error("job d'un run terminé", "run", run.Name, "job", run.JobID, "error", err)
		return
	}

	s.emit(ctx, event.PipelineFinishedAt(
		s.at(), job.ProjectID, runID, run.JobID,
		run.Name, job.Kind, factOutcome(run.Status), releaseID))
}

// emitReleaseBuilt constate la conclusion d'une construction.
//
// Appelé après la transition d'état, qui est l'endroit où la conclusion devient
// vraie une seule fois.
func (s *runSink) emitReleaseBuilt(
	ctx context.Context, linked store.JobReleaseContext, jobState string,
) {
	s.emit(ctx, event.ReleaseBuiltAt(
		s.at(), linked.ProjectID, linked.ReleaseID, linked.JobID,
		linked.Tag, jobOutcome(jobState), linked.GitCommit))
}

// emitReleaseVerified constate qu'une release a reçu tous ses verdicts.
func (s *runSink) emitReleaseVerified(
	ctx context.Context, linked store.JobReleaseContext, target release.State, kinds []string,
) {
	outcome := event.Succeeded
	if target != release.StateReady {
		outcome = event.Failed
	}

	s.emit(ctx, event.ReleaseVerifiedAt(
		s.at(), linked.ProjectID, linked.ReleaseID, linked.JobID,
		linked.Tag, outcome, kinds))
}

// emitReleasePromoted constate qu'une promotion a abouti.
//
// **Le re-tag a réussi**, ce qui est un constat et non l'intention de le
// demander. Ce que le déployeur en fait ensuite est une autre affaire, et c'est
// l'écran des environnements qui la raconte (B8).
//
// Une promotion échouée n'émet rien de particulier : `pipeline.finished` la porte
// déjà, avec son issue. Ajouter `release.promoted` en échec inventerait un fait
// dont le nom dirait le contraire de ce qui s'est passé.
func (s *runSink) emitReleasePromoted(ctx context.Context, linked store.JobReleaseContext, jobState string) {
	if jobState != store.JobSucceeded || linked.Environment == "" {
		return
	}

	s.emit(ctx, event.ReleasePromotedAt(
		s.at(), linked.ProjectID, linked.ReleaseID, linked.JobID,
		linked.Tag, linked.Environment))
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
		pipelines: verificationRunner{sink: sink},
		actions:   actions,
	}, logger)
}

// planStateOf traduit l'issue d'un job dans le vocabulaire des plans.
//
// Une annulation vaut un échec pour un plan : ce qui suivait ne doit pas partir,
// et il n'y a pas d'état « interrompu » dans le modèle. La nuance existe dans le
// job et dans le fait `pipeline.finished` ; la perdre ici ne coûte rien, parce
// que la seule question posée à cet endroit est « la suite part-elle ? ».
func planStateOf(jobState string) plan.State {
	if jobState == store.JobSucceeded {
		return plan.Succeeded
	}
	return plan.Failed
}
