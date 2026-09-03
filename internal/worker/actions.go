package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/action"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/webhook"
)

/*
 * L'exécution des actions d'un plan.
 *
 * # Une action est conclue à l'instant où elle est faite
 *
 * C'est ce qui la distingue d'un geste de type pipeline : celui-ci devient un job
 * et attend la conclusion de son run, alors qu'une action rend son verdict tout de
 * suite. Le moteur le sait par la valeur nulle qu'elle rend à la place d'un
 * identifiant de job.
 *
 * # Ce que le contexte apporte, et ce qu'il n'apporte pas
 *
 * Les gabarits sont liés à la charge du fait déclencheur — `{tag}`, `{outcome}`.
 * Les **secrets n'y entrent jamais** : un champ sensible est désigné, et posé
 * comme en-tête par ce paquet sans jamais traverser un gabarit ni un journal
 * (doc 14 §14.7). C'est ce qui empêche qu'un jeton finisse interpolé dans une URL
 * qu'un message d'erreur recopiera.
 */

// SecretResolver rend la valeur en clair d'un champ sensible désigné.
//
// Interface étroite, et volontairement pauvre : elle ne sait faire qu'une chose,
// et le geste qui l'appelle ne peut donc rien en tirer d'autre. La désignation
// s'écrit `plugin.champ` — `redmine.api_key`.
type SecretResolver interface {
	ResolveSecret(ctx context.Context, projectID *int64, reference string) (string, error)
}

// ActionRunner met en œuvre les actions internes d'un plan.
type ActionRunner struct {
	webhooks *webhook.Client
	secrets  SecretResolver
	logger   *slog.Logger
}

func NewActionRunner(
	webhooks *webhook.Client, secrets SecretResolver, logger *slog.Logger,
) *ActionRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &ActionRunner{webhooks: webhooks, secrets: secrets, logger: logger}
}

// Run met en œuvre un geste d'action.
//
// Rend toujours un identifiant de job nul : une action ne passe pas par la file,
// et n'a rien à attendre.
func (r *ActionRunner) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	descriptor, err := action.Get(geste.Ref)
	if err != nil {
		return nil, err
	}
	// Revalidé ici bien que le plan l'ait été à l'enregistrement : un plan figé
	// dans une exécution a pu être écrit par une version antérieure du registre,
	// et partir sur des paramètres qu'une action ne déclare plus produirait un
	// appel dont personne ne saurait dire ce qu'il a envoyé.
	if err := descriptor.Validate(geste.Params); err != nil {
		return nil, err
	}

	switch geste.Ref {
	case action.Webhook:
		return nil, r.postWebhook(ctx, run, geste)
	default:
		// `forge.status` est déclarée et n'est pas encore mise en œuvre : elle
		// demande un client d'écriture vers la forge, qui viendra avec les faits
		// de plugin. Refuser nommément vaut mieux que rendre « parti » sur un
		// geste que personne n'exécute.
		return nil, fmt.Errorf("action %q déclarée mais pas encore exécutée", geste.Ref)
	}
}

// postWebhook émet l'appel sortant d'un geste `webhook`.
func (r *ActionRunner) postWebhook(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) error {
	if r.webhooks == nil {
		return fmt.Errorf("appels sortants non configurés")
	}

	target := action.Expand(geste.Params["url"], run.Context)
	body, err := webhookBody(geste.Params["body"], run)
	if err != nil {
		return err
	}

	headers, err := r.authHeader(ctx, run, geste)
	if err != nil {
		return err
	}

	response, err := r.webhooks.Post(ctx, target, headers, body)
	if err != nil {
		return err
	}
	if !response.OK() {
		// Un refus du destinataire est un **constat** : le geste échoue, et le
		// motif porte le code et un extrait. Sans eux, « échoué » n'apprend rien
		// et oblige à relancer pour voir.
		return fmt.Errorf("le destinataire a répondu %d: %s",
			response.Status, truncate(response.Body, 200))
	}

	r.logger.Info("appel sortant abouti",
		"execution", run.ID, "geste", geste.Ref, "code", response.Status)
	return nil
}

