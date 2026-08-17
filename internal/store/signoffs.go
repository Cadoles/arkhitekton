package store

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
// La validation porte sur le couple release-environnement et non sur
// l'environnement seul. Une release validée en qualif ne l'est pas en
// préproduction, et l'environnement qui passe à la version suivante ne doit pas
// hériter de l'avis rendu sur la précédente.
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

// ListSignoffs rend les validations d'une release, tous environnements confondus.
//
// L'ordre suit le rang des environnements puis le rôle : la lecture naturelle est
// celle du flux de promotion, unstable d'abord, production ensuite.
func (db *DB) ListSignoffs(ctx context.Context, releaseID int64) ([]Signoff, error) {
	rows, err := db.pool.Query(ctx,
		`select e.id, s.role, s.status, coalesce(s.detail, ''), s.decided_by, s.decided_at
		 from release_signoffs s
		 join environments e on e.id = s.environment_id
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
// L'écrasement est délibéré et borné à l'auteur du rôle concerné : changer d'avis
// après une correction est le cas normal en recette. L'avis précédent n'est pas
// perdu pour autant, le journal d'audit conservant chaque décision — c'est lui, et
// non cette table, qui répond à « qui avait validé quoi, et quand ».
func (db *DB) SetSignoff(ctx context.Context, releaseID int64, environmentID, role, status, detail, decidedBy string) error {
	_, err := db.pool.Exec(ctx,
		`insert into release_signoffs
		     (release_id, environment_id, role, status, detail, decided_by, decided_at)
		 values ($1, $2, $3, $4, nullif($5, ''), $6, now())
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
//
// Retirer n'est pas refuser : un avis rendu par erreur, ou sur la mauvaise
// release, doit pouvoir disparaître sans laisser un `fail` qui bloquerait la
// livraison. Le retrait est tracé au journal comme le reste.
func (db *DB) DeleteSignoff(ctx context.Context, releaseID int64, environmentID, role string) error {
	tag, err := db.pool.Exec(ctx,
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
func (db *DB) EnvironmentSignoffs(ctx context.Context, releaseID int64, environmentID string) ([]Signoff, error) {
	rows, err := db.pool.Query(ctx,
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

// SignoffEnvironmentExists dit si l'environnement visé est déclaré.
//
// La clé étrangère le garantirait, mais son échec remonte en erreur 500 alors que
// l'environnement inconnu est une faute de l'appelant. Le vérifier avant permet de
// répondre 404 avec le nom fautif.
func (db *DB) SignoffEnvironmentExists(ctx context.Context, environmentID string) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx,
		`select true from environments where id = $1`, environmentID).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("environnement %s: %w", environmentID, err)
	}
	return exists, nil
}
