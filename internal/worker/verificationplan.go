package worker

import (
	"context"
	"fmt"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La chaîne de vérification, exprimée comme plan.
 *
 * C'est la **porte** du chantier des automatisations, et elle se franchit ici
 * (doc 14 §14.1) : si l'enchaînement qui existe depuis le lot 3 n'entre pas dans
 * le modèle de plan, le modèle est trop pauvre, et on l'apprend avant d'avoir
 * écrit une ligne d'interface.
 *
 * Il y entre, et sa forme dit pourquoi : **une seule étape**, N gestes de type
 * pipeline exécutés en parallèle. C'est exactement ce que `launchVerification`
 * faisait en boucle, et la boucle était déjà un plan — non déclaré.
 *
 * # Ce que le plan ne porte pas
 *
 * Les paramètres. Ils dépendent du fait déclencheur — la révision construite, le
 * tag, le registre — et un plan qui les figerait ne servirait qu'une fois. Le
 * plan **nomme** le geste ; le moteur lie ses valeurs au moment de partir.
 *
 * C'est la même séparation que partout ailleurs dans ce dépôt : la déclaration
 * dit quoi, l'exécution résout avec quoi.
 */

// verificationPlan compose le plan de vérification d'un projet.
//
// Rend un plan **vide** quand rien ne s'applique, et c'est un cas normal : un
// projet peut n'enchaîner aucune vérification. L'appelant conclut alors tout de
// suite plutôt que d'attendre un verdict que personne ne rendra.
//
// L'ordre des gestes suit celui du catalogue. Il n'a pas d'effet sur l'exécution
// — ils partent ensemble — mais il en a un sur la lecture : deux plans composés
// dans deux ordres différents décriraient la même chose sans se ressembler.
func (s *runSink) verificationPlan(ctx context.Context, projectID int64) plan.Plan {
	if s.catalog == nil {
		// Sans catalogue, rien n'est lançable. Le distinguer d'un projet qui
		// n'enchaîne rien appartient à l'appelant : ici, les deux donnent un plan
		// vide, et c'est le journal qui dit lequel des deux on regarde.
		return plan.Plan{}
	}

	var gestes []plan.Geste
	for _, kind := range s.catalog.VerificationKinds() {
		pipeline, found := s.catalog.Find(kind)
		if !found {
			s.logger.Warn("famille de vérification absente du catalogue", "kind", kind)
			continue
		}

		// Ce que le **projet** déclare vérifier. Le catalogue dit ce que
		// l'instance sait lancer, ce qui n'est pas la même question : un projet
		// sans campagne de charge voyait `load-tests` s'enchaîner, échouer faute
		// de scénario, et laisser sa release attendre un verdict qui ne viendrait
		// jamais.
		if !s.pipelineApplies(ctx, projectID, kind) {
			s.logger.Info("vérification non enchaînée", "kind", kind,
				"raison", "le projet ne déclare pas cette famille")
			continue
		}

		gestes = append(gestes, plan.Geste{
			Kind: plan.GestePipeline, Ref: pipeline.Kind, Label: pipeline.Label,
		})
	}

	if len(gestes) == 0 {
		return plan.Plan{}
	}
	return plan.Plan{Stages: []plan.Stage{{
		Label: "Vérifications", Gestes: gestes,
	}}}
}

// Clés du contexte d'exécution d'un plan de vérification.
//
// Nommées plutôt qu'écrites en clair aux deux bouts : le plan les pose, le runner
// les lit, et deux orthographes produiraient un geste sans paramètres dont rien
// ne dirait pourquoi.
const (
	contextRevision = "revision"
	contextTag      = "tag"
)

// verificationTrigger nomme le déclencheur intégré de la chaîne de vérification.
//
// `builtin:` le distingue des réactions déclarées, qui porteront `reaction:<id>`.
// Les deux partagent la même unicité par fait, et c'est ce qui permettra à une
// réaction déclarée de coexister avec celle-ci sans que l'une bloque l'autre.
const verificationTrigger = "builtin:verification"

// verificationRunner met en œuvre un geste de vérification.
//
// Réalise [GesteRunner]. Le moteur ne connaît pas la différence entre un pipeline
// et une action : il demande, et retient l'identifiant du job qu'on lui rend.
type verificationRunner struct{ sink *runSink }

func (r verificationRunner) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	if geste.Kind != plan.GestePipeline {
		// Les actions arrivent avec la tranche suivante. Refuser nommément vaut
		// mieux que de rendre « parti » sur un geste que personne n'exécute.
		return nil, fmt.Errorf("geste %q: les actions ne sont pas encore exécutées", geste.Ref)
	}

	pipeline, found := r.sink.catalog.Find(geste.Ref)
	if !found {
		return nil, fmt.Errorf("famille %q absente du catalogue", geste.Ref)
	}

	// Les valeurs sortent de ce que le catalogue déclare vouloir : `revision`,
	// `app-revision` et `mse-revision` désignent la même chose sous trois noms
	// hérités de pipelines écrits séparément, et c'est leur `suggest: git-ref`
	// commun qui le dit. Tous valent le commit construit — c'est lui qu'on
	// vérifie, pas la branche dont il vient (B17).
	params, missing := pipeline.VerificationInputs(
		run.Context[contextRevision], run.Context[contextTag], r.sink.registryProject)
	if missing != "" {
		// Un paramètre requis qu'on ne sait pas renseigner ferait échouer le run à
		// l'ordonnancement : ne pas l'enfiler du tout est plus honnête, et le
		// motif dit lequel manque.
		return nil, fmt.Errorf("paramètre %q non renseignable", missing)
	}

	id, err := r.sink.verification.Enqueue(ctx, store.JobRequest{
		Kind:        pipeline.Kind,
		Params:      params,
		RequestedBy: verificationRequester,
		ReleaseID:   run.ReleaseID,
		// Le projet, sans quoi la vérification part **sans** et monte le secret du
		// déploiement au lieu de la clé du projet. Elle clone comme la
		// construction : l'oublier annulait la séparation des clés pour la moitié
		// des runs d'une release.
		ProjectID: run.ProjectID,
	})
	if err != nil {
		return nil, err
	}

	r.sink.logger.Info("vérification lancée",
		"release", run.Context[contextTag], "kind", geste.Ref, "job", id,
		"revision", run.Context[contextRevision])
	return &id, nil
}
