package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/event"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugin"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/store"
)

/*
 * Les sondes : d'où viennent les faits d'un plugin.
 *
 * # Sonde plutôt que réception
 *
 * « Détecter » cache deux mécanismes très différents. Le dashboard interroge, ou
 * la forge pousse. La sonde a été retenue pour trois raisons qui tiennent
 * ensemble (doc 14 §14.4) :
 *
 *   - la machinerie existe — la synchronisation du suivi fait exactement cela
 *     depuis le lot C, et `sync_state` porte déjà `(source, project_id)` ;
 *   - la réception ajouterait un **point d'entrée public**, non authentifié par
 *     nature, qui demande une signature partagée et une fenêtre anti-rejeu : une
 *     décision d'une autre nature, pas un point d'extension ;
 *   - [B6] a déjà tranché ce débat pour le suivi.
 *
 * # Les trois obligations, tenues **ici** et non par chaque sonde
 *
 * Elles vivent dans la boucle, délibérément : une obligation confiée à chaque
 * réalisation est une obligation qu'une réalisation oubliera.
 *
 *  1. **La première passe n'émet rien.** Elle établit le curseur et s'arrête.
 *     Sans cela, activer une sonde sur un dépôt qui porte quatre cents demandes
 *     de fusion émet quatre cents faits, donc déclenche quatre cents plans, donc
 *     envoie quatre cents notifications. Une activation ne réveille pas le passé.
 *  2. **La clé de déduplication vient de la sonde.** Elle seule sait ce qui
 *     identifie ses faits — pour une demande de fusion, son numéro. La boucle
 *     relit une fenêtre de recouvrement à chaque passe ; sans clé stable, chaque
 *     passe renotifierait.
 *  3. **Un échec est isolé par projet.** Deux projets peuvent viser deux forges,
 *     et la panne de l'une ne dit rien de l'autre. C'est la règle que la
 *     synchronisation du suivi vient d'apprendre.
 */

// Schedule dit quand une sonde veut être interrogée.
//
// Déclaré par la sonde et non imposé par la boucle : le suivi bouge au rythme
// d'une saisie humaine et se relit toutes les cinq minutes, une forge se regarde
// plus souvent. Une cadence unique servirait mal les deux — et le plancher du
// déploiement reste au-dessus de tout (doc 14 §14.4).
type Schedule struct {
	// Interval est la période souhaitée. Relevée au plancher du déploiement : une
	// sonde ne peut pas demander à interroger une machine tierce plus souvent que
	// ce que l'exploitation a autorisé.
	Interval time.Duration
	// Eager demande une passe immédiate au démarrage.
	//
	// Faux par défaut, et c'est le bon défaut : une sonde qui ne fait que
	// constater n'a rien à rattraper, et un redémarrage en boucle martèlerait une
	// machine qui n'est pas la nôtre. Vrai pour une source qui **remplit un
	// écran** — celui des versions serait vide jusqu'au premier battement.
	Eager bool
}

// Probe observe une source extérieure et rend les faits qu'elle y voit.
//
// Une interface étroite : la sonde ne persiste **pas ses faits**, ne les
// déduplique pas, ne les date pas de son propre chef. Elle lit et rapporte ; la
// boucle décide de ce qui s'écrit et de ce qui déclenche.
//
// Ce qu'elle fait de ses **propres** données ne regarde pas la boucle. La
// synchronisation du suivi projette des versions et des demandes dans ses tables
// au passage : c'est son travail, et le lui interdire aurait demandé un second
// mécanisme pour faire tourner exactement la même boucle.
type Probe interface {
	// PluginID dit de quel plugin la sonde tient sa connexion et son activation.
	// Un plugin éteint — sur l'instance ou sur le projet — n'est pas interrogé.
	PluginID() string
	// Source nomme le curseur dans `sync_state`. Propre à la sonde et non au
	// plugin : un plugin peut en porter plusieurs, et un curseur partagé ferait
	// que l'une avance celui de l'autre.
	Source() string
	// Schedule dit à quelle cadence interroger, et s'il faut le faire d'emblée.
	Schedule() Schedule
	// Poll rend les faits survenus depuis le curseur, et le curseur suivant.
	//
	// Les faits portent leur **propre** clé de déduplication et leur **propre**
	// date — celle du fait, jamais celle de la lecture. Une sonde qui rattrape
	// trois jours d'histoire doit produire des faits datés de ces trois jours,
	// sinon tout paraît avoir eu lieu à l'instant et l'ordre est perdu.
	Poll(ctx context.Context, project store.Project, config map[string]string,
		cursor string) ([]event.Event, string, error)
}

// ProbeStore est ce que la boucle attend de la base.
type ProbeStore interface {
	ListProjects(ctx context.Context) ([]store.Project, error)
	ResolvedPlugin(ctx context.Context, projectID int64, pluginID string) (store.PluginSettings, error)
	GetSyncState(ctx context.Context, source string, projectID int64) (store.SyncState, error)
	RecordSyncSuccess(ctx context.Context, source string, projectID int64, cursor string) error
	RecordSyncFailure(ctx context.Context, source string, projectID int64, message string) error
	RecordEvent(ctx context.Context, fact event.Event) (bool, error)
}

