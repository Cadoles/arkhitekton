package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Le moteur d'exécution des plans.
 *
 * # Piloté par les signaux, pas par une horloge
 *
 * Doc 14 §14.9 imaginait une boucle qui consommerait le fil des faits à
 * intervalle. Ce n'est pas ce qui est écrit ici, et l'écart mérite d'être motivé.
 *
 * L'observateur délivre déjà **exactement** les deux signaux dont le moteur a
 * besoin : une construction conclue, et un job terminé. Une boucle à intervalle
 * ajouterait un curseur à tenir, une latence, et une seconde source de vérité sur
 * ce qui a été traité — pour redécouvrir des faits que l'observateur vient de lui
 * passer.
 *
 * Le moteur est donc un **composant**, appelé aux deux endroits où quelque chose
 * arrive. Le jour où des faits viendront de sondes plutôt que de l'observateur,
 * c'est la sonde qui l'appellera, avec le même point d'entrée.
 *
 * # Deux entrées, et rien d'autre
 *
 *   - `Trigger` ouvre une exécution sur un fait, et fait partir sa première étape.
 *   - `Advance` conclut un geste sur la fin de son job, et fait partir la suite.
 *
 * Les deux sont **rappelables sans effet** : `CreatePlanRun` rend l'exécution
 * existante plutôt que d'en ouvrir une seconde, et `plan.Next` ne propose que ce
 * qui attend encore. C'est ce qui rend le moteur tolérant à la redélivrance des
 * runs terminés, qui est la règle et non l'exception.
 */

// PlanStore est ce que le moteur attend de la base.
//
// Interface étroite plutôt que le magasin entier : le moteur ne fait qu'ouvrir,
// lire et marquer, et lui donner le reste l'inviterait à décider de choses qui ne
// lui appartiennent pas — l'état d'une release, notamment, que le plan n'écrit
// jamais (doc 14 §14.5).
type PlanStore interface {
	CreatePlanRun(ctx context.Context, run store.PlanRun) (store.PlanRun, bool, error)
	PlanRunByID(ctx context.Context, id int64) (store.PlanRun, error)
	PlanSteps(ctx context.Context, runID int64) ([]store.PlanStep, error)
	MarkPlanStep(ctx context.Context, runID int64, stage, position int,
		state string, job *int64, detail string) error
	PlanStepForJob(ctx context.Context, jobID int64) (int64, store.PlanStep, error)
	FinishPlanRun(ctx context.Context, runID int64, state string) (bool, error)
}

// ArtifactStore est ce que le moteur attend pour porter les artefacts.
//
// Séparé de [PlanStore] et **optionnel** : un moteur sans lui exécute les plans
// exactement comme avant, et c'est ce qui permet aux tests du modèle de ne pas
// monter une base pour éprouver un enchaînement.
type ArtifactStore interface {
	// RunResults rend ce que le pipeline d'un job a rendu comme résultats.
	RunResults(ctx context.Context, jobID int64) (map[string]string, error)
	RecordPlanArtifacts(ctx context.Context, runID int64,
		releaseID *int64, artifacts []store.PlanArtifact) error
	PlanArtifacts(ctx context.Context, runID int64) ([]store.PlanArtifact, error)
}

// GesteRunner met en œuvre un geste.
//
// Extrait en interface parce que les deux natures de geste n'ont rien en commun :
// un pipeline devient un job de la file, une action sort du cluster. Le moteur
// n'a pas à connaître la différence — il demande, et retient ce qu'on lui répond.
type GesteRunner interface {
	// Run met le geste en œuvre. Rend l'identifiant du job pour un geste de type
	// pipeline, nil pour une action ; et un motif quand il n'est pas parti.
	Run(ctx context.Context, run store.PlanRun, geste plan.Geste) (*int64, error)
}

