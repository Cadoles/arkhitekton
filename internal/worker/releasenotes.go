package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

/*
 * Détection des demandes qu'une release apporte.
 *
 * Deux conditions, et les deux comptent :
 *
 *   * la demande est **en revue** dans le suivi — son code est écrit et relu ;
 *   * une pull request **fusionnée** la cite, dans son titre ou l'un de ses
 *     messages de commit.
 *
 * Ni l'une ni l'autre ne suffit. Une demande en revue dont rien n'est fusionné
 * n'est pas dans l'image ; une PR fusionnée qui cite une demande encore en
 * développement désigne un travail partiel. Annoncer l'une ou l'autre au client
 * serait une note fausse, et une note fausse est pire qu'une note vide : elle se
 * lit comme une liste vérifiée.
 *
 * La détection tourne **à la création** de la release et ne se rejoue pas seule.
 * Ce qu'elle a trouvé devient une proposition que l'on complète ou corrige à la
 * main : rejouer effacerait ces ajustements.
 */

// PullReader est ce que la détection attend de la forge.
type PullReader interface {
	ListMergedPullRequests(ctx context.Context, since time.Time) ([]provider.PullRequest, error)
	LoadCommitMessages(ctx context.Context, pull *provider.PullRequest) error
}

// NoteStore est ce que la détection attend de la base.
type NoteStore interface {
	NoteCandidates(ctx context.Context, releaseID int64, statuses []string) (map[string]store.NoteEntry, error)
	LinkNoteIssue(ctx context.Context, releaseID int64, reference, source, pullRequest, actor string) error
}

// NoteBuilder compose la note d'une release à sa création.
type NoteBuilder struct {
	forge  PullReader
	store  NoteStore
	logger *slog.Logger
	// statuses énumère les statuts du suivi qui rendent une demande éligible.
	// Configurables : le vocabulaire appartient au client, qui peut renommer
	// « En revue » sans que ce code ait à changer.
	statuses []string
}

func NewNoteBuilder(
	forge PullReader, notes NoteStore, statuses []string, logger *slog.Logger,
) *NoteBuilder {
	return &NoteBuilder{forge: forge, store: notes, statuses: statuses, logger: logger}
}

// NoteResult dit ce que la détection a trouvé.
type NoteResult struct {
	// Linked est le nombre de demandes rattachées.
	Linked int
	// Pulls est le nombre de pull requests fusionnées examinées.
	Pulls int
	// Incomplete signale qu'au moins une PR n'a pas pu être lue entièrement. La
	// note est alors une proposition partielle, et le dire évite de la prendre
	// pour exhaustive.
	Incomplete bool
}

// Build rattache à la release les demandes détectées.
//
// `since` borne la lecture des PR : la date de création de la release
// précédente, quand elle existe. Sans elle, la détection remonte jusqu'à la
// borne du client de forge.
func (b *NoteBuilder) Build(
	ctx context.Context, releaseID int64, since time.Time, actor string,
) (NoteResult, error) {
	var result NoteResult

	candidates, err := b.store.NoteCandidates(ctx, releaseID, b.statuses)
	if err != nil {
		return result, err
	}
	// Aucune demande en revue : rien ne peut être détecté, et interroger la forge
	// pour le découvrir coûterait deux cents requêtes pour rien.
	if len(candidates) == 0 {
		return result, nil
	}

	pulls, err := b.forge.ListMergedPullRequests(ctx, since)
	if err != nil {
		return result, err
	}
	result.Pulls = len(pulls)

	for index := range pulls {
		pull := &pulls[index]

		// Les commits complètent le titre : la convention autorise le numéro dans
		// l'un ou l'autre. Un échec de lecture ne fait pas échouer la détection —
		// le titre reste exploitable — mais il est signalé, la note devenant
		// partielle.
		if err := b.forge.LoadCommitMessages(ctx, pull); err != nil {
			result.Incomplete = true
			b.logger.Warn("commits d'une pull request illisibles",
				"pull", pull.Number, "error", err)
		}

		texts := append([]string{pull.Title, pull.Body}, pull.Commits...)
		for _, reference := range release.ExtractIssueRefs(texts...) {
			if _, eligible := candidates[reference]; !eligible {
				continue
			}
			err := b.store.LinkNoteIssue(ctx, releaseID, reference,
				store.NoteSourceAuto, pull.Reference(), actor)
			switch {
			case errors.Is(err, store.ErrNoteAlreadyLinked):
				// Deux PR pour une même demande : c'est courant — un correctif
				// puis son suivi — et la première rattachée fait foi.
				continue
			case err != nil:
				return result, err
			}
			result.Linked++
		}
	}

	b.logger.Info("note de release composée",
		"release", releaseID, "demandes", result.Linked,
		"pull_requests", result.Pulls, "partielle", result.Incomplete)
	return result, nil
}

// KindReleaseNotes est la nature de job qui compose une note de release.
//
// Absente du catalogue : ce travail lit le suivi, la forge et la base, et n'a
// aucune raison de traverser Tekton. Il passe malgré tout par la file — comme
// les exports — pour son ordonnancement et sa trace : la composition interroge
// jusqu'à deux cents pull requests, et la faire dans la requête de création
// aurait fait attendre l'utilisateur devant un formulaire.
const KindReleaseNotes = "release-notes"

// Kind implémente LocalTask.
func (b *NoteBuilder) Kind() string { return KindReleaseNotes }

// Run compose la note du job.
//
// Les paramètres portent la release et la borne de lecture. Un échec est rendu à
// l'ordonnanceur, qui conclut le job : une note absente se voit à l'écran, et la
// relancer est un geste, pas une reprise silencieuse.
func (b *NoteBuilder) Run(ctx context.Context, job store.Job) error {
	if job.ReleaseID == nil {
		return errors.New("note de release: aucun identifiant de release")
	}

	var since time.Time
	if raw := job.Params["since"]; raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			// Une borne illisible n'empêche pas de composer : sans elle la lecture
			// remonte plus loin, ce qui est plus coûteux mais pas faux.
			b.logger.Warn("borne de lecture illisible", "valeur", raw, "error", err)
		} else {
			since = parsed
		}
	}

	_, err := b.Build(ctx, *job.ReleaseID, since, job.RequestedBy)
	return err
}