// minProbeInterval est le plancher d'interrogation.
//
// La charge se compte : *projets × sondes × fréquence*, et le multiplicande est
// une machine qui n'est pas la nôtre. Une sonde qui interrogerait une forge toutes
// les dix secondes pour trente projets serait un déni de service involontaire.
const minProbeInterval = time.Minute

// ProbeRunner fait tourner les sondes de tous les projets.
type ProbeRunner struct {
	store   ProbeStore
	secrets SecretOpener
	probes  []Probe
	// dispatcher déclenche les réactions déclarées d'un fait neuf. Nil : les
	// faits sont constatés et personne n'y réagit.
	dispatcher *Dispatcher
	interval   time.Duration
	logger     *slog.Logger
	now        func() time.Time
}

// WithDispatcher branche l'aiguillage des faits vers les réactions déclarées.
func (r *ProbeRunner) WithDispatcher(dispatcher *Dispatcher) *ProbeRunner {
	r.dispatcher = dispatcher
	return r
}

func NewProbeRunner(
	probes ProbeStore, opener SecretOpener, interval time.Duration,
	logger *slog.Logger, sondes ...Probe,
) *ProbeRunner {
	if interval < minProbeInterval {
		interval = minProbeInterval
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ProbeRunner{
		store: probes, secrets: opener, probes: sondes,
		interval: interval, logger: logger, now: time.Now,
	}
}

// Run interroge les sondes jusqu'à annulation.
//
// Une boucle **par sonde**, chacune à sa cadence. Un tour commun obligerait la
// plus lente et la plus rapide à s'accorder, et le suivi — cinq minutes — ferait
// alors la loi d'une forge qu'on veut regarder plus souvent.
func (r *ProbeRunner) Run(ctx context.Context) error {
	if len(r.probes) == 0 {
		// Rien à interroger. On ne tourne pas dans le vide : la boucle rendrait
		// un tour de journal par intervalle pour ne rien faire.
		<-ctx.Done()
		return ctx.Err()
	}

	var pending sync.WaitGroup
	for _, probe := range r.probes {
		pending.Add(1)
		go func(probe Probe) {
			defer pending.Done()
			r.loop(ctx, probe)
		}(probe)
	}
	pending.Wait()
	return ctx.Err()
}

// loop fait tourner une sonde à sa cadence.
func (r *ProbeRunner) loop(ctx context.Context, probe Probe) {
	schedule := probe.Schedule()
	// Le plancher du déploiement l'emporte : une sonde déclare sa cadence
	// au-dessus de lui, jamais en dessous.
	interval := schedule.Interval
	if interval < r.interval {
		interval = r.interval
	}

	// Une passe immédiate seulement si la sonde la réclame. Une source qui remplit
	// un écran la veut ; une source qui ne fait que constater n'a rien à
	// rattraper, et l'attente évite qu'un redémarrage en boucle martèle une
	// machine tierce.
	if schedule.Eager {
		r.sweep(ctx, probe)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.sweep(ctx, probe)
		}
	}
}

// sweep passe une sonde sur tous les projets.
func (r *ProbeRunner) sweep(ctx context.Context, probe Probe) {
	projects, err := r.store.ListProjects(ctx)
	if err != nil {
		r.logger.Error("projets à sonder", "error", err)
		return
	}

	for _, project := range projects {
		// L'échec d'un projet **n'arrête pas** les suivants : deux projets peuvent
		// viser deux serveurs, et la panne de l'un ne dit rien de l'autre.
		if err := r.pollOne(ctx, probe, project); err != nil {
			r.logger.Error("sonde",
				"source", probe.Source(), "projet", project.Slug, "error", err)
			if recordErr := r.store.RecordSyncFailure(
				ctx, probe.Source(), project.ID, err.Error()); recordErr != nil {
				r.logger.Error("enregistrement de l'échec d'une sonde",
					"source", probe.Source(), "projet", project.Slug, "error", recordErr)
			}
		}
	}
}

