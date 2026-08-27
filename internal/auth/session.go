package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Session est une session authentifiée, conservée côté serveur.
//
// Le navigateur ne détient qu'un identifiant opaque. C'est ce qui permet de
// révoquer une session — impossible avec un JWT auto-porté, qui reste valide
// jusqu'à son expiration quoi qu'il arrive. Le dashboard déclenche des
// promotions en production : la révocation immédiate doit exister.
type Session struct {
	ID        string
	Subject   string
	Username  string
	Email     string
	ExpiresAt time.Time
	LastUsed  time.Time
}

var (
	// ErrSessionNotFound couvre l'absence, l'expiration et la révocation. Les
	// distinguer côté client donnerait un oracle sur l'existence des sessions.
	ErrSessionNotFound = errors.New("session inconnue ou expirée")
	// ErrNoRole signale un utilisateur authentifié sans rôle attribué.
	ErrNoRole = errors.New("aucun rôle attribué")
)

// SessionStore gère les sessions et les rôles dans PostgreSQL.
type SessionStore struct {
	pool *pgxpool.Pool
	ttl  time.Duration
	now  func() time.Time
}

func NewSessionStore(pool *pgxpool.Pool, ttl time.Duration) *SessionStore {
	return &SessionStore{pool: pool, ttl: ttl, now: time.Now}
}

// Create ouvre une session pour l'utilisateur identifié.
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
		return Session{}, fmt.Errorf("création de session: %w", err)
	}

	// La date de dernière connexion rend praticable la revue périodique des
	// rôles : sans elle, un rôle `releaser` survit silencieusement au départ de
	// son titulaire.
	if _, err := s.pool.Exec(ctx,
		`update user_roles set last_seen = $2, username = coalesce($3, username) where subject = $1`,
		subject, now, nullable(username)); err != nil {
		return Session{}, fmt.Errorf("mise à jour de la dernière connexion: %w", err)
	}

	return session, nil
}

// Lookup rend la session et le rôle associés à un identifiant.
//
// L'expiration est filtrée en SQL plutôt qu'en Go : une session expirée ne doit
// pas pouvoir être retournée puis oubliée par un appelant distrait.
func (s *SessionStore) Lookup(ctx context.Context, id string) (Session, Role, error) {
	session, role, _, err := s.LookupRoles(ctx, id)
	return session, role, err
}

// LookupRoles rend la session, le rôle global, et les rôles par projet.
//
// La jointure ne peut plus rendre une ligne : un sujet peut porter un rôle
// global **et** des rôles de projet, et les fondre en une seule réponse ferait
// perdre celui qui ne s'applique pas partout.
//
// Deux requêtes plutôt qu'une jointure qui multiplierait la session par ses
// rôles : la session doit être lue même sans aucun rôle — l'utilisateur existe et
// doit apparaître dans l'écran d'administration en attente d'attribution.
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
		return Session{}, "", nil, fmt.Errorf("lecture de session: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`select role, project_id from user_roles where subject = $1`, session.Subject)
	if err != nil {
		return session, "", nil, fmt.Errorf("lecture des rôles: %w", err)
	}
	defer rows.Close()

	var global Role
	projects := map[int64]Role{}
	for rows.Next() {
		var raw string
		var projectID *int64
		if err := rows.Scan(&raw, &projectID); err != nil {
			return session, "", nil, fmt.Errorf("lecture des rôles: %w", err)
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
		return session, "", nil, fmt.Errorf("lecture des rôles: %w", err)
	}

	if global == "" && len(projects) == 0 {
		// Authentifié mais sans rôle : l'utilisateur existe et doit apparaître
		// dans l'écran d'administration en attente d'attribution, plutôt que de
		// recevoir une erreur opaque suivie d'un ticket.
		return session, "", nil, ErrNoRole
	}
	return session, global, projects, nil
}

// Touch prolonge la session et note son dernier usage.
func (s *SessionStore) Touch(ctx context.Context, id string) error {
	now := s.now().UTC()
	_, err := s.pool.Exec(ctx,
		`update sessions set last_used = $2, expires_at = $3 where id = $1`,
		id, now, now.Add(s.ttl))
	return err
}

// Revoke supprime une session : c'est la déconnexion.
func (s *SessionStore) Revoke(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `delete from sessions where id = $1`, id)
	return err
}

// RevokeSubject supprime toutes les sessions d'un utilisateur. Sert au retrait
// d'un rôle et à la réponse à incident.
func (s *SessionStore) RevokeSubject(ctx context.Context, subject string) error {
	_, err := s.pool.Exec(ctx, `delete from sessions where subject = $1`, subject)
	return err
}

