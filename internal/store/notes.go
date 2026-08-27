package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

/*
 * Note de release : quelles demandes Redmine une release apporte.
 *
 * La table ne porte qu'un rattachement. Le sujet, le tracker et le statut
 * viennent de `issues` à la lecture, si bien qu'un sujet corrigé dans
 * Redmine apparaît sans qu'on ait rien à régénérer.
 */

// ErrNoteAlreadyLinked signale une demande déjà rattachée.
//
// Distinct d'une erreur d'écriture : rattacher deux fois est un geste
// d'utilisateur, pas une panne, et l'appelant doit pouvoir le dire plutôt que
// d'échouer.
var ErrNoteAlreadyLinked = errors.New("store: demande déjà rattachée à cette release")

// NoteEntry est une ligne de note de release.
type NoteEntry struct {
	// ExternalID est l'identifiant de la demande dans le suivi, en texte.
	ExternalID  string
	Source      string
	PullRequest string
	AddedBy     string
	// Les champs suivants viennent de `issues`, absents tant que la
	// synchronisation n'a pas vu la demande.
	Known      bool
	Tracker    string
	Subject    string
	StatusName string
	Anomaly    bool
}

const (
	NoteSourceAuto   = "auto"
	NoteSourceManual = "manual"
)

// LinkNoteIssue rattache une demande à la note d'une release.
func (db *DB) LinkNoteIssue(
	ctx context.Context, releaseID int64, reference, source, pullRequest, actor string,
) error {
	tag, err := db.pool.Exec(ctx,
		`insert into release_notes (release_id, external_id, source, pull_request, added_by)
		 values ($1, $2, $3, $4, $5)
		 on conflict (release_id, external_id) do nothing`,
		releaseID, reference, source, pullRequest, actor)
	if err != nil {
		return fmt.Errorf("rattachement de la demande %s: %w", reference, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoteAlreadyLinked
	}
	return nil
}

// UnlinkNoteIssue retire une demande de la note d'une release.
func (db *DB) UnlinkNoteIssue(ctx context.Context, releaseID int64, reference string) error {
	if _, err := db.pool.Exec(ctx,
		`delete from release_notes where release_id = $1 and external_id = $2`,
		releaseID, reference); err != nil {
		return fmt.Errorf("retrait de la demande %s: %w", reference, err)
	}
	return nil
}

// ListNoteEntries rend la note d'une release.
//
// Jointure lâche sur `issues` : une demande rattachée avant que la
// synchronisation ne l'ait vue reste listée, sans son sujet. La masquer ferait
// disparaître une ligne que quelqu'un a ajoutée, ce qui se lirait comme une
// perte.
func (db *DB) ListNoteEntries(ctx context.Context, releaseID int64) ([]NoteEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select n.external_id, n.source, n.pull_request, n.added_by,
		        i.external_id is not null,
		        coalesce(i.tracker, ''), coalesce(i.subject, ''),
		        coalesce(i.status_name, '')
		   from release_notes n
		   join releases r on r.id = n.release_id
		   -- Jointure bornee au projet de la release. Depuis la migration 0031
		   -- un identifiant n'est unique que dans son projet : joindre sur le
		   -- seul external_id ferait apparaitre le sujet d'une demande homonyme
		   -- d'un autre client, et dupliquerait la ligne. (Pas de backtick ici :
		   -- cette chaine est brute, un backtick la fermerait.)
		   left join issues i
		          on i.project_id = r.project_id and i.external_id = n.external_id
		  where n.release_id = $1
		  order by n.external_id`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("note de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var entries []NoteEntry
	for rows.Next() {
		var entry NoteEntry
		if err := rows.Scan(&entry.ExternalID, &entry.Source, &entry.PullRequest,
			&entry.AddedBy, &entry.Known, &entry.Tracker, &entry.Subject,
			&entry.StatusName); err != nil {
			return nil, fmt.Errorf("note de la release %d: %w", releaseID, err)
		}
		entry.Anomaly = isAnomaly(entry.Tracker)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// NoteCandidates rend les demandes éligibles à une note automatique.
//
// Le critère est le statut : une demande en revue a son code écrit et relu, ce
// qui est la condition pour qu'une PR la concernant ait pu être fusionnée. Le
// rapprochement avec les PR est fait par l'appelant, qui seul parle à la forge.
//
// Les statuts retenus sont passés en paramètre plutôt qu'écrits ici : ils
// viennent du vocabulaire du suivi du client, qui peut le renommer sans que ce
// code ait à changer.
//
// Le vivier est **borné au projet de la release**, et la borne passe par la
// release plutôt que par un identifiant de projet en paramètre : l'appelant a la
// release sous la main, et un paramètre qu'on peut oublier de passer rendrait
// candidate la demande d'un autre client portant le même numéro.
func (db *DB) NoteCandidates(
	ctx context.Context, releaseID int64, statuses []string,
) (map[string]NoteEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select i.external_id, coalesce(i.tracker, ''), coalesce(i.subject, ''),
		        coalesce(i.status_name, '')
		   from releases r
		   join issues i on i.project_id = r.project_id
		  where r.id = $1 and i.status_name = any($2)`, releaseID, statuses)
	if err != nil {
		return nil, fmt.Errorf("demandes en revue: %w", err)
	}
	defer rows.Close()

	candidates := map[string]NoteEntry{}
	for rows.Next() {
		var entry NoteEntry
		if err := rows.Scan(&entry.ExternalID, &entry.Tracker, &entry.Subject,
			&entry.StatusName); err != nil {
			return nil, fmt.Errorf("demandes en revue: %w", err)
		}
		entry.Known = true
		entry.Anomaly = isAnomaly(entry.Tracker)
		candidates[entry.ExternalID] = entry
	}
	return candidates, rows.Err()
}