// Engine fait avancer les exécutions de plan.
type Engine struct {
	store  PlanStore
	runner GesteRunner
	// stages est prévenu du règlement d'une étape à rôle. Nil : les plans
	// s'exécutent sans que rien n'en dépende au-dehors.
	stages StageSink
	// artifacts porte ce que les gestes produisent. Nil : aucun artefact n'est
	// retenu, et `{artifacts.…}` reste littéral dans les gestes qui suivent.
	artifacts ArtifactStore
	// products dit ce qu'une famille du catalogue laisse derrière elle.
	products func(ref string) string
	logger   *slog.Logger
}

func NewEngine(plans PlanStore, runner GesteRunner, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{store: plans, runner: runner, logger: logger}
}

// Trigger ouvre une exécution sur un fait, et fait partir ce qui peut partir.
//
// Rend le nombre de gestes effectivement partis. L'appelant en dépend : une
// release dont aucune vérification n'a pu partir rejoint `ready` plutôt que
// d'attendre un verdict que personne ne rendra.
//
// Un plan vide n'ouvre **aucune** exécution : une ligne sans geste serait une
// exécution que rien ne fait avancer, et qu'il faudrait un balai pour retrouver.
func (e *Engine) Trigger(ctx context.Context, request store.PlanRun) int {
	if request.Plan.Empty() {
		return 0
	}
	if err := request.Plan.Validate(); err != nil {
		// Refusé ici plutôt que déroulé à moitié : un plan invalide laisserait
		// une exécution ouverte sur des gestes que le modèle ne sait pas
		// enchaîner.
		e.logger.Error("plan refusé", "declencheur", request.Trigger,
			"fait", request.EventKey, "error", err)
		return 0
	}

	run, created, err := e.store.CreatePlanRun(ctx, request)
	if err != nil {
		e.logger.Error("ouverture d'une exécution de plan",
			"declencheur", request.Trigger, "fait", request.EventKey, "error", err)
		return 0
	}
	if !created {
		// Le cas nominal d'une redélivrance. On fait tout de même avancer : une
		// exécution ouverte puis interrompue par un redémarrage doit repartir.
		e.logger.Debug("exécution de plan déjà ouverte",
			"execution", run.ID, "fait", request.EventKey)
	}

	return e.advance(ctx, run)
}

// Advance conclut le geste qu'un job mettait en œuvre, et fait partir la suite.
//
// Sans effet quand le job ne vient d'aucun plan, ce qui est le cas courant : la
// plupart des jobs sont lancés à la main ou par la création d'une release.
func (e *Engine) Advance(ctx context.Context, jobID int64, state plan.State) {
	runID, step, err := e.store.PlanStepForJob(ctx, jobID)
	if err != nil {
		if !errors.Is(err, store.ErrPlanRunNotFound) {
			e.logger.Error("geste d'un job conclu", "job", jobID, "error", err)
		}
		return
	}

	// Un geste déjà conclu ne se reconclut pas. L'observateur redélivre les runs
	// terminés, et récrire l'état ferait repartir l'étape suivante à chaque
	// passage — le défaut exact qu'`advanceRelease` documente pour les
	// vérifications.
	if plan.State(step.State).Terminal() {
		e.logger.Debug("geste déjà conclu, redélivrance ignorée",
			"execution", runID, "job", jobID, "etat", step.State)
		return
	}

	if err := e.store.MarkPlanStep(ctx, runID, step.Stage, step.Position,
		string(state), nil, ""); err != nil {
		e.logger.Error("conclusion d'un geste", "execution", runID, "job", jobID, "error", err)
		return
	}

	run, err := e.store.PlanRunByID(ctx, runID)
	if err != nil {
		e.logger.Error("exécution d'un geste conclu", "execution", runID, "error", err)
		return
	}

	// Ce que le geste a produit, **avant** de faire partir la suite : l'étape
	// suivante peut le viser, et le relever après elle la ferait partir sur un
	// artefact qui n'existe pas encore.
	if state == plan.Succeeded {
		e.collectArtifacts(ctx, run, step, jobID)
	}

	e.advance(ctx, run)
}

