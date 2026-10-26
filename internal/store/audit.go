package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AuditEntry is a line of the audit log.
type AuditEntry struct {
	ID     int64
	Actor  string
	Action string
	Target string
	// Payload carries what distinguishes two occurrences of the same action: the tag pushed,
	// the environment targeted, a launch's parameters.
	Payload map[string]any
	At      time.Time
}

// Recorded actions. Named here rather than as free strings at the call site: a typo in an
// `action` makes the line impossible to find when it is looked for, that is, afterwards.
const (
	AuditReleaseCreate = "release.create"
	// Approving a release commits what will reach the user. It is recorded separately from
	// its creation: they are two distinct decisions, often taken by different people and days
	// apart.
	AuditReleaseRelease = "release.release"
	// A rebuild re-runs a pipeline on the same git reference: recording it separates "the
	// image was rebuilt" from "the code changed".
	AuditReleaseRebuild    = "release.rebuild"
	AuditReleaseVerify     = "release.verify"
	AuditReleaseNoteAdd    = "release.note.add"
	AuditReleaseNoteRemove = "release.note.remove"
	AuditReleasePromote    = "release.promote"
	AuditReleaseActions    = "release.actions"
	AuditJobEnqueue        = "job.enqueue"
	AuditJobCancel         = "job.cancel"
	// A queue's ceiling decides what the cluster accepts to run at once. Raising it commits
	// the machine, lowering it lengthens every verification: both deserve to be attributed.
	AuditQueueLimit = "queue.limit"
	// Declaring a repository says what the dashboard drives, and removing it takes that away.
	// The access check is recorded too: it is the trace that says since when a repository was
	// known to be unreachable.
	AuditProjectSet = "project.set"
	// Declaring a version by hand only happens on a project with no tracker: elsewhere the
	// tracking tool is authoritative. The record says who decided a scope existed, which no
	// synchronisation will say in its place.
	AuditVersionDeclare   = "version.declare"
	AuditProjectRemove    = "project.remove"
	AuditProjectProbe     = "project.probe"
	AuditArtifactGenerate = "artifact.generate"
	AuditBaselinePin      = "bench.baseline.pin"
	// Acceptance approval is the decision that authorises a delivery. Its history cannot live
	// in `release_signoffs`, where a change of mind replaces the previous one: the log is the
	// only place that answers "who had approved, before retracting".
	AuditSignoffSet   = "release.signoff"
	AuditSignoffClear = "release.signoff.clear"
	// Role changes are recorded like outgoing actions: they decide who can ship, and "who
	// promoted whom" must have an answer without consulting application logs.
	AuditRoleGrant  = "role.grant"
	AuditRoleRevoke = "role.revoke"
)

// Audit records an action.
//
// "Who promoted what, and when" must have an answer without consulting application logs.
//
// The error is returned but the caller need not treat it as a failure of its action: the
// log is a record, not a transaction. Refusing a promotion because recording it failed
// would be an availability regression for no gain — the promotion did happen.
func (db *DB) Audit(ctx context.Context, actor, action, target string, payload map[string]any) error {
	var encoded []byte
	if payload != nil {
		var err error
		encoded, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("journal d'audit %q: %w", action, err)
		}
	}

	_, err := db.pool.Exec(ctx,
		`insert into audit_log (actor, action, target, payload) values ($1, $2, $3, $4)`,
		actor, action, nullIfEmpty(target), encoded)
	if err != nil {
		return fmt.Errorf("journal d'audit %q: %w", action, err)
	}
	return nil
}

// ListAudit returns the log, newest first.
//
// An empty `target` returns the whole log; set, it returns one object's history — the two
// reads the indexes exist for.
func (db *DB) ListAudit(ctx context.Context, target string, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := db.pool.Query(ctx,
		`select id, actor, action, coalesce(target, ''), payload, at
		 from audit_log
		 where $1 = '' or target = $1
		 order by at desc, id desc
		 limit $2`, target, limit)
	if err != nil {
		return nil, fmt.Errorf("reading the audit log: %w", err)
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var entry AuditEntry
		var payload []byte
		if err := rows.Scan(&entry.ID, &entry.Actor, &entry.Action,
			&entry.Target, &payload, &entry.At); err != nil {
			return nil, fmt.Errorf("reading the audit log: %w", err)
		}
		if len(payload) > 0 {
			if err := json.Unmarshal(payload, &entry.Payload); err != nil {
				return nil, fmt.Errorf("reading the audit log: %w", err)
			}
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