// ListReleaseVersionIssues rend les demandes de la version d'une release.
//
// C'est le vivier dans lequel on puise pour compléter une note à la main : la
// demande qu'on cherche appartient presque toujours à la version de la release,
// et la retrouver par son numéro seul demandait de l'avoir sous les yeux dans
// Redmine.
//
// Les rejetées sont écartées : elles ne sont pas livrées, et les proposer au
// rattachement inviterait à annoncer au client ce qui a été abandonné.
func (db *DB) ListReleaseVersionIssues(ctx context.Context, releaseID int64) ([]NoteEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select i.external_id, coalesce(i.tracker, ''), coalesce(i.subject, ''),
		        coalesce(i.status_name, '')
		   from releases r
		   join issues i on i.version_id = r.version_id
		  where r.id = $1
		    and i.status_bucket <> $2
		  order by i.tracker, i.external_id`, releaseID, BucketRejected)
	if err != nil {
		return nil, fmt.Errorf("demandes de la version de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var entries []NoteEntry
	for rows.Next() {
		var entry NoteEntry
		if err := rows.Scan(&entry.ExternalID, &entry.Tracker, &entry.Subject,
			&entry.StatusName); err != nil {
			return nil, fmt.Errorf("demandes de la version de la release %d: %w", releaseID, err)
		}
		entry.Known = true
		entry.Anomaly = isAnomaly(entry.Tracker)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// ReleaseIDByTag rend la clé d'une release depuis son tag.
func (db *DB) ReleaseIDByTag(ctx context.Context, tag string) (int64, error) {
	var id int64
	if err := db.pool.QueryRow(ctx,
		`select id from releases where tag = $1`, tag).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrReleaseNotFound
		}
		return 0, fmt.Errorf("release %q: %w", tag, err)
	}
	return id, nil
}

// isAnomaly classe un tracker.
//
// Le préfixe et non l'égalité : Redmine porte `Anomalie`, `Anomalie bloquante`
// et d'autres déclinaisons, toutes des corrections. Même règle que la projection
// des versions, d'où la constante partagée.
func isAnomaly(tracker string) bool {
	return len(tracker) >= len(anomalyTrackerPrefix) &&
		tracker[:len(anomalyTrackerPrefix)] == anomalyTrackerPrefix
}