// pollOne interroge une sonde pour un projet.
func (r *ProbeRunner) pollOne(ctx context.Context, probe Probe, project store.Project) error {
	settings, err := r.store.ResolvedPlugin(ctx, project.ID, probe.PluginID())
	if err != nil {
		if errors.Is(err, store.ErrPluginDisabled) {
			// Éteint sur l'instance ou sur le projet. Ce n'est pas une panne : le
			// signaler à chaque tour remplirait le journal d'un état voulu.
			return nil
		}
		return err
	}

	config, err := r.openSecrets(probe.PluginID(), settings.Config)
	if err != nil {
		return err
	}

	state, err := r.store.GetSyncState(ctx, probe.Source(), project.ID)
	if err != nil {
		return err
	}

	facts, cursor, err := probe.Poll(ctx, project, config, state.Cursor)
	if err != nil {
		return err
	}

	// **La première passe ne déclenche rien.**
	//
	// C'est la règle la moins évidente et la plus coûteuse à découvrir : sans
	// elle, activer une sonde sur un dépôt qui porte quatre cents demandes de
	// fusion enverrait quatre cents notifications. Une activation ne réveille pas
	// le passé.
	//
	// # Elle enregistre pourtant, et c'est une correction
	//
	// La passe d'amorçage **écrit** les faits, sans les aiguiller. Les jeter
	// aurait suffi tant que toute sonde filtrait sur son curseur — la sonde Gitea
	// ne rend que les demandes ouvertes depuis, et ne les revoit pas.
	//
	// Une sonde qui rend un **instantané** casse cette hypothèse : le suivi relit
	// toutes les versions à chaque passe, parce que l'API de Redmine ne les filtre
	// pas. Jetés à l'amorçage sans être retenus, ces faits seraient neufs à la
	// passe suivante — et la déclencheraient tous. « Une activation ne réveille
	// pas le passé » n'aurait été vrai que d'un tour.
	priming := state.Cursor == ""

	emitted := 0
	for _, fact := range facts {
		fact.ProjectID = &project.ID
		fact.Source = probe.PluginID()

		recorded, err := r.store.RecordEvent(ctx, fact)
		if err != nil {
			// Un fait qu'on n'a pas su écrire n'arrête pas les autres : le curseur
			// n'avance pas non plus, et la passe suivante le reverra.
			return fmt.Errorf("fait %q: %w", fact.Type, err)
		}
		if !recorded || priming {
			continue
		}
		emitted++
		// Sur un fait **neuf** seulement : la fenêtre de recouvrement fait revoir
		// les mêmes demandes à chaque passe, et déclencher sur chacune enverrait
		// une notification par tour.
		r.dispatcher.Dispatch(ctx, fact)
	}

	switch {
	case priming:
		r.logger.Info("sonde amorcée, aucune réaction déclenchée",
			"source", probe.Source(), "projet", project.Slug,
			"faits_retenus", len(facts),
			"raison", "une activation ne réveille pas le passé")
	case emitted > 0:
		r.logger.Info("faits constatés par sonde",
			"source", probe.Source(), "projet", project.Slug,
			"emis", emitted, "lus", len(facts))
	}
	return r.store.RecordSyncSuccess(ctx, probe.Source(), project.ID, cursor)
}

// openSecrets desselle les champs sensibles de la configuration d'un plugin.
//
// C'est le seul endroit où ils passent en clair, et ils n'en sortent pas : la
// carte rendue est consommée par la sonde le temps de son appel.
func (r *ProbeRunner) openSecrets(
	pluginID string, sealed map[string]string,
) (map[string]string, error) {
	return openPluginSecrets(r.secrets, pluginID, sealed)
}

// openPluginSecrets desselle la configuration d'un plugin, pour qui en a besoin.
//
// Partagée entre les sondes et les actions de plugin depuis que les deux
// atteignent la même frontière avec la même connexion. En écrire deux versions
// ferait deux endroits où décider ce qu'on fait d'un champ qu'on n'a pas su
// ouvrir — et le second finirait par le laisser passer vide.
func openPluginSecrets(
	opener SecretOpener, pluginID string, sealed map[string]string,
) (map[string]string, error) {
	config := make(map[string]string, len(sealed))
	for name, value := range sealed {
		if !secretField(pluginID, name) {
			config[name] = value
			continue
		}
		if opener == nil || !opener.Configured() {
			if strings.TrimSpace(value) == "" {
				continue
			}
			return nil, fmt.Errorf(
				"le champ %q est scellé et le scellement n'est pas configuré", name)
		}
		opened, err := opener.Open(value)
		if err != nil {
			// Le message ne cite pas la valeur, et pour cause : c'est celle qu'on
			// n'a pas su ouvrir.
			return nil, fmt.Errorf("champ %q illisible: %w", name, err)
		}
		config[name] = opened
	}
	return config, nil
}

// secretField dit si un champ d'un plugin porte une valeur scellée.
//
// Lu dans le registre plutôt qu'écrit ici : deux listes divergeraient au premier
// champ ajouté, et l'écart se manifesterait par une valeur scellée transmise
// telle quelle à une forge — qui la refuserait sur un message d'authentification
// ne disant pas que la valeur n'a pas été ouverte.
func secretField(pluginID, field string) bool {
	descriptor, err := plugin.Get(pluginID)
	if err != nil {
		return false
	}
	// Les deux portées : un champ sensible déclaré globalement l'est aussi quand
	// il est surchargé par un projet, et l'inverse est vrai.
	return descriptor.IsSecret(plugin.ScopeGlobal, field) ||
		descriptor.IsSecret(plugin.ScopeProject, field)
}
