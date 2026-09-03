package worker

import (
	"context"
	"fmt"
	"strings"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La sonde du suivi.
 *
 * Celle qui **existait avant le contrat**, et c'est tout son intérêt. Un contrat
 * de sonde écrit sur la seule sonde Gitea en aurait épousé la forme sans qu'on
 * s'en aperçoive ; écrit puis éprouvé sur une source qui tourne depuis des mois,
 * il a une chance d'être juste (doc 14 §14.11).
 *
 * # Ce que cette seconde réalisation a révélé
 *
 * Deux manques, tous deux corrigés dans `probes.go` :
 *
 *   - **la cadence était celle de la boucle**, pas de la sonde. Le suivi se relit
 *     toutes les cinq minutes — une saisie humaine ne va pas plus vite — et une
 *     forge se regarde plus souvent. [Schedule] laisse chacune déclarer la sienne,
 *     au-dessus du plancher du déploiement ;
 *   - **l'amorçage jetait ses faits au lieu de les retenir**. Cela suffisait tant
 *     qu'une sonde filtrait sur son curseur. Celle-ci rend un **instantané** —
 *     l'API de Redmine ne filtre pas les versions —, et les faits jetés seraient
 *     revenus neufs à la passe suivante, déclenchant tout ce que l'amorçage
 *     prétendait taire.
 *
 * # Ce que le contrat a absorbé sans se tordre
 *
 * Le reste : réglages du plugin par projet, descellement de la clé, curseur dans
 * `sync_state`, isolation des échecs. La synchronisation en portait sa propre
 * version, écrite six mois plus tôt ; il n'en reste rien ici.
 */

// redmineProbe adapte la synchronisation du suivi au contrat de sonde.
//
// Une enveloppe mince, et délibérément : la projection n'a pas bougé. Ce fichier
// ne fait que traduire ce que la boucle apporte — une configuration résolue et
// descellée — en ce que la projection attend, un client et un curseur.
type redmineProbe struct{ sync *TrackerSync }

// NewRedmineProbe construit la sonde du suivi.
func NewRedmineProbe(sync *TrackerSync) Probe { return redmineProbe{sync: sync} }

func (p redmineProbe) PluginID() string { return trackerTool }

// Source nomme le curseur dans `sync_state`, et garde son nom historique.
func (p redmineProbe) Source() string { return redmineSource }

// Schedule : cinq minutes, et une passe immédiate.
//
// La passe immédiate n'est pas un confort. Cette sonde **remplit un écran** —
// celui des versions —, qui serait vide pendant cinq minutes après chaque
// démarrage. C'est le seul cas où l'attente coûte plus que l'appel qu'elle évite,
// et c'est pour lui que [Schedule.Eager] existe.
func (p redmineProbe) Schedule() Schedule {
	return Schedule{Interval: syncInterval, Eager: true}
}

// Poll projette une passe et rend les faits qu'elle constate.
//
// La configuration arrive **descellée** : la boucle l'a ouverte, et c'est
// désormais le seul endroit où cela se fait. La clé n'existe en clair que le temps
// de construire le client, dans un processus qui n'expose aucun port.
func (p redmineProbe) Poll(
	ctx context.Context, project store.Project, config map[string]string, cursor string,
) ([]event.Event, string, error) {
	// L'identifiant du projet dans le suivi vient du réglage, et retombe sur la
	// colonne `tracker_project` : elle a précédé les plugins, elle est juste, et un
	// déploiement dont l'amorçage n'aurait pas tourné ne doit pas cesser de se
	// synchroniser pour autant.
	trackerProject := strings.TrimSpace(config["project"])
	if trackerProject == "" {
		trackerProject = strings.TrimSpace(project.TrackerProject)
	}
	if trackerProject == "" {
		// Nommé plutôt que passé sous silence. Avant la migration, un projet sans
		// suivi était simplement absent de la liste ; ici le plugin a été **allumé
		// pour ce projet**, ce qui est une intention. La taire laisserait un écran
		// des versions vide sans que rien ne dise pourquoi.
		return nil, "", fmt.Errorf(
			"le plugin %q est actif sur ce projet sans identifiant de projet dans le suivi",
			trackerTool)
	}

	client, err := p.sync.connect(TrackerConnection{
		URL:     strings.TrimSpace(config["url"]),
		APIKey:  config["api_key"],
		Project: trackerProject,
	})
	if err != nil {
		return nil, "", err
	}

	return p.sync.Observe(ctx, project.ID, trackerProject, client, cursor)
}
