package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Session is an authenticated session, held server-side.
//
// The browser holds only an opaque identifier. That is what allows revoking a
// session — impossible with a self-contained JWT, which stays valid until it
// expires whatever happens. The dashboard triggers promotions to production:
// immediate revocation must exist.
type Session struct {
	ID        string
	Subject   string
	Username  string
	Email     string
	ExpiresAt time.Time
	LastUsed  time.Time
}

var (
	// ErrSessionNotFound covers absence, expiry and revocation. Distinguishing them
	// client-side would give an oracle on which sessions exist.
	ErrSessionNotFound = errors.New("unknown or expired session")
	// ErrNoRole reports an authenticated user with no role granted.
	ErrNoRole = errors.New("no role granted")
)

// SessionStore manages sessions and roles in PostgreSQL.
type SessionStore struct {
	pool *pgxpool.Pool
	ttl  time.Duration
	now  func() time.Time
}

func NewSessionStore(pool *pgxpool.Pool, ttl time.Duration) *SessionStore {
	return &SessionStore{pool: pool, ttl: ttl, now: time.Now}
}

// Create opens a session for the identified user.
func (s *SessionStore) Create(ctx context.Context, subject, username, email string) (Session, error) {
	id, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}

	now := s.now().UTC()
	session := Session{
		ID:        id,
		Subject:   subject,
		Username:  username,
		Email:     email,
		ExpiresAt: now.Add(s.ttl),
		LastUsed:  now,
	}

	_, err = s.pool.Exec(ctx,
		`insert into sessions (id, subject, username, email, expires_at, created_at, last_used)
		 values ($1, $2, $3, $4, $5, $6, $6)`,
		session.ID, session.Subject, session.Username, session.Email, session.ExpiresAt, now)
	if err != nil {
		return Session{}, fmt.Errorf("creating the session: %w", err)
	}

	// The last sign-in date makes periodic role review practicable: without it a
	// releaser role silently outlives its holder's departure.
	if _, err := s.pool.Exec(ctx,
		`update user_roles set last_seen = $2, username = coalesce($3, username) where subject = $1`,
		subject, now, nullable(username)); err != nil {
		return Session{}, fmt.Errorf("updating the last sign-in: %w", err)
	}

	return session, nil
}

// Lookup returns the session and role for an identifier.
//
// Expiry is filtered in SQL rather than in Go: an expired session must not be
// returnable and then forgotten by a distracted caller.
func (s *SessionStore) Lookup(ctx context.Context, id string) (Session, Role, error) {
	session, role, _, err := s.LookupRoles(ctx, id)
	return session, role, err
}

// LookupRoles returns the session, the global role, and the per-project roles.
//
// The join can no longer return one row: a subject may hold a global role and
// project roles, and merging them into one answer would lose whichever does not
// apply everywhere.
//
// Two queries rather than a join multiplying the session by its roles: the session
// must be read even with no role at all — the user exists and must appear in the
// administration screen, awaiting a grant.
func (s *SessionStore) LookupRoles(
	ctx context.Context, id string,
) (Session, Role, map[int64]Role, error) {
	var session Session

	err := s.pool.QueryRow(ctx,
		`select s.id, s.subject, coalesce(s.username, ''), coalesce(s.email, ''),
		        s.expires_at, coalesce(s.last_used, s.created_at)
		   from sessions s
		  where s.id = $1 and s.expires_at > $2`,
		id, s.now().UTC()).
		Scan(&session.ID, &session.Subject, &session.Username, &session.Email,
			&session.ExpiresAt, &session.LastUsed)

	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, "", nil, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, "", nil, fmt.Errorf("reading the session: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`select role, project_id from user_roles where subject = $1`, session.Subject)
	if err != nil {
		return session, "", nil, fmt.Errorf("reading the roles: %w", err)
	}
	defer rows.Close()

	var global Role
	projects := map[int64]Role{}
	for rows.Next() {
		var raw string
		var projectID *int64
		if err := rows.Scan(&raw, &projectID); err != nil {
			return session, "", nil, fmt.Errorf("reading the roles: %w", err)
		}
		parsed, err := ParseRole(raw)
		if err != nil {
			return session, "", nil, err
		}
		if projectID == nil {
			global = parsed
			continue
		}
		projects[*projectID] = parsed
	}
	if err := rows.Err(); err != nil {
		return session, "", nil, fmt.Errorf("reading the roles: %w", err)
	}

	if global == "" && len(projects) == 0 {
		// Authenticated but without a role: the user exists and must appear in the
		// administration screen awaiting a grant, rather than receive an opaque error
		// followed by a ticket.
		return session, "", nil, ErrNoRole
	}
	return session, global, projects, nil
}

// Touch extends the session and records its last use.
func (s *SessionStore) Touch(ctx context.Context, id string) error {
	now := s.now().UTC()
	_, err := s.pool.Exec(ctx,
		`update sessions set last_used = $2, expires_at = $3 where id = $1`,
		id, now, now.Add(s.ttl))
	return err
}

// Revoke deletes a session: this is signing out.
func (s *SessionStore) Revoke(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `delete from sessions where id = $1`, id)
	return err
}

