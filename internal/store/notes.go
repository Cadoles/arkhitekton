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
 * viennent de `redmine_issues` à la lecture, si bien qu'un sujet corrigé dans
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
	RedmineID   int64
	Source      string
	PullRequest string
	AddedBy     string
	// Les champs suivants viennent de `redmine_issues`, absents tant que la
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
	ctx context.Context, releaseID, redmineID int64, source, pullRequest, actor string,
) error {
	tag, err := db.pool.Exec(ctx,
		`insert into release_notes (release_id, redmine_id, source, pull_request, added_by)
		 values ($1, $2, $3, $4, $5)
		 on conflict (release_id, redmine_id) do nothing`,
		releaseID, redmineID, source, pullRequest, actor)
	if err != nil {
		return fmt.Errorf("rattachement de la demande %d: %w", redmineID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoteAlreadyLinked
	}
	return nil
}

// UnlinkNoteIssue retire une demande de la note d'une release.
func (db *DB) UnlinkNoteIssue(ctx context.Context, releaseID, redmineID int64) error {
	if _, err := db.pool.Exec(ctx,
		`delete from release_notes where release_id = $1 and redmine_id = $2`,
		releaseID, redmineID); err != nil {
		return fmt.Errorf("retrait de la demande %d: %w", redmineID, err)
	}
	return nil
}

// ListNoteEntries rend la note d'une release.
//
// Jointure lâche sur `redmine_issues` : une demande rattachée avant que la
// synchronisation ne l'ait vue reste listée, sans son sujet. La masquer ferait
// disparaître une ligne que quelqu'un a ajoutée, ce qui se lirait comme une
// perte.
func (db *DB) ListNoteEntries(ctx context.Context, releaseID int64) ([]NoteEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select n.redmine_id, n.source, n.pull_request, n.added_by,
		        i.redmine_id is not null,
		        coalesce(i.tracker, ''), coalesce(i.subject, ''),
		        coalesce(i.status_name, '')
		   from release_notes n
		   left join redmine_issues i on i.redmine_id = n.redmine_id
		  where n.release_id = $1
		  order by n.redmine_id`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("note de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var entries []NoteEntry
	for rows.Next() {
		var entry NoteEntry
		if err := rows.Scan(&entry.RedmineID, &entry.Source, &entry.PullRequest,
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
// viennent du vocabulaire Redmine du client, qui peut le renommer sans que ce
// code ait à changer.
func (db *DB) NoteCandidates(ctx context.Context, statuses []string) (map[int64]NoteEntry, error) {
	rows, err := db.pool.Query(ctx,
		`select redmine_id, coalesce(tracker, ''), coalesce(subject, ''),
		        coalesce(status_name, '')
		   from redmine_issues
		  where status_name = any($1)`, statuses)
	if err != nil {
		return nil, fmt.Errorf("demandes en revue: %w", err)
	}
	defer rows.Close()

	candidates := map[int64]NoteEntry{}
	for rows.Next() {
		var entry NoteEntry
		if err := rows.Scan(&entry.RedmineID, &entry.Tracker, &entry.Subject,
			&entry.StatusName); err != nil {
			return nil, fmt.Errorf("demandes en revue: %w", err)
		}
		entry.Known = true
		entry.Anomaly = isAnomaly(entry.Tracker)
		candidates[entry.RedmineID] = entry
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
		`select i.redmine_id, coalesce(i.tracker, ''), coalesce(i.subject, ''),
		        coalesce(i.status_name, '')
		   from releases r
		   join issues i on i.version_id = r.version_id
		  where r.id = $1
		    and i.status_bucket <> $2
		  order by i.tracker, i.redmine_id`, releaseID, BucketRejected)
	if err != nil {
		return nil, fmt.Errorf("demandes de la version de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var entries []NoteEntry
	for rows.Next() {
		var entry NoteEntry
		if err := rows.Scan(&entry.RedmineID, &entry.Tracker, &entry.Subject,
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