// webhookBody compose la charge envoyée.
//
// Un corps vide envoie la **charge du fait** telle quelle. C'est le défaut utile :
// un destinataire générique reçoit tout ce qu'on sait, et personne n'a de gabarit
// à écrire pour commencer.
func webhookBody(template string, run store.PlanRun) ([]byte, error) {
	if strings.TrimSpace(template) == "" {
		payload := map[string]string{}
		maps.Copy(payload, run.Context)
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("charge illisible: %w", err)
		}
		return encoded, nil
	}

	expanded := action.Expand(template, run.Context)
	// Le corps est annoncé `application/json` : l'envoyer sans vérifier ferait
	// recevoir au destinataire un JSON invalide, qu'il rejetterait avec un
	// message sur **sa** syntaxe plutôt que sur notre gabarit.
	if !json.Valid([]byte(expanded)) {
		return nil, fmt.Errorf("le corps rendu n'est pas du JSON valide")
	}
	return []byte(expanded), nil
}

// authHeader compose l'en-tête d'authentification, sans jamais rendre le jeton.
//
// C'est le seul chemin par lequel un secret atteint un appel sortant. Il ne passe
// ni par un gabarit, ni par le corps, ni par un journal — la valeur ne vit que
// dans la carte rendue, consommée immédiatement par le client.
func (r *ActionRunner) authHeader(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (map[string]string, error) {
	name := strings.TrimSpace(geste.Params["auth_header"])
	reference := strings.TrimSpace(geste.Params["auth_secret"])

	switch {
	case name == "" && reference == "":
		return nil, nil
	case name == "" || reference == "":
		// Les deux vont ensemble. Un jeton sans en-tête ne partirait nulle part ;
		// un en-tête sans jeton partirait vide, et le destinataire refuserait sur
		// un message d'authentification qui ne dirait pas que la valeur manque
		// chez nous.
		return nil, fmt.Errorf(
			"l'en-tête d'authentification et le jeton se déclarent ensemble")
	case r.secrets == nil:
		return nil, fmt.Errorf("le scellement n'est pas configuré : le jeton %q "+
			"ne peut pas être ouvert", reference)
	}

	value, err := r.secrets.ResolveSecret(ctx, run.ProjectID, reference)
	if err != nil {
		// Le message ne cite pas la valeur, et pour cause : c'est celle qu'on n'a
		// pas su ouvrir.
		return nil, fmt.Errorf("jeton %q illisible: %w", reference, err)
	}
	if value == "" {
		return nil, fmt.Errorf("le jeton %q n'est pas renseigné", reference)
	}
	return map[string]string{name: value}, nil
}

// truncate borne un extrait de réponse.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// gesteRouter aiguille un geste vers celui qui sait le mettre en œuvre.
//
// Le moteur ne connaît qu'un `GesteRunner` ; les deux natures de geste n'ayant
// rien en commun, l'aiguillage vit ici plutôt que dans le moteur — qui n'aurait
// alors plus de raison d'ignorer la différence.
type gesteRouter struct {
	pipelines GesteRunner
	actions   GesteRunner
}

func (r gesteRouter) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	// Les deux branches gardent leur runner. Un moteur assemblé sans l'un des
	// deux — ce qui arrive dans les tests, et arriverait dans un déploiement où
	// les appels sortants ne sont pas configurés — doit refuser nommément plutôt
	// que déréférencer une interface nulle : le geste échoue avec son motif, et
	// l'exécution se conclut au lieu de faire tomber la boucle d'observation.
	switch geste.Kind {
	case plan.GestePipeline:
		if r.pipelines == nil {
			return nil, fmt.Errorf("enchaînement de pipelines non configuré")
		}
		return r.pipelines.Run(ctx, run, geste)
	case plan.GesteAction:
		if r.actions == nil {
			return nil, fmt.Errorf("actions non configurées")
		}
		return r.actions.Run(ctx, run, geste)
	default:
		return nil, fmt.Errorf("nature de geste %q inconnue", geste.Kind)
	}
}

// ActionIDs énumère les actions internes, pour la validation d'un plan.
func ActionIDs() []string {
	registry := action.Registry()
	ids := make([]string, 0, len(registry))
	for _, descriptor := range registry {
		ids = append(ids, descriptor.ID)
	}
	sort.Strings(ids)
	return ids
}
