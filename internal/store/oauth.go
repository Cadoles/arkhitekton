package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

/*
 * What the OAuth authorisation server keeps: the clients that registered, the requests waiting for
 * consent, the grants a person gave and their tokens.
 *
 * Tokens and codes arrive here as hashes: the caller hashes what it hands out, and only the hash is
 * ever written or looked up.
 */

var (
	// ErrOAuthNotFound covers an unknown, expired or already used client, request, code or token.
	ErrOAuthNotFound = errors.New("oauth: unknown or expired")
	// ErrOAuthReplay reports a refresh token that was already replaced: whoever holds it is not the
	// client the grant was last refreshed for. The grant is revoked when it is returned.
	ErrOAuthReplay = errors.New("oauth: refresh token replayed")
)

// OAuthClient is a client that registered itself.
type OAuthClient struct {
	ID           string
	Name         string
	RedirectURIs []string
	CreatedAt    time.Time
}

// CreateOAuthClient records a registration.
func (db *DB) CreateOAuthClient(ctx context.Context, client OAuthClient) error {
	_, err := db.pool.Exec(ctx,
		`insert into oauth_clients (id, name, redirect_uris) values ($1, $2, $3)`,
		client.ID, client.Name, client.RedirectURIs)
	if err != nil {
		return fmt.Errorf("registering an oauth client: %w", err)
	}
	return nil
}

// OAuthClient reads a client.
func (db *DB) OAuthClient(ctx context.Context, id string) (OAuthClient, error) {
	var client OAuthClient
	err := db.pool.QueryRow(ctx,
		`select id, name, redirect_uris, created_at from oauth_clients where id = $1`, id).
		Scan(&client.ID, &client.Name, &client.RedirectURIs, &client.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthClient{}, ErrOAuthNotFound
	}
	if err != nil {
		return OAuthClient{}, fmt.Errorf("reading an oauth client: %w", err)
	}
	return client, nil
}

// OAuthRequest is an authorisation request waiting for consent.
type OAuthRequest struct {
	ID            string
	ClientID      string
	ClientName    string
	RedirectURI   string
	State         string
	CodeChallenge string
	Scopes        []string
	ExpiresAt     time.Time
}

// CreateOAuthRequest records a request.
func (db *DB) CreateOAuthRequest(ctx context.Context, request OAuthRequest) error {
	_, err := db.pool.Exec(ctx,
		`insert into oauth_requests (id, client_id, redirect_uri, state, code_challenge, scopes, expires_at)
		 values ($1, $2, $3, $4, $5, $6, $7)`,
		request.ID, request.ClientID, request.RedirectURI, request.State, request.CodeChallenge,
		request.Scopes, request.ExpiresAt)
	if err != nil {
		return fmt.Errorf("recording an oauth request: %w", err)
	}
	return nil
}

const oauthRequestColumns = `r.id, r.client_id, c.name, r.redirect_uri, r.state, r.code_challenge, r.scopes, r.expires_at`

func scanOAuthRequest(row pgx.Row) (OAuthRequest, error) {
	var request OAuthRequest
	err := row.Scan(&request.ID, &request.ClientID, &request.ClientName, &request.RedirectURI,
		&request.State, &request.CodeChallenge, &request.Scopes, &request.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthRequest{}, ErrOAuthNotFound
	}
	if err != nil {
		return OAuthRequest{}, fmt.Errorf("reading an oauth request: %w", err)
	}
	return request, nil
}

// OAuthRequest reads a request that has not expired.
func (db *DB) OAuthRequest(ctx context.Context, id string, now time.Time) (OAuthRequest, error) {
	return scanOAuthRequest(db.pool.QueryRow(ctx,
		`select `+oauthRequestColumns+`
		   from oauth_requests r join oauth_clients c on c.id = r.client_id
		  where r.id = $1 and r.expires_at > $2`, id, now))
}

// TakeOAuthRequest removes a request and returns it, once: approving or denying it twice finds it
// gone the second time.
func (db *DB) TakeOAuthRequest(ctx context.Context, id string, now time.Time) (OAuthRequest, error) {
	return scanOAuthRequest(db.pool.QueryRow(ctx,
		`with taken as (delete from oauth_requests where id = $1 and expires_at > $2 returning *)
		 select `+oauthRequestColumns+` from taken r join oauth_clients c on c.id = r.client_id`, id, now))
}

// OAuthGrant is what a person allowed a client to do on their behalf.
type OAuthGrant struct {
	ID         int64
	ClientID   string
	ClientName string
	Subject    string
	Username   string
	Email      string
	// Groups are those of the session that gave the grant: group roles resolve on them.
	Groups    []string
	Scopes    []string
	CreatedAt time.Time
	LastUsed  *time.Time
	// AccessExpires is when the current access token lapses.
	AccessExpires *time.Time
}