// collectArtifacts retient ce qu'un geste vient de produire.
//
// # Les résultats du pipeline, tels quels
//
// Chaque résultat Tekton devient un artefact portant son nom : `image`, `tag`.
// Aucune traduction, aucune sélection — le pipeline dit ce qu'il rend, et le plan
// le retient. Choisir ici lesquels sont « de vrais artefacts » demanderait une
// seconde déclaration à tenir d'accord avec celle du pipeline.
//
// # Un relevé qui échoue n'arrête pas le plan
//
// Le geste a réussi ; perdre ce fait parce qu'on n'a pas su lire ses résultats
// serait pire que de continuer sans eux. L'étape suivante échouera alors
// nommément sur l'artefact manquant, ce qui est un diagnostic, là où une
// exécution arrêtée ici n'en serait pas un.
func (e *Engine) collectArtifacts(
	ctx context.Context, run store.PlanRun, step store.PlanStep, jobID int64,
) {
	if e.artifacts == nil {
		return
	}

	results, err := e.artifacts.RunResults(ctx, jobID)
	if err != nil {
		e.logger.Error("résultats d'un geste conclu",
			"execution", run.ID, "job", jobID, "error", err)
		return
	}
	if len(results) == 0 {
		return
	}

	kind := ""
	if e.products != nil {
		kind = e.products(step.Ref)
	}

	produced := make([]store.PlanArtifact, 0, len(results))
	for name, reference := range results {
		produced = append(produced, store.PlanArtifact{
			Stage: step.Stage, Position: step.Position,
			Name: name, Kind: kind, Reference: reference,
		})
	}

	if err := e.artifacts.RecordPlanArtifacts(ctx, run.ID, run.ReleaseID, produced); err != nil {
		e.logger.Error("artefacts d'un geste conclu",
			"execution", run.ID, "job", jobID, "error", err)
		return
	}
	e.logger.Info("artefacts relevés", "execution", run.ID,
		"geste", step.Ref, "artefacts", len(produced))
}

// advance fait partir ce qui peut partir, et conclut l'exécution si elle est
// réglée. Rend le nombre de gestes partis.
func (e *Engine) advance(ctx context.Context, run store.PlanRun) int {
	progress, err := e.progressOf(ctx, run)
	if err != nil {
		e.logger.Error("avancement d'une exécution", "execution", run.ID, "error", err)
		return 0
	}

	// Avant de faire partir la suite : prévenir de ce qui vient de se régler.
	//
	// L'ordre compte. Une release doit passer en `verifying` **avant** que ses
	// tests ne soient enfilés, sans quoi le premier test conclu trouverait une
	// release encore en construction et sa transition serait refusée.
	e.notifyStages(ctx, run, progress)

	// Ce que les étapes déjà conclues ont produit, ajouté au contexte. C'est ce
	// qui permet à un geste de test de viser l'image que la construction vient de
	// publier : `{artifacts.portal.image}`.
	run.Context = e.withArtifacts(ctx, run, progress.Plan)

	started := 0
	for _, step := range progress.Next() {
		if !step.Run {
			// Sa condition n'est pas remplie. Marqué `skipped` et non laissé en
			// attente : une étape dont un geste reste en attente ne se règle
			// jamais, et l'exécution s'arrêterait là sans rien dire.
			e.mark(ctx, run.ID, step.Position, plan.Skipped, nil,
				"condition non remplie")
			continue
		}

		job, err := e.runner.Run(ctx, run, step.Geste)
		if err != nil {
			e.logger.Error("mise en œuvre d'un geste", "execution", run.ID,
				"geste", step.Geste.Ref, "error", err)
			e.mark(ctx, run.ID, step.Position, plan.Failed, nil, err.Error())
			continue
		}

		started++
		if job == nil {
			// Une action est conclue à l'instant où elle est faite : rien
			// n'arrivera plus tard pour la conclure.
			e.mark(ctx, run.ID, step.Position, plan.Succeeded, nil, "")
			continue
		}
		// Un geste de type pipeline reste **en cours** : c'est la conclusion de
		// son job qui le réglera. Le marquer réussi ici était le pis-aller de
		// l'exécution synchrone, et c'est précisément ce que cette tranche corrige.
		e.mark(ctx, run.ID, step.Position, plan.Running, job, "")
	}

	// Relu après les écritures : `Next` a pu tout régler — une étape entièrement
	// sautée, une action qui conclut — et l'exécution est alors terminée.
	if started == 0 || len(progress.Next()) > 0 {
		e.concludeIfSettled(ctx, run)
	}
	return started
}

