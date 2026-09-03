package worker

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/gitea"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * La sonde des demandes de fusion.
 *
 * Celle qui a motivé la demande : « un plugin Gitea pourrait déclarer un
 * événement de création de PR lorsqu'il en détecte une nouvelle ».
 *
 * Elle ne persiste rien et ne déduplique rien — la boucle s'en charge (voir
 * `probes.go`). Ce qu'elle doit tenir, elle, tient en deux points :
 *
 *   - **la clé de déduplication**, dérivée du numéro de la demande dans son
 *     dépôt. Elle seule sait ce qui identifie ses faits, et la boucle relit une
 *     fenêtre de recouvrement à chaque passe ;
 *   - **la date du fait**, celle de la création de la demande, jamais celle de la
 *     lecture. Une sonde qui rattrape trois jours doit produire des faits datés de
 *     ces trois jours.
 */

// giteaPullProbe observe les demandes de fusion ouvertes d'un dépôt.
type giteaPullProbe struct {
	timeout time.Duration
}

// NewGiteaPullProbe construit la sonde des demandes de fusion.
func NewGiteaPullProbe(timeout time.Duration) Probe {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return giteaPullProbe{timeout: timeout}
}

func (p giteaPullProbe) PluginID() string { return "gitea" }

// Source nomme le curseur dans `sync_state`.
//
// Propre à la sonde et non au plugin : Gitea pourrait en porter d'autres — les
// tags posés, les demandes fermées — et un curseur partagé ferait que l'une
// avance celui de l'autre.
func (p giteaPullProbe) Source() string { return "gitea.pull_requests" }

// pullRequestOpened est le type de fait émis, préfixé comme le veut la règle.
const pullRequestOpened = "gitea.pull_request.opened"

// Poll rend les demandes ouvertes depuis le curseur.
func (p giteaPullProbe) Poll(
	ctx context.Context, project store.Project, config map[string]string, cursor string,
) ([]event.Event, string, error) {
	url := strings.TrimSpace(config["url"])
	token := strings.TrimSpace(config["token"])
	if url == "" || token == "" {
		// Nommé plutôt que laissé au client : « URL invalide » enverrait chercher
		// une faute de frappe là où c'est un réglage jamais saisi.
		return nil, "", fmt.Errorf(
			"forge non configurée: renseigner l'URL et le jeton du plugin %q", p.PluginID())
	}
	if strings.TrimSpace(project.Repository) == "" {
		return nil, "", fmt.Errorf("le projet ne déclare aucun dépôt")
	}

	client, err := gitea.New(gitea.Options{
		URL: url, Token: token, Repository: project.Repository, Timeout: p.timeout,
	})
	if err != nil {
		return nil, "", err
	}

	since := parseCursor(cursor)
	pulls, err := client.ListOpenPullRequests(ctx, since)
	if err != nil {
		return nil, "", err
	}

	facts := make([]event.Event, 0, len(pulls))
	newest := since
	for _, pull := range pulls {
		facts = append(facts, event.Event{
			Type:    pullRequestOpened,
			Subject: "#" + strconv.FormatInt(pull.Number, 10),
			Payload: map[string]string{
				"number":        strconv.FormatInt(pull.Number, 10),
				"title":         pull.Title,
				"author":        pull.Author,
				"source_branch": pull.SourceBranch,
				"target_branch": pull.TargetBranch,
				"url":           pull.URL,
			},
			// La date du **fait**, celle de l'ouverture de la demande.
			OccurredAt: pull.CreatedAt,
			// La clé porte le dépôt et le numéro : un numéro seul se répéterait
			// d'un dépôt à l'autre, et deux projets suivis se voleraient leurs
			// faits.
			DedupKey: pullDedupKey(project.Repository, pull.Number),
		})
		if pull.CreatedAt.After(newest) {
			newest = pull.CreatedAt
		}
	}

	return facts, formatCursor(newest, since), nil
}

// pullDedupKey identifie une demande de fusion.
//
// Le dépôt et le numéro, et rien d'autre : une demande ouverte est un fait unique,
// et la relire dix fois ne doit pas en produire dix.
func pullDedupKey(repository string, number int64) string {
	return pullRequestOpened + ":" + repository + ":" + strconv.FormatInt(number, 10)
}

// parseCursor lit le curseur, ou rend le zéro.
//
// Un curseur illisible est traité comme une absence : c'est la même règle que
// pour la synchronisation du suivi, et elle mène à une passe d'amorçage — qui
// n'émet rien — plutôt qu'à une reprise depuis un instant faux.
func parseCursor(cursor string) time.Time {
	if strings.TrimSpace(cursor) == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, cursor)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// formatCursor compose le curseur suivant.
//
// Il **n'avance jamais en arrière** : une passe qui ne voit rien de neuf rend le
// curseur précédent, plutôt qu'un zéro qui relancerait un amorçage et perdrait
// tout l'historique de déduplication.
func formatCursor(newest, previous time.Time) string {
	if newest.IsZero() {
		newest = previous
	}
	if newest.IsZero() {
		// Aucune demande, jamais. Le curseur porte l'instant de la lecture pour
		// que la passe suivante cesse d'être un amorçage : sans cela, un dépôt
		// sans demande de fusion resterait éternellement en amorçage, et la
		// première demande ouverte n'émettrait rien.
		newest = time.Now().UTC()
	}
	return newest.UTC().Format(time.RFC3339)
}
