package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/plan"
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

// ErrNoAutomation dit qu'aucune automatisation n'a jamais été enregistrée.
//
// Distinct d'un plan vide, et c'est tout l'objet de cette erreur : seul ce cas
// autorise la reprise à en composer une.
var ErrNoAutomation = errors.New("aucune automatisation déclarée")

// ReleaseAutomation est ce qu'un projet exécute quand une release est déclarée.
type ReleaseAutomation struct {
	ProjectID int64
	Plan      plan.Plan
	UpdatedBy string
}

// ReleaseAutomation rend l'automatisation d'un projet.
//
// Rend [ErrNoAutomation] quand aucune ligne n'existe — l'état qui autorise une
// reprise, et le seul.
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
		return ReleaseAutomation{}, fmt.Errorf("lecture de l'automatisation: %w", err)
	}
	if err := json.Unmarshal(encoded, &entry.Plan); err != nil {
		return ReleaseAutomation{}, fmt.Errorf("plan illisible: %w", err)
	}
	return entry, nil
}

// SaveReleaseAutomation enregistre l'automatisation d'un projet.
//
// Un remplacement et jamais une création distincte : la clé est le projet, et
// distinguer les deux obligerait l'appelant à savoir si une ligne existe pour
// choisir son verbe.
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
		return fmt.Errorf("enregistrement de l'automatisation: %w", err)
	}
	return nil
}

// ProjectsWithoutAutomation rend les projets qui n'en ont jamais eu.
//
// La question de la reprise, posée en une requête : un projet par ligne absente,
// et non tous les projets filtrés en Go. Un projet dont le plan a été vidé n'y
// figure pas — il a une ligne, donc quelqu'un a décidé.
func (db *DB) ProjectsWithoutAutomation(ctx context.Context) ([]Project, error) {
	const query = `
		select p.id from projects p
		left join release_automations a on a.project_id = p.id
		where a.project_id is null
		order by p.id`

	rows, err := db.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("projets sans automatisation: %w", err)
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

	// Les projets sont relus entiers : la reprise a besoin de leurs cibles, de
	// leur Dockerfile et de leur contexte de build, et les rassembler ici évite à
	// l'appelant une seconde requête par projet.
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