// RevokeSubject deletes every session of a user. Used when removing a role and in
// incident response.
func (s *SessionStore) RevokeSubject(ctx context.Context, subject string) error {
	_, err := s.pool.Exec(ctx, `delete from sessions where subject = $1`, subject)
	return err
}

// PurgeExpired deletes lapsed sessions. Called periodically by the worker.
func (s *SessionStore) PurgeExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `delete from sessions where expires_at <= $1`, s.now().UTC())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GrantRole grants or replaces a user's role.
func (s *SessionStore) GrantRole(ctx context.Context, subject, username string, role Role, grantedBy string) error {
	return s.GrantRoleIn(ctx, subject, username, role, nil, grantedBy)
}

// GrantRoleIn grants a role in a project, or everywhere when it is nil.
//
// Two `on conflict` clauses rather than one: the indexes carrying uniqueness are
// partial — at most one global role, at most one per project — and `on conflict`
// requires naming the one it targets. A composite key would have allowed a single
// clause but let several global roles through, `null` never equalling `null`.
func (s *SessionStore) GrantRoleIn(
	ctx context.Context, subject, username string, role Role, projectID *int64, grantedBy string,
) error {
	if !role.Valid() {
		return fmt.Errorf("invalid role %q", role)
	}
	// `admin` governs accounts, repositories and synchronisation — none of which
	// belongs to a project. The database refuses it too; saying it here makes the
	// message readable rather than surfacing a constraint.
	if role == RoleAdmin && projectID != nil {
		return fmt.Errorf("role %q is not granted per project", role)
	}

	conflict := "(subject) where project_id is null"
	if projectID != nil {
		conflict = "(subject, project_id) where project_id is not null"
	}

	_, err := s.pool.Exec(ctx,
		`insert into user_roles (subject, username, role, project_id, granted_by, granted_at)
		 values ($1, $2, $3, $4, $5, $6)
		 on conflict `+conflict+` do update
		 set role = excluded.role, granted_by = excluded.granted_by,
		     granted_at = excluded.granted_at,
		     username = coalesce(excluded.username, user_roles.username)`,
		subject, nullable(username), string(role), projectID, grantedBy, s.now().UTC())
	return err
}

// ApplyBootstrap consumes the role declared for this account, if any, and returns
// the role granted or an empty string when nothing was pending.
//
// `user_roles` is keyed by `subject`, the identifier the provider puts in the
// token — a number, in Gitea's case. It is knowable only after the first sign-in: a
// row written in advance would target a non-existent subject, and the real user
// would sign in with no permission at all.
//
// The declarations come from `bootstrap_grants`, written once by the bootstrap —
// not from a configuration re-read on every sign-in. The difference is a power: a
// variable read every time lets whoever can edit it grant themselves a role
// indefinitely, with no trace but a sign-in. A consumed declaration promotes
// nobody again.
//
// The row produced carries `granted_by = 'bootstrap'`, which makes it
// recognisable in the administration screen.
func (s *SessionStore) ApplyBootstrap(ctx context.Context, subject, username string) (string, error) {
	// The match accepts both forms, and the order matters: the subject first, which is
	// immutable, then the account name, which can be reassigned. A declaration by
	// subject must beat a name collision.
	//
	// `claimed_at is null` is what makes the claim single-use: a consumed declaration
	// promotes nobody, including the same account after an administrator demoted it.
	//
	// It all fits in one query. Splitting it between SQL and Go would open a window
	// between the read and the write, during which two concurrent sign-ins would claim
	// the same declaration.
	const query = `
		with candidate as (
		    select identifier, role from bootstrap_grants
		     where claimed_at is null
		       and (identifier = $1 or ($2 <> '' and lower(identifier) = lower($2)))
		     order by (identifier = $1) desc
		     limit 1
		),
		claimed as (
		    update bootstrap_grants
		       set claimed_at = $3, claimed_by = $1
		     where identifier in (select identifier from candidate)
		    returning role
		)
		insert into user_roles (subject, username, role, project_id, granted_by, granted_at)
		select $1, nullif($2, ''), role, null, 'bootstrap', $3 from claimed
		-- An existing role is never overwritten: an account deliberately demoted
		-- must not become an administrator again at its next sign-in.
		--
		-- The clause targets the partial index of global roles: the bootstrap
		-- grants no others, and a project role already set must not block the
		-- global grant a declaration claims.
		on conflict (subject) where project_id is null do nothing
		returning role`

	var role string
	err := s.pool.QueryRow(ctx, query, subject, username, s.now().UTC()).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No declaration pending for this account, or a role already exists. Neither is an
			// anomaly: it is the case of every sign-in after the first.
			return "", nil
		}
		return "", fmt.Errorf("bootstrapping %q: %w", subject, err)
	}
	return role, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
