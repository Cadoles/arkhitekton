package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	var (
		session Session
		role    *string
	)

	err := s.pool.QueryRow(ctx,
		`select s.id, s.subject, coalesce(s.username, ''), coalesce(s.email, ''),
		        s.expires_at, coalesce(s.last_used, s.created_at), r.role
		   from sessions s
		   left join user_roles r on r.subject = s.subject
		  where s.id = $1 and s.expires_at > $2`,
		id, s.now().UTC()).
		Scan(&session.ID, &session.Subject, &session.Username, &session.Email,
			&session.ExpiresAt, &session.LastUsed, &role)

	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, "", ErrSessionNotFound
	}
	if err != nil {
		return Session{}, "", fmt.Errorf("lecture de session: %w", err)
	}

	if role == nil {
		// Authentifié mais sans rôle : l'utilisateur existe et doit apparaître
		// dans l'écran d'administration en attente d'attribution, plutôt que de
		// recevoir une erreur opaque suivie d'un ticket.
		return session, "", ErrNoRole
	}

	parsed, err := ParseRole(*role)
	if err != nil {
		return session, "", err
	}
	return session, parsed, nil
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
	if !role.Valid() {
		return fmt.Errorf("rôle invalide %q", role)
	}
	_, err := s.pool.Exec(ctx,
		`insert into user_roles (subject, username, role, granted_by, granted_at)
		 values ($1, $2, $3, $4, $5)
		 on conflict (subject) do update
		 set role = excluded.role, granted_by = excluded.granted_by,
		     granted_at = excluded.granted_at,
		     username = coalesce(excluded.username, user_roles.username)`,
		subject, nullable(username), string(role), grantedBy, s.now().UTC())
	return err
}

// ApplyBootstrap promeut administrateur un compte déclaré en configuration.
//
// Appelée **à la connexion**, et c'est tout le correctif. La version précédente
// insérait les lignes au démarrage, en prenant chaque identifiant de la
// configuration pour un `subject`. Or Gitea met son identifiant numérique dans le
// claim `sub` — `121` — là où un humain écrit naturellement `vcarroy` dans un
// fichier de configuration. Les deux ne se rencontraient jamais : le rôle était
// créé pour un sujet qui n'existait pas, et l'utilisateur réel se connectait sans
// aucune permission.
//
// Le sujet n'est connaissable qu'après la première connexion. La résolution y a
// donc lieu, en acceptant les deux formes : le `subject`, immuable et donc plus
// sûr pour qui le connaît, ou le nom de compte, seule valeur qu'on puisse écrire
// à l'avance.
//
// Un rôle existant n'est jamais écrasé — un compte rétrogradé volontairement ne
// doit pas redevenir administrateur à sa prochaine connexion.
//
// La correspondance par nom de compte est plus faible que par sujet : un nom peut
// être réattribué dans Gitea. Elle ne joue qu'en l'absence de tout rôle, donc au
// premier accès, et la ligne produite porte `granted_by = 'bootstrap'`, ce qui la
// rend reconnaissable dans l'écran d'administration.
func (s *SessionStore) ApplyBootstrap(ctx context.Context, subject, username string, identifiers []string) (bool, error) {
	matched := false
	for _, identifier := range identifiers {
		identifier = strings.TrimSpace(identifier)
		if identifier == "" {
			continue
		}
		if identifier == subject || (username != "" && strings.EqualFold(identifier, username)) {
			matched = true
			break
		}
	}
	if !matched {
		return false, nil
	}

	tag, err := s.pool.Exec(ctx,
		`insert into user_roles (subject, username, role, granted_by, granted_at)
		 values ($1, nullif($2, ''), 'admin', 'bootstrap', $3)
		 on conflict (subject) do nothing`,
		subject, username, s.now().UTC())
	if err != nil {
		return false, fmt.Errorf("amorçage de %q: %w", subject, err)
	}
	return tag.RowsAffected() > 0, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
