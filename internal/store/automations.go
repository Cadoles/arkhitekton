package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/plan"
)

/*
 * L'automatisation d'une release.
 *
 * Une par projet : « que fait-on quand une release est déclarée ? » n'a qu'une
 * réponse, et deux en ouvriraient deux exécutions dont chacune ferait avancer
 * l'état de la release.
 *
 * # L'absence est une valeur
 *
 * Trois états, et non deux. Pas de ligne — personne n'a rien déclaré, et la
 * reprise du worker compose depuis `targets`. Une ligne au plan vide — quelqu'un
 * a vidé l'automatisation, et ce projet ne construit plus. Une ligne au plan
 * garni — le cas courant.
 *
 * Confondre les deux premiers ferait réapparaître au redémarrage une
 * automatisation qu'on vient d'effacer.
 */

// ErrNoAutomation says no automation was ever recorded.
//
// Distinct from an empty plan, and that is the whole point of this error: only that case
// allows the catch-up to compose one.
var ErrNoAutomation = errors.New("no automation declared")

// ReleaseAutomation is what a project runs when a release is declared.
type ReleaseAutomation struct {
	ProjectID int64
	Plan      plan.Plan
	UpdatedBy string
}

// ReleaseAutomation returns a project's automation.
//
// Returns [ErrNoAutomation] when no row exists — the state that allows a catch-up, and the
// only one.
func (db *DB) ReleaseAutomation(ctx context.Context, projectID int64) (ReleaseAutomation, error) {
	const query = `
		select project_id, plan, updated_by
		from release_automations where project_id = $1`

	var entry ReleaseAutomation
	var encoded []byte
	err := db.pool.QueryRow(ctx, query, projectID).
		Scan(&entry.ProjectID, &encoded, &entry.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReleaseAutomation{}, ErrNoAutomation
	}
	if err != nil {
		return ReleaseAutomation{}, fmt.Errorf("reading the automation: %w", err)
	}
	if err := json.Unmarshal(encoded, &entry.Plan); err != nil {
		return ReleaseAutomation{}, fmt.Errorf("plan illisible: %w", err)
	}
	return entry, nil
}

// SaveReleaseAutomation records a project's automation.
//
// A replacement and never a distinct creation: the key is the project, and telling the two
// apart would force the caller to know whether a row exists in order to choose its verb.
func (db *DB) SaveReleaseAutomation(
	ctx context.Context, entry ReleaseAutomation, actor string,
) error {
	encoded, err := json.Marshal(entry.Plan)
	if err != nil {
		return fmt.Errorf("plan illisible: %w", err)
	}

	const upsert = `
		insert into release_automations (project_id, plan, updated_by)
		values ($1, $2, $3)
		on conflict (project_id) do update set
		    plan = excluded.plan,
		    updated_by = excluded.updated_by,
		    updated_at = now()`

	if _, err := db.pool.Exec(ctx, upsert, entry.ProjectID, encoded, actor); err != nil {
		return fmt.Errorf("recording the automation: %w", err)
	}
	return nil
}

// ProjectsWithoutAutomation returns the projects that never had one.
//
// The catch-up's question, asked in one query: one project per missing row, and not every
// project filtered in Go. A project whose plan was emptied does not appear — it has a row,
// so somebody decided.
func (db *DB) ProjectsWithoutAutomation(ctx context.Context) ([]Project, error) {
	const query = `
		select p.id from projects p
		left join release_automations a on a.project_id = p.id
		where a.project_id is null
		order by p.id`

	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("projects without an automation: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The projects are read in full: the catch-up needs their targets, their Dockerfile and
	// their build context, and gathering them here spares the caller a second query per
	// project.
	projects := make([]Project, 0, len(ids))
	for _, id := range ids {
		project, err := db.GetProject(ctx, id)
		if err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	return projects, nil
}
