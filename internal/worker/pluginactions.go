package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/action"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/gitea"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plugin"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
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
	descriptor, _, found := plugin.FindAction(geste.Ref)
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

	switch descriptor.ID {
	case "gitea":
		return nil, r.runGitea(ctx, run, geste, project, config)
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

// runGitea met en œuvre les actions de la forge.
func (r *PluginActionRunner) runGitea(
	ctx context.Context, run store.PlanRun, geste plan.Geste,
	project store.Project, config map[string]string,
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

	switch geste.Ref {
	case giteaStatusRef:
		commit := r.expand(geste, run, "commit")
		if commit == "" {
			return fmt.Errorf("le commit visé est vide après interpolation : " +
				"le fait déclencheur ne porte pas la variable nommée")
		}
		if err := writer.SetCommitStatus(ctx, commit,
			r.expand(geste, run, "state"),
			r.expand(geste, run, "context"),
			r.expand(geste, run, "description"),
			r.expand(geste, run, "target_url"),
		); err != nil {
			return err
		}
		r.logger.Info("statut de commit posé",
			"execution", run.ID, "projet", project.Slug,
			"contexte", geste.Params["context"], "etat", r.expand(geste, run, "state"))
		return nil

	case giteaCommentRef:
		raw := r.expand(geste, run, "number")
		number, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			// Le cas courant, et il vaut d'être dit ainsi : une réaction branchée
			// sur `release.built` n'a pas de `{number}`, le gabarit reste littéral,
			// et « invalide » sans citer la valeur enverrait chercher ailleurs.
			return fmt.Errorf("le numéro de demande de fusion vaut %q après "+
				"interpolation : ce fait porte-t-il bien la variable nommée ?", raw)
		}
		if err := writer.CommentOnPullRequest(ctx, number, r.expand(geste, run, "body")); err != nil {
			return err
		}
		r.logger.Info("commentaire posté",
			"execution", run.ID, "projet", project.Slug, "demande", number)
		return nil

	default:
		return fmt.Errorf("action %q inconnue du plugin \"gitea\"", geste.Ref)
	}
}

// expand lie un paramètre à la charge du fait déclencheur.
func (r *PluginActionRunner) expand(geste plan.Geste, run store.PlanRun, name string) string {
	return action.Expand(geste.Params[name], run.Context)
}
