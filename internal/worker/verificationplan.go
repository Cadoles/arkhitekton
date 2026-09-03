package worker

import (
	"context"

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

// runVerificationPlan exécute le plan de vérification et rend le nombre de gestes
// effectivement partis.
//
// Synchrone, comme l'était la boucle qu'il remplace. La bascule vers le moteur
// asynchrone — qui consomme les faits et persiste l'avancement — est la suite du
// chantier ; ce qui compte à ce stade est que le **modèle** décide de ce qui
// part, et non plus une boucle écrite à la main.
//
// Le décompte est rendu parce que l'appelant en dépend : une release dont aucune
// vérification n'a pu partir rejoint `ready` plutôt que d'attendre indéfiniment un
// verdict que personne ne rendra.
func (s *runSink) runVerificationPlan(
	ctx context.Context, linked store.JobReleaseContext, sujet plan.Plan,
) int {
	// Les tests portent sur ce qui a été construit, donc sur le commit — jamais
	// sur la branche d'où il vient, qui a pu bouger depuis (B17).
	revision := linked.GitCommit
	if revision == "" {
		revision = linked.GitRef
	}
	if revision == "" {
		s.logger.Warn("vérification non lancée",
			"release", linked.Tag, "raison", "aucune référence Git sur la release")
		return 0
	}

	launched := 0
	progress := plan.Start(sujet)

	for {
		steps := progress.Next()
		if len(steps) == 0 {
			return launched
		}

		for _, step := range steps {
			state := plan.Skipped
			if step.Run {
				if s.enqueueGeste(ctx, linked, step.Geste, revision) {
					launched++
					// `succeeded` dit ici « parti », et non « conclu ». C'est le
					// pis-aller d'une exécution synchrone : l'avancement ne survit
					// pas à l'appel, et personne ne viendra le corriger. Le moteur
					// asynchrone rendra cet état vrai — c'est même sa raison
					// d'être.
					state = plan.Succeeded
				} else {
					state = plan.Failed
				}
			}
			progress, _ = progress.Mark(step.Position, state)
		}
	}
}

// enqueueGeste enfile un geste de type pipeline, et dit s'il est parti.
func (s *runSink) enqueueGeste(
	ctx context.Context, linked store.JobReleaseContext, geste plan.Geste, revision string,
) bool {
	pipeline, found := s.catalog.Find(geste.Ref)
	if !found {
		s.logger.Warn("geste sans famille au catalogue",
			"release", linked.Tag, "geste", geste.Ref)
		return false
	}

	// Les valeurs sortent de ce que le catalogue déclare vouloir : `revision`,
	// `app-revision` et `mse-revision` désignent la même chose sous trois noms
	// hérités de pipelines écrits séparément, et c'est leur `suggest: git-ref`
	// commun qui le dit. Tous valent le commit construit — c'est lui qu'on
	// vérifie, pas la branche dont il vient.
	params, missing := pipeline.VerificationInputs(revision, linked.Tag, s.registryProject)
	if missing != "" {
		// Un paramètre requis qu'on ne sait pas renseigner ferait échouer le run à
		// l'ordonnancement : ne pas l'enfiler du tout est plus honnête, et le
		// journal dit lequel manque.
		s.logger.Warn("famille de vérification non lançable",
			"release", linked.Tag, "kind", geste.Ref, "parametre_manquant", missing)
		return false
	}

	id, err := s.verification.Enqueue(ctx, store.JobRequest{
		Kind:        pipeline.Kind,
		Params:      params,
		RequestedBy: verificationRequester,
		ReleaseID:   &linked.ReleaseID,
		// Le projet, sans quoi la vérification part **sans** et monte le secret du
		// déploiement au lieu de la clé du projet. Elle clone comme la
		// construction : l'oublier annulait la séparation des clés pour la moitié
		// des runs d'une release.
		ProjectID: &linked.ProjectID,
	})
	if err != nil {
		s.logger.Error("mise en file d'une vérification",
			"release", linked.Tag, "kind", geste.Ref, "error", err)
		return false
	}

	s.logger.Info("vérification lancée",
		"release", linked.Tag, "kind", geste.Ref, "job", id, "revision", revision)
	return true
}
