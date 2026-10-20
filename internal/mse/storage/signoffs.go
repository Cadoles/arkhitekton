package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrSignoffNotFound reports a missing sign-off.
var ErrSignoffNotFound = errors.New("sign-off not found")

// Signoff is a participant's opinion on a release in an environment.
//
// The sign-off bears on the release-environment pair: a release validated in qualif is not
// validated in pre-production, and the environment that moves to the next version must not
// inherit the opinion given on the previous one.
type Signoff struct {
	ReleaseTag    string
	EnvironmentID string
	Role          string
	// Status is `pass`, `warn` or `fail`. Waiting is the absence of a row.
	Status    string
	Detail    string
	DecidedBy string
	DecidedAt time.Time
}

// ListSignoffs returns a release's sign-offs, in the order of its project's promotion flow then
// of the roles.
func (s *Store) ListSignoffs(ctx context.Context, releaseID int64) ([]Signoff, error) {
	rows, err := s.pool.Query(ctx,
		`select e.id, s.role, s.status, coalesce(s.detail, ''), s.decided_by, s.decided_at
		 from release_signoffs s
		 join environments e on e.project_id = s.project_id and e.id = s.environment_id
		 where s.release_id = $1
		 order by e.rank, s.role`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("sign-offs of release %d: %w", releaseID, err)
	}
	defer rows.Close()

	var signoffs []Signoff
	for rows.Next() {
		var signoff Signoff
		if err := rows.Scan(&signoff.EnvironmentID, &signoff.Role, &signoff.Status,
			&signoff.Detail, &signoff.DecidedBy, &signoff.DecidedAt); err != nil {
			return nil, fmt.Errorf("sign-offs of release %d: %w", releaseID, err)
		}
		signoffs = append(signoffs, signoff)
	}
	return signoffs, rows.Err()
}

// SetSignoff records a participant's opinion, or replaces their own.
//
// The project is the release's, read from the database: an opinion can target only the
// environment of the release's project. The previous opinion is not lost: the audit log keeps
// every decision.
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
		return fmt.Errorf("recording the sign-off: %w", err)
	}
	return nil
}

// DeleteSignoff removes an opinion and puts the role back to waiting.
func (s *Store) DeleteSignoff(ctx context.Context, releaseID int64, environmentID, role string) error {
	tag, err := s.pool.Exec(ctx,
		`delete from release_signoffs
		 where release_id = $1 and environment_id = $2 and role = $3`,
		releaseID, environmentID, role)
	if err != nil {
		return fmt.Errorf("removing the sign-off: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSignoffNotFound
	}
	return nil
}

// EnvironmentSignoffs returns a release's sign-offs for a single environment, in the order of
// the roles.
func (s *Store) EnvironmentSignoffs(ctx context.Context, releaseID int64, environmentID string) ([]Signoff, error) {
	rows, err := s.pool.Query(ctx,
		`select role, status, coalesce(detail, ''), decided_by, decided_at
		 from release_signoffs
		 where release_id = $1 and environment_id = $2
		 order by role`, releaseID, environmentID)
	if err != nil {
		return nil, fmt.Errorf("sign-offs of environment %s: %w", environmentID, err)
	}
	defer rows.Close()

	var signoffs []Signoff
	for rows.Next() {
		signoff := Signoff{EnvironmentID: environmentID}
		if err := rows.Scan(&signoff.Role, &signoff.Status, &signoff.Detail,
			&signoff.DecidedBy, &signoff.DecidedAt); err != nil {
			return nil, fmt.Errorf("sign-offs of environment %s: %w", environmentID, err)
		}
		signoffs = append(signoffs, signoff)
	}
	return signoffs, rows.Err()
}

// SignoffEnvironmentExists says whether the environment targeted is declared for the project.
//
// The foreign key would guarantee it, but its failure surfaces as a 500 whereas an unknown
// environment is a fault of the caller.
func (s *Store) SignoffEnvironmentExists(ctx context.Context, projectID int64, environmentID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`select true from environments where project_id = $1 and id = $2`,
		projectID, environmentID).Scan(&exists)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("environment %s: %w", environmentID, err)
	}
	return exists, nil
}
