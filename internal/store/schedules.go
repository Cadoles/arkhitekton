package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/schedule"
)

/*
 * The declared schedules.
 *
 * As for reactions, this store validates only what the database checks: `internal/schedule` carries
 * the shape, the HTTP layer the references.
 */

// ErrScheduleNotFound reports a missing schedule.
var ErrScheduleNotFound = errors.New("schedule not found")

// ErrScheduleExists reports a name already taken within this project.
var ErrScheduleExists = errors.New("a schedule already carries this name in this project")

// RecordedSchedule is a schedule and what its firings left.
type RecordedSchedule struct {
	schedule.Schedule
	NextFireAt  *time.Time
	LastFiredAt *time.Time
	LastOutcome string
}

const scheduleColumns = `id, project_id, name, cron, timezone, ref, enabled, plan,
	next_fire_at, last_fired_at, last_outcome`

func scanSchedule(row scannable) (RecordedSchedule, error) {
	var (
		entry   RecordedSchedule
		encoded []byte
	)
	if err := row.Scan(&entry.ID, &entry.ProjectID, &entry.Name, &entry.Cron, &entry.Timezone,
		&entry.Ref, &entry.Enabled, &encoded, &entry.NextFireAt, &entry.LastFiredAt,
		&entry.LastOutcome); err != nil {
		return RecordedSchedule{}, err
	}
	if err := json.Unmarshal(encoded, &entry.Plan); err != nil {
		return RecordedSchedule{}, fmt.Errorf("plan of schedule %d unreadable: %w", entry.ID, err)
	}
	return entry, nil
}

func (db *DB) querySchedules(ctx context.Context, query string, args ...any) ([]RecordedSchedule, error) {
	rows, err := db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("schedules: %w", err)
	}
	defer rows.Close()

	var schedules []RecordedSchedule
	for rows.Next() {
		entry, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, entry)
	}
	return schedules, rows.Err()
}

// ListSchedules returns a project's schedules, by name, switched off ones included.
func (db *DB) ListSchedules(ctx context.Context, projectID int64) ([]RecordedSchedule, error) {
	return db.querySchedules(ctx, `select `+scheduleColumns+` from schedules
		where project_id = $1 order by lower(btrim(name))`, projectID)
}

// DueSchedules returns the switched-on schedules whose next firing has come.
func (db *DB) DueSchedules(ctx context.Context, now time.Time) ([]RecordedSchedule, error) {
	return db.querySchedules(ctx, `select `+scheduleColumns+` from schedules
		where enabled and next_fire_at <= $1 order by next_fire_at, id`, now)
}

// GetSchedule returns a project's schedule. The project bounds the read, as for a reaction.
func (db *DB) GetSchedule(ctx context.Context, projectID, id int64) (RecordedSchedule, error) {
	entry, err := scanSchedule(db.pool.QueryRow(ctx,
		`select `+scheduleColumns+` from schedules where project_id = $1 and id = $2`, projectID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return RecordedSchedule{}, ErrScheduleNotFound
	}
	return entry, err
}

// SaveSchedule creates a schedule, or replaces the one carrying this identifier, with its next
// firing; nil for a schedule switched off.
func (db *DB) SaveSchedule(
	ctx context.Context, entry schedule.Schedule, next *time.Time, actor string,
) (int64, error) {
	encoded, err := json.Marshal(entry.Plan)
	if err != nil {
		return 0, fmt.Errorf("unreadable plan: %w", err)
	}

	if entry.ID == 0 {
		var id int64
		err := db.pool.QueryRow(ctx, `
			insert into schedules (project_id, name, cron, timezone, ref, enabled, plan,
			                       next_fire_at, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			returning id`,
			entry.ProjectID, entry.Name, entry.Cron, entry.Timezone, entry.Ref, entry.Enabled,
			encoded, next, actor).Scan(&id)
		if err != nil {
			return 0, wrapScheduleWrite(err)
		}
		return id, nil
	}

	tag, err := db.pool.Exec(ctx, `
		update schedules
		set name = $3, cron = $4, timezone = $5, ref = $6, enabled = $7, plan = $8,
		    next_fire_at = $9, updated_at = now()
		where project_id = $1 and id = $2`,
		entry.ProjectID, entry.ID, entry.Name, entry.Cron, entry.Timezone, entry.Ref,
		entry.Enabled, encoded, next)
	if err != nil {
		return 0, wrapScheduleWrite(err)
	}
	if tag.RowsAffected() == 0 {
		return 0, ErrScheduleNotFound
	}
	return entry.ID, nil
}

// DeleteSchedule removes a project's schedule.
func (db *DB) DeleteSchedule(ctx context.Context, projectID, id int64) error {
	tag, err := db.pool.Exec(ctx,
		`delete from schedules where project_id = $1 and id = $2`, projectID, id)
	if err != nil {
		return fmt.Errorf("deleting schedule %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrScheduleNotFound
	}
	return nil
}

// ClaimTick moves a schedule past the tick it was due at, and says whether this call did.
//
// Conditional on the tick still being the one read: a schedule edited meanwhile, or a tick another
// worker already took, is not taken twice.
func (db *DB) ClaimTick(ctx context.Context, id int64, tick, next time.Time, outcome string) (bool, error) {
	tag, err := db.pool.Exec(ctx, `
		update schedules
		set next_fire_at = $3, last_fired_at = $2, last_outcome = $4
		where id = $1 and enabled and next_fire_at = $2`, id, tick, next, outcome)
	if err != nil {
		return false, fmt.Errorf("schedule %d: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// RecordTickOutcome says what a claimed tick gave.
func (db *DB) RecordTickOutcome(ctx context.Context, id int64, outcome string) error {
	if _, err := db.pool.Exec(ctx,
		`update schedules set last_outcome = $2 where id = $1`, id, outcome); err != nil {
		return fmt.Errorf("schedule %d: %w", id, err)
	}
	return nil
}

// PlanRunInProgress says whether a run of this trigger is still going.
func (db *DB) PlanRunInProgress(ctx context.Context, trigger string) (bool, error) {
	var running bool
	err := db.pool.QueryRow(ctx,
		`select exists (select 1 from plan_runs where trigger = $1 and state = 'running')`,
		trigger).Scan(&running)
	if err != nil {
		return false, fmt.Errorf("runs of %s: %w", trigger, err)
	}
	return running, nil
}

func wrapScheduleWrite(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrScheduleExists
	}
	return fmt.Errorf("recording a schedule: %w", err)
}
