package worker

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/action"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/gitea"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * L'exécution des actions qu'un plugin déclare.
 *
 * # Ce que ce fichier ajoute au précédent
 *
 * `actions.go` met en œuvre l'appel sortant : une cible saisie dans un
 * formulaire, un transport qui la juge, un jeton désigné. Ici, rien de tout cela.
 * Une action de plugin parle à **sa** frontière, celle que le plugin a déjà réglée
 * — et c'est ce qui rend les deux chemins irréductibles l'un à l'autre.
 *
 * Trois conséquences valent d'être nommées :
 *
 *   - **la cible n'est pas choisie**. Elle vient de la configuration du plugin,
 *     pas du geste. Aucune vérification d'adresse n'est donc faite ici : celle de
 *     `internal/webhook` existe parce qu'une URL vient d'un formulaire, et ce
 *     n'est pas le cas ;
 *   - **aucun secret n'est désigné**. Le jeton est celui du plugin, ouvert le
 *     temps de l'appel. Un geste ne peut pas nommer un autre jeton, et ne peut
 *     donc pas s'en servir pour faire sortir celui d'un voisin ;
 *   - **le plugin doit être allumé**. Un plugin éteint est un refus nommé, pas un
 *     appel silencieux : la configuration existe encore en base, et partir avec
 *     enverrait un statut au nom d'une intégration qu'on croit désactivée.
 */

// PluginActionStore est ce que les actions de plugin attendent de la base.
//
// Deux lectures et pas une de plus : la connexion résolue du plugin, et le projet
// — dont on ne veut que le dépôt. Une interface plus large inviterait ce runner à
// décider de choses qui ne lui appartiennent pas.
type PluginActionStore interface {
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
	GetProject(ctx context.Context, id int64) (store.Project, error)
}

// PluginActionRunner met en œuvre les actions déclarées par les plugins.
type PluginActionRunner struct {
	store   PluginActionStore
	secrets SecretOpener
	timeout time.Duration
	logger  *slog.Logger
}

func NewPluginActionRunner(
	settings PluginActionStore, secrets SecretOpener, timeout time.Duration, logger *slog.Logger,
) *PluginActionRunner {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PluginActionRunner{store: settings, secrets: secrets, timeout: timeout, logger: logger}
}

// Run met en œuvre un geste d'action de plugin.
//
// Rend toujours un identifiant de job nul, comme les actions internes : rien de
// tout cela ne passe par la file, et il n'y a rien à attendre.
func (r *PluginActionRunner) Run(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
) (*int64, error) {
	descriptor, spec, found := plugin.FindAction(geste.Ref)
	if !found {
		return nil, fmt.Errorf("aucun plugin ne déclare l'action %q", geste.Ref)
	}
	if run.ProjectID == nil {
		// Un plugin est réglé par projet. Un fait qui n'en désigne aucun n'a pas
		// de connexion à emprunter, et partir sur celle de l'instance choisirait
		// un dépôt au hasard.
		return nil, fmt.Errorf(
			"l'action %q porte sur un projet, et ce fait n'en désigne aucun", geste.Ref)
	}

	settings, err := r.store.ResolvedPlugin(ctx, *run.ProjectID, descriptor.ID)
	if err != nil {
		return nil, fmt.Errorf("réglages du plugin %q: %w", descriptor.ID, err)
	}
	if !settings.Enabled {
		return nil, fmt.Errorf(
			"le plugin %q est éteint pour ce projet : l'action %q ne part pas",
			descriptor.ID, geste.Ref)
	}

	config, err := openPluginSecrets(r.secrets, descriptor.ID, settings.Config)
	if err != nil {
		return nil, fmt.Errorf("connexion du plugin %q: %w", descriptor.ID, err)
	}

	project, err := r.store.GetProject(ctx, *run.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("projet de l'exécution: %w", err)
	}

	// Les gabarits sont liés **puis** revérifiés. Voir [verifierJeuxFermes] : un
	// jeu fermé validé avant interpolation ne dit rien de ce qui en sortira.
	params := lierParams(spec, geste.Params, run.Context)
	if err := verifierJeuxFermes(spec, geste.Params, params); err != nil {
		return nil, err
	}

	switch descriptor.ID {
	case "gitea":
		return nil, r.runGitea(ctx, run, geste.Ref, params, project, config)
	default:
		// Un plugin qui déclare une action sans que personne ne la mette en œuvre.
		// Refuser nommément vaut mieux que rendre « parti » : le registre et
		// l'exécution ont divergé, et le dire est la seule façon de s'en apercevoir.
		return nil, fmt.Errorf(
			"le plugin %q déclare %q, et rien ne l'exécute", descriptor.ID, geste.Ref)
	}
}

// Références complètes des actions du plugin Gitea.
const (
	giteaStatusRef  = "gitea.status"
	giteaCommentRef = "gitea.comment"
)

