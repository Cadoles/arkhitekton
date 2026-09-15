package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrSignoffNotFound signale une validation absente.
var ErrSignoffNotFound = errors.New("validation introuvable")

// Signoff est l'avis d'un intervenant sur une release dans un environnement.
//
// La validation porte sur le couple release-environnement : une release validée
// en qualif ne l'est pas en préproduction, et l'environnement qui passe à la
// version suivante ne doit pas hériter de l'avis rendu sur la précédente.
type Signoff struct {
	ReleaseTag    string
	EnvironmentID string
	Role          string
	// Status vaut `pass`, `warn` ou `fail`. L'attente est l'absence de ligne.
	Status    string
	Detail    string
	DecidedBy string
	DecidedAt time.Time
}

// ListSignoffs rend les validations d'une release, dans l'ordre du flux de
// promotion de son projet puis des rôles.
func (s *Store) ListSignoffs(ctx context.Context, releaseID int64) ([]Signoff, error) {
	rows, err := s.pool.Query(ctx,
		`select e.id, s.role, s.status, coalesce(s.detail, ''), s.decided_by, s.decided_at
		 from release_signoffs s
		 join environments e on e.project_id = s.project_id and e.id = s.environment_id
		 where s.release_id = $1
		 order by e.rank, s.role`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("validations de la release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var signoffs []Signoff
	for rows.Next() {
		var signoff Signoff
		if err := rows.Scan(&signoff.EnvironmentID, &signoff.Role, &signoff.Status,
			&signoff.Detail, &signoff.DecidedBy, &signoff.DecidedAt); err != nil {
			return nil, fmt.Errorf("validations de la release %d: %w", releaseID, err)
		}
		signoffs = append(signoffs, signoff)
	}
	return signoffs, rows.Err()
}

// SetSignoff enregistre l'avis d'un intervenant, ou remplace le sien.
//
// Le projet est celui de la release, lu en base : un avis ne peut viser que
// l'environnement du projet de la release. L'avis précédent n'est pas perdu : le
// journal d'audit conserve chaque décision.
func (s *Store) SetSignoff(ctx context.Context, releaseID int64, environmentID, role, status, detail, decidedBy string) error {
	_, err := s.pool.Exec(ctx,
		`insert into release_signoffs
		     (project_id, release_id, environment_id, role, status, detail, decided_by, decided_at)
		 values ((select project_id from releases where id = $1),
		         $1, $2, $3, $4, nullif($5, ''), $6, now())
		 on conflict (release_id, environment_id, role) do update
		 set status = excluded.status, detail = excluded.detail,
		     decided_by = excluded.decided_by, decided_at = excluded.decided_at`,
		releaseID, environmentID, role, status, detail, decidedBy)
	if err != nil {
		return fmt.Errorf("enregistrement de la validation: %w", err)
	}
	return nil
}

// DeleteSignoff retire un avis et remet le rôle en attente.
func (s *Store) DeleteSignoff(ctx context.Context, releaseID int64, environmentID, role string) error {
	tag, err := s.pool.Exec(ctx,
		`delete from release_signoffs
		 where release_id = $1 and environment_id = $2 and role = $3`,
		releaseID, environmentID, role)
	if err != nil {
		return fmt.Errorf("retrait de la validation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSignoffNotFound
	}
	return nil
}

// EnvironmentSignoffs rend les validations d'une release pour un seul
// environnement, dans l'ordre des rôles.
func (s *Store) EnvironmentSignoffs(ctx context.Context, releaseID int64, environmentID string) ([]Signoff, error) {
	rows, err := s.pool.Query(ctx,
		`select role, status, coalesce(detail, ''), decided_by, decided_at
		 from release_signoffs
		 where release_id = $1 and environment_id = $2
		 order by role`, releaseID, environmentID)
	if err != nil {
		return nil, fmt.Errorf("validations de l'environnement %s: %w", environmentID, err)
	}
	defer rows.Close()

	var signoffs []Signoff
	for rows.Next() {
		signoff := Signoff{EnvironmentID: environmentID}
		if err := rows.Scan(&signoff.Role, &signoff.Status, &signoff.Detail,
			&signoff.DecidedBy, &signoff.DecidedAt); err != nil {
			return nil, fmt.Errorf("validations de l'environnement %s: %w", environmentID, err)
		}
		signoffs = append(signoffs, signoff)
	}
	return signoffs, rows.Err()
}

// SignoffEnvironmentExists dit si l'environnement visé est déclaré pour le projet.
//
// La clé étrangère le garantirait, mais son échec remonte en erreur 500 alors que
// l'environnement inconnu est une faute de l'appelant.
func (s *Store) SignoffEnvironmentExists(ctx context.Context, projectID int64, environmentID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`select true from environments where project_id = $1 and id = $2`,
		projectID, environmentID).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("environnement %s: %w", environmentID, err)
	}
	return exists, nil
}