// PurgeExpired supprime les sessions échues. Appelé périodiquement par le worker.
func (s *SessionStore) PurgeExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `delete from sessions where expires_at <= $1`, s.now().UTC())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GrantRole attribue ou remplace le rôle d'un utilisateur.
func (s *SessionStore) GrantRole(ctx context.Context, subject, username string, role Role, grantedBy string) error {
	return s.GrantRoleIn(ctx, subject, username, role, nil, grantedBy)
}

// GrantRoleIn attribue un rôle dans un projet, ou partout quand il est nil.
//
// Deux `on conflict` plutôt qu'un : les index qui portent l'unicité sont
// partiels — au plus un rôle global, au plus un par projet — et `on conflict`
// exige de nommer celui qu'il vise. Une clé composite `(subject, project_id)`
// aurait permis une seule clause, mais laissé passer plusieurs rôles globaux,
// `null` n'étant jamais égal à `null`.
func (s *SessionStore) GrantRoleIn(
	ctx context.Context, subject, username string, role Role, projectID *int64, grantedBy string,
) error {
	if !role.Valid() {
		return fmt.Errorf("rôle invalide %q", role)
	}
	// `admin` gouverne les comptes, les dépôts et la synchronisation — rien de
	// tout cela n'appartient à un projet. La base le refuse aussi ; le dire ici
	// rend le message lisible plutôt que de laisser remonter une contrainte.
	if role == RoleAdmin && projectID != nil {
		return fmt.Errorf("le rôle %q ne s'attribue pas par projet", role)
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

// ApplyBootstrap consomme le rôle déclaré pour ce compte, s'il en existe un.
//
// Rend le rôle attribué, ou une chaîne vide quand rien n'était en attente.
//
// `user_roles` est indexée par `subject`, l'identifiant que le fournisseur met
// dans le jeton — un nombre, chez Gitea. Il n'est connaissable qu'après la
// première connexion : une ligne écrite à l'avance viserait un sujet inexistant,
// et l'utilisateur réel se connecterait sans aucune permission. Le projet l'a
// fait, et l'a retiré. La déclaration porte donc l'intention, et la connexion la
// transforme en rôle.
//
// Les déclarations viennent de `bootstrap_grants`, posée **une fois** par
// l'amorçage — et non d'une configuration relue à chaque connexion. La
// différence est un pouvoir : une variable d'environnement toujours lue permet à
// qui peut l'éditer de se redonner un rôle indéfiniment, sans autre trace qu'une
// connexion. Une déclaration consommée ne repromeut plus personne.
//
// La ligne produite porte `granted_by = 'bootstrap'`, ce qui la rend
// reconnaissable dans l'écran d'administration.
func (s *SessionStore) ApplyBootstrap(ctx context.Context, subject, username string) (string, error) {
	// La correspondance accepte les deux formes, et l'ordre compte : le sujet
	// d'abord, immuable, puis le nom de compte, qui peut être réattribué dans
	// Gitea. Une déclaration par sujet doit l'emporter sur une homonymie.
	//
	// `claimed_at is null` est ce qui rend la réclamation unique : une
	// déclaration consommée ne repromeut personne, y compris le même compte
	// après qu'un administrateur l'a rétrogradé.
	//
	// Tout tient dans une requête. La répartir entre du SQL et du Go ouvrirait
	// une fenêtre entre la lecture et l'écriture, pendant laquelle deux
	// connexions simultanées réclameraient la même déclaration.
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
		-- Un rôle existant n'est jamais écrasé : un compte rétrogradé
		-- volontairement ne doit pas redevenir administrateur à sa prochaine
		-- connexion.
		--
		-- La clause vise l'index partiel des rôles **globaux** : l'amorçage n'en
		-- accorde pas d'autres, et un rôle de projet déjà posé ne doit pas
		-- empêcher l'attribution globale qu'une déclaration réclame.
		on conflict (subject) where project_id is null do nothing
		returning role`

	var role string
	err := s.pool.QueryRow(ctx, query, subject, username, s.now().UTC()).Scan(&role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Aucune déclaration en attente pour ce compte, ou un rôle existe
			// déjà. Ni l'un ni l'autre n'est une anomalie : c'est le cas de
			// toutes les connexions après la première.
			return "", nil
		}
		return "", fmt.Errorf("amorçage de %q: %w", subject, err)
	}
	return role, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