// lierParams remplace les gabarits par la charge du fait déclencheur.
func lierParams(
	spec plugin.ActionSpec, declares, contexte map[string]string,
) map[string]string {
	params := make(map[string]string, len(spec.Fields))
	for _, field := range spec.Fields {
		params[field.Name] = action.Expand(declares[field.Name], contexte)
	}
	return params
}

// verifierJeuxFermes refuse une valeur interpolée hors du jeu déclaré.
//
// # Pourquoi ce contrôle-ci ne pouvait pas se faire plus tôt
//
// À l'enregistrement, un champ à jeu fermé laisse passer un gabarit : ce qu'il
// vaudra dépend de la charge du fait, et le refuser obligerait à écrire l'état en
// dur. Mais la valeur **produite** n'a alors été vérifiée par personne.
//
// C'est un défaut constaté contre une vraie forge, et pas une précaution
// théorique. Le gabarit naturel — `state: {outcome}` — rend `succeeded`, parce
// que c'est le vocabulaire d'un fait ; la forge, elle, dit `success`. Gitea a
// **accepté** l'appel et stocké l'état tel quel : un statut hors de son propre
// vocabulaire, qu'aucune interface ne sait afficher, sans le moindre message.
//
// Un destinataire indulgent est le pire des cas : rien n'échoue, et rien ne
// marche. Le geste échoue donc ici, en nommant ce qui est admis.
//
// # Deux vocabulaires, et aucune traduction
//
// L'issue d'un fait et l'état d'un statut de forge ne se correspondent pas, et
// aucune table ne les rapproche : elle serait à tenir, et ne vaudrait que pour
// cette forge-ci. Une réaction qui veut refléter l'issue s'écrit avec un état en
// dur et un appariement sur `outcome` — ce que le modèle sait déjà faire.
func verifierJeuxFermes(spec plugin.ActionSpec, declares, params map[string]string) error {
	for _, field := range spec.Fields {
		if len(field.OneOf) == 0 {
			continue
		}
		value := strings.TrimSpace(params[field.Name])
		if value == "" || slices.Contains(field.OneOf, value) {
			continue
		}

		// Le gabarit d'origine est cité quand il en portait un : sans lui, « reçu
		// "succeeded" » n'apprend pas d'où la valeur vient.
		origine := strings.TrimSpace(declares[field.Name])
		if origine != value {
			return fmt.Errorf("%s vaut %q après interpolation de %q, "+
				"or le destinataire n'admet que %s",
				field.Label, value, origine, strings.Join(field.OneOf, ", "))
		}
		return fmt.Errorf("%s vaut %q, or le destinataire n'admet que %s",
			field.Label, value, strings.Join(field.OneOf, ", "))
	}
	return nil
}

// runGitea met en œuvre les actions de la forge.
func (r *PluginActionRunner) runGitea(
	ctx context.Context, run store.PlanRun, ref string,
	params map[string]string, project store.Project, config map[string]string,
) error {
	url := strings.TrimSpace(config["url"])
	token := strings.TrimSpace(config["token"])
	if url == "" || token == "" {
		// Nommé plutôt que laissé au client : « URL invalide » enverrait chercher
		// une faute de frappe là où c'est un réglage jamais saisi.
		return fmt.Errorf("forge non configurée: renseigner l'URL et le jeton du plugin \"gitea\"")
	}
	if strings.TrimSpace(project.Repository) == "" {
		return fmt.Errorf("le projet %q ne déclare aucun dépôt", project.Slug)
	}

	writer, err := gitea.NewWriter(gitea.Options{
		URL: url, Token: token, Repository: project.Repository, Timeout: r.timeout,
	})
	if err != nil {
		return err
	}

	switch ref {
	case giteaStatusRef:
		commit := params["commit"]
		if commit == "" {
			return fmt.Errorf("le commit visé est vide après interpolation : " +
				"le fait déclencheur ne porte pas la variable nommée")
		}
		if err := writer.SetCommitStatus(ctx, commit, params["state"],
			params["context"], params["description"], params["target_url"]); err != nil {
			return err
		}
		r.logger.Info("statut de commit posé",
			"execution", run.ID, "projet", project.Slug,
			"contexte", params["context"], "etat", params["state"])
		return nil

	case giteaCommentRef:
		raw := strings.TrimSpace(params["number"])
		number, err := strconv.Atoi(raw)
		if err != nil {
			// Le cas courant, et il vaut d'être dit ainsi : une réaction branchée
			// sur `release.built` n'a pas de `{number}`, le gabarit reste littéral,
			// et « invalide » sans citer la valeur enverrait chercher ailleurs.
			return fmt.Errorf("le numéro de demande de fusion vaut %q après "+
				"interpolation : ce fait porte-t-il bien la variable nommée ?", raw)
		}
		if err := writer.CommentOnPullRequest(ctx, number, params["body"]); err != nil {
			return err
		}
		r.logger.Info("commentaire posté",
			"execution", run.ID, "projet", project.Slug, "demande", number)
		return nil

	default:
		return fmt.Errorf("action %q inconnue du plugin \"gitea\"", ref)
	}
}