// OAuthCode is an authorisation code, bound to its grant.
type OAuthCode struct {
	Hash          []byte
	GrantID       int64
	RedirectURI   string
	CodeChallenge string
	ExpiresAt     time.Time
}

// CreateOAuthGrant records a grant and the code that will hand out its first tokens.
func (db *DB) CreateOAuthGrant(ctx context.Context, grant OAuthGrant, code OAuthCode) (int64, error) {
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("recording an oauth grant: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	groups := grant.Groups
	if groups == nil {
		groups = []string{}
	}
	var id int64
	if err := tx.QueryRow(ctx,
		`insert into oauth_grants (client_id, subject, username, email, groups, scopes)
		 values ($1, $2, $3, $4, $5, $6) returning id`,
		grant.ClientID, grant.Subject, grant.Username, grant.Email, groups, grant.Scopes).Scan(&id); err != nil {
		return 0, fmt.Errorf("recording an oauth grant: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`insert into oauth_codes (hash, grant_id, redirect_uri, code_challenge, expires_at)
		 values ($1, $2, $3, $4, $5)`,
		code.Hash, id, code.RedirectURI, code.CodeChallenge, code.ExpiresAt); err != nil {
		return 0, fmt.Errorf("recording an oauth code: %w", err)
	}
	return id, tx.Commit(ctx)
}

// TakeOAuthCode removes a code and returns it with its client, once.
func (db *DB) TakeOAuthCode(ctx context.Context, hash []byte, now time.Time) (OAuthCode, string, error) {
	var code OAuthCode
	var clientID string
	err := db.pool.QueryRow(ctx,
		`with taken as (delete from oauth_codes where hash = $1 and expires_at > $2 returning *)
		 select t.hash, t.grant_id, t.redirect_uri, t.code_challenge, t.expires_at, g.client_id
		   from taken t join oauth_grants g on g.id = t.grant_id`, hash, now).
		Scan(&code.Hash, &code.GrantID, &code.RedirectURI, &code.CodeChallenge, &code.ExpiresAt, &clientID)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthCode{}, "", ErrOAuthNotFound
	}
	if err != nil {
		return OAuthCode{}, "", fmt.Errorf("reading an oauth code: %w", err)
	}
	return code, clientID, nil
}

// OAuthTokens are the hashes of a grant's new tokens.
type OAuthTokens struct {
	Access         []byte
	AccessExpires  time.Time
	Refresh        []byte
	RefreshExpires time.Time
}

// IssueOAuthTokens gives a grant its first tokens, after its code was exchanged.
func (db *DB) IssueOAuthTokens(ctx context.Context, grantID int64, tokens OAuthTokens) error {
	tag, err := db.pool.Exec(ctx,
		`update oauth_grants
		    set access_hash = $2, access_expires = $3, refresh_hash = $4, refresh_expires = $5,
		        last_used = now()
		  where id = $1`,
		grantID, tokens.Access, tokens.AccessExpires, tokens.Refresh, tokens.RefreshExpires)
	if err != nil {
		return fmt.Errorf("issuing oauth tokens: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrOAuthNotFound
	}
	return nil
}

const oauthGrantColumns = `g.id, g.client_id, c.name, g.subject, g.username, g.email, g.groups, g.scopes,
	g.created_at, g.last_used, g.access_expires`

func scanOAuthGrant(row pgx.Row) (OAuthGrant, error) {
	var grant OAuthGrant
	err := row.Scan(&grant.ID, &grant.ClientID, &grant.ClientName, &grant.Subject, &grant.Username,
		&grant.Email, &grant.Groups, &grant.Scopes, &grant.CreatedAt, &grant.LastUsed, &grant.AccessExpires)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthGrant{}, ErrOAuthNotFound
	}
	if err != nil {
		return OAuthGrant{}, fmt.Errorf("reading an oauth grant: %w", err)
	}
	return grant, nil
}

// RefreshOAuthGrant replaces a grant's tokens, given its current refresh token, in one statement:
// two replicas handed the same token cannot both succeed. A refresh token already replaced revokes
// the grant and answers [ErrOAuthReplay].
func (db *DB) RefreshOAuthGrant(
	ctx context.Context, clientID string, previous []byte, tokens OAuthTokens, now time.Time,
) (OAuthGrant, error) {
	grant, err := scanOAuthGrant(db.pool.QueryRow(ctx,
		`with refreshed as (
		   update oauth_grants
		      set previous_refresh = refresh_hash, refresh_hash = $3, refresh_expires = $4,
		          access_hash = $5, access_expires = $6, last_used = $7
		    where client_id = $1 and refresh_hash = $2 and refresh_expires > $7
		   returning *)
		 select `+oauthGrantColumns+` from refreshed g join oauth_clients c on c.id = g.client_id`,
		clientID, previous, tokens.Refresh, tokens.RefreshExpires, tokens.Access, tokens.AccessExpires, now))
	if !errors.Is(err, ErrOAuthNotFound) {
		return grant, err
	}

	tag, err := db.pool.Exec(ctx, `delete from oauth_grants where previous_refresh = $1`, previous)
	if err != nil {
		return OAuthGrant{}, fmt.Errorf("revoking a replayed oauth grant: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return OAuthGrant{}, ErrOAuthReplay
	}
	return OAuthGrant{}, ErrOAuthNotFound
}

// OAuthGrantByAccess reads the grant an access token belongs to, while the token is valid, and
// records its use at most once a minute.
func (db *DB) OAuthGrantByAccess(ctx context.Context, access []byte, now time.Time) (OAuthGrant, error) {
	grant, err := scanOAuthGrant(db.pool.QueryRow(ctx,
		`select `+oauthGrantColumns+`
		   from oauth_grants g join oauth_clients c on c.id = g.client_id
		  where g.access_hash = $1 and g.access_expires > $2`, access, now))
	if err != nil {
		return OAuthGrant{}, err
	}
	if grant.LastUsed == nil || now.Sub(*grant.LastUsed) > time.Minute {
		if _, err := db.pool.Exec(ctx,
			`update oauth_grants set last_used = $2 where id = $1`, grant.ID, now); err != nil {
			return OAuthGrant{}, fmt.Errorf("recording an oauth grant's use: %w", err)
		}
	}
	return grant, nil
}

// ListOAuthGrants lists the grants that hold tokens, newest first: a subject's, or everyone's when
// subject is empty.
func (db *DB) ListOAuthGrants(ctx context.Context, subject string) ([]OAuthGrant, error) {
	rows, err := db.pool.Query(ctx,
		`select `+oauthGrantColumns+`
		   from oauth_grants g join oauth_clients c on c.id = g.client_id
		  where g.refresh_hash is not null and ($1 = '' or g.subject = $1)
		  order by g.created_at desc, g.id desc`, subject)
	if err != nil {
		return nil, fmt.Errorf("listing oauth grants: %w", err)
	}
	defer rows.Close()

	var grants []OAuthGrant
	for rows.Next() {
		grant, err := scanOAuthGrant(rows)
		if err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

// DeleteOAuthGrant revokes a grant: a subject's, or anyone's when subject is empty.
func (db *DB) DeleteOAuthGrant(ctx context.Context, id int64, subject string) (OAuthGrant, error) {
	return scanOAuthGrant(db.pool.QueryRow(ctx,
		`with deleted as (delete from oauth_grants where id = $1 and ($2 = '' or subject = $2) returning *)
		 select `+oauthGrantColumns+` from deleted g join oauth_clients c on c.id = g.client_id`, id, subject))
}

// RevokeOAuthToken revokes the grant a token, access or refresh, belongs to. An unknown token is
// not an error: RFC 7009 answers the same either way.
func (db *DB) RevokeOAuthToken(ctx context.Context, clientID string, hash []byte) error {
	_, err := db.pool.Exec(ctx,
		`delete from oauth_grants where client_id = $1 and (access_hash = $2 or refresh_hash = $2)`,
		clientID, hash)
	if err != nil {
		return fmt.Errorf("revoking an oauth token: %w", err)
	}
	return nil
}

// PurgeOAuth removes what can no longer be used: expired requests and codes, grants whose refresh
// token lapsed or that never got tokens, and clients registered a day ago that hold no grant.
func (db *DB) PurgeOAuth(ctx context.Context, now time.Time) (int64, error) {
	var total int64
	for _, statement := range []string{
		`delete from oauth_requests where expires_at <= $1`,
		`delete from oauth_codes where expires_at <= $1`,
		`delete from oauth_grants
		  where refresh_expires <= $1
		     or (refresh_hash is null and created_at <= $1::timestamptz - interval '1 hour')`,
		`delete from oauth_clients c
		  where c.created_at <= $1::timestamptz - interval '1 day'
		    and not exists (select 1 from oauth_grants g where g.client_id = c.id)
		    and not exists (select 1 from oauth_requests r where r.client_id = c.id)`,
	} {
		tag, err := db.pool.Exec(ctx, statement, now)
		if err != nil {
			return total, fmt.Errorf("purging oauth records: %w", err)
		}
		total += tag.RowsAffected()
	}
	return total, nil
}