// StageSink est prévenu qu'une étape portant un rôle vient de se régler.
//
// C'est l'ancrage du cycle de vie d'une release dans le plan (doc 15 §15.3). Le
// moteur ne sait rien des releases : il dit « l'étape de construction s'est
// réglée ainsi », et ce qu'on en fait ne le regarde pas.
type StageSink interface {
	StageSettled(ctx context.Context, run store.PlanRun,
		role plan.Role, outcome plan.State)
}

// WithArtifacts branche le relevé de ce que les gestes produisent.
//
// `products` traduit une référence de catalogue en nature d'artefact — le
// `produces` d'un pipeline. Le moteur ne connaît pas le catalogue, et le lui
// donner l'inviterait à décider de ce qui se lance.
func (e *Engine) WithArtifacts(artifacts ArtifactStore, products func(ref string) string) *Engine {
	e.artifacts, e.products = artifacts, products
	return e
}

// WithStages branche l'observateur des étapes à rôle.
func (e *Engine) WithStages(sink StageSink) *Engine {
	e.stages = sink
	return e
}

// notifyStages prévient de chaque étape à rôle déjà réglée.
//
// # Sans mémoire de ce qui a déjà été annoncé
//
// La même étape est donc annoncée à chaque avancement. C'est délibéré : tenir
// une marque de « déjà prévenu » demanderait une colonne, et l'oublier une seule
// fois laisserait une release bloquée sans que rien ne le dise.
//
// La répétition est sans effet parce que le destinataire écrit **sous
// condition** — `AdvanceReleaseState(depuis, vers)` ne réussit qu'une fois. C'est
// la même idempotence que l'ancienne conclusion de construction, obtenue au même
// endroit.
func (e *Engine) notifyStages(ctx context.Context, run store.PlanRun, progress plan.Progress) {
	if e.stages == nil {
		return
	}
	for index, stage := range progress.Plan.Stages {
		if stage.Role == "" {
			// Une étape sans rôle ne touche à rien. C'est ce qui rend une étape de
			// notification inoffensive pour le cycle de vie d'une release.
			continue
		}
		outcome, settled := progress.StageOutcome(index)
		if !settled {
			continue
		}
		e.stages.StageSettled(ctx, run, stage.Role, outcome)
	}
}

// withArtifacts ajoute au contexte ce que les gestes conclus ont produit.
//
// # La forme : `artifacts.<geste>.<nom>`
//
// La clé est le **libellé** du geste producteur, pas sa position : c'est lui
// qu'on lit dans l'éditeur, et `{artifacts.portal.image}` se comprend là où
// `{artifacts.0.0.image}` demanderait de compter les étapes.
//
// Trois gestes de construction portant la même référence de catalogue sont ainsi
// distingués par leur libellé — c'est précisément pour cela que la composition
// d'un plan de release le renseigne avec le nom de l'image.
//
// # Une recherche, pas un langage
//
// Le contexte gagne des clés ; `action.Expand` fait le reste. Pas d'opérateur,
// pas de condition, pas d'appel — la promesse de doc 14 §14.2 tient (doc 15
// §15.4).
func (e *Engine) withArtifacts(
	ctx context.Context, run store.PlanRun, sujet plan.Plan,
) map[string]string {
	if e.artifacts == nil {
		return run.Context
	}

	produced, err := e.artifacts.PlanArtifacts(ctx, run.ID)
	if err != nil {
		// Sans eux, un geste qui vise un artefact échouera nommément. C'est un
		// meilleur diagnostic qu'une exécution arrêtée ici.
		e.logger.Error("artefacts d'une exécution", "execution", run.ID, "error", err)
		return run.Context
	}
	if len(produced) == 0 {
		return run.Context
	}

	enrichi := make(map[string]string, len(run.Context)+len(produced))
	maps.Copy(enrichi, run.Context)
	for _, artifact := range produced {
		libelle := gesteLabelAt(sujet, artifact.Stage, artifact.Position)
		if libelle == "" {
			continue
		}
		enrichi["artifacts."+libelle+"."+artifact.Name] = artifact.Reference
	}
	return enrichi
}

// gesteLabelAt lit le libellé d'un geste dans le plan figé.
//
// Le plan **figé**, et non le catalogue courant : un geste renommé depuis ne doit
// pas rendre introuvable ce qu'il a produit sous son ancien nom.
func gesteLabelAt(sujet plan.Plan, stage, position int) string {
	if stage < 0 || stage >= len(sujet.Stages) {
		return ""
	}
	gestes := sujet.Stages[stage].Gestes
	if position < 0 || position >= len(gestes) {
		return ""
	}
	return gestes[position].Label
}

// mark écrit le sort d'un geste sans interrompre l'avancement des autres.
func (e *Engine) mark(
	ctx context.Context, runID int64, position plan.Position,
	state plan.State, job *int64, detail string,
) {
	if err := e.store.MarkPlanStep(ctx, runID, position.Stage, position.Geste,
		string(state), job, detail); err != nil {
		e.logger.Error("marquage d'un geste", "execution", runID,
			"etape", position.Stage, "geste", position.Geste, "error", err)
	}
}

// concludeIfSettled conclut une exécution dont plus rien ne bouge.
func (e *Engine) concludeIfSettled(ctx context.Context, run store.PlanRun) {
	progress, err := e.progressOf(ctx, run)
	if err != nil {
		e.logger.Error("conclusion d'une exécution", "execution", run.ID, "error", err)
		return
	}
	if !progress.Done() {
		return
	}

	state := store.PlanSucceeded
	if progress.Outcome() == plan.Failed {
		state = store.PlanFailed
	}

	concluded, err := e.store.FinishPlanRun(ctx, run.ID, state)
	if err != nil {
		e.logger.Error("conclusion d'une exécution", "execution", run.ID, "error", err)
		return
	}
	if concluded {
		e.logger.Info("exécution de plan conclue",
			"execution", run.ID, "declencheur", run.Trigger, "issue", state)
	}
}

// progressOf recompose l'avancement depuis la base.
//
// Relu à chaque tour plutôt que tenu en mémoire : les gestes se concluent depuis
// d'autres appels — la fin d'un job arrive par l'observateur — et un avancement
// gardé en mémoire serait faux dès le second.
func (e *Engine) progressOf(ctx context.Context, run store.PlanRun) (plan.Progress, error) {
	steps, err := e.store.PlanSteps(ctx, run.ID)
	if err != nil {
		return plan.Progress{}, err
	}

	progress := plan.Start(run.Plan)
	for _, step := range steps {
		if step.Stage >= len(progress.States) ||
			step.Position >= len(progress.States[step.Stage]) {
			// Un geste hors du plan figé. Ne devrait pas arriver — le plan est
			// copié dans son exécution précisément pour cela — mais le taire
			// ferait avancer sur un état incomplet.
			return plan.Progress{}, fmt.Errorf(
				"geste %d/%d hors du plan de l'exécution %d",
				step.Stage, step.Position, run.ID)
		}
		progress.States[step.Stage][step.Position] = plan.State(step.State)
	}
	return progress, nil
}
