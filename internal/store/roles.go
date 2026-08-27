package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// UserRole est un rôle attribué.
type UserRole struct {
	Subject  string
	Username string
	Role     string
	// ProjectID est la portée : nil quand le rôle vaut partout. Une même personne
	// peut donc apparaître plusieurs fois dans la liste — une ligne globale, une
	// par projet où une exception a été posée.
	ProjectID *int64
	// ProjectSlug et ProjectLabel nomment cette portée pour l'affichage. Vides
	// pour un rôle global.
	ProjectSlug  string
	ProjectLabel string
	// GrantedBy vaut `bootstrap` pour les comptes promus par la configuration au
	// démarrage. La distinction compte : ces rôles réapparaissent au redémarrage
	// si on les retire sans toucher à la configuration.
	GrantedBy string
	GrantedAt time.Time
	// LastSeen est la dernière fois que le rôle a servi. Nil pour un compte
	// habilité qui ne s'est jamais connecté — un rôle attribué à un identifiant
	// erroné se reconnaît ainsi.
	LastSeen *time.Time
	// Sessions est le nombre de sessions actives. Une révocation qui n'en ferme
	// aucune laisserait l'ancien rôle en vigueur jusqu'à expiration.
	Sessions int
}

// ErrRoleNotFound signale un rôle absent.
var ErrRoleNotFound = errors.New("rôle introuvable")

// ErrLastAdmin refuse le retrait du dernier administrateur.
var ErrLastAdmin = errors.New(
	"dernier administrateur: le retirer laisserait le dashboard sans personne pour attribuer de rôle")

// ListUserRoles rend les rôles attribués, avec leurs sessions actives.
func (db *DB) ListUserRoles(ctx context.Context) ([]UserRole, error) {
	rows, err := db.pool.Query(ctx,
		`select r.subject, coalesce(r.username, ''), r.role, r.granted_by, r.granted_at,
		        r.last_seen,
		        (select count(*) from sessions s
		         where s.subject = r.subject and s.expires_at > now()),
		        r.project_id, coalesce(p.slug, ''), coalesce(p.label, '')
		 from user_roles r
		 left join projects p on p.id = r.project_id
		 -- Les rôles globaux d'abord, puis ceux des projets : une personne peut
		 -- désormais apparaître plusieurs fois, et lire sa portée générale avant
		 -- ses exceptions est l'ordre dans lequel on raisonne.
		 --
		 -- Les plus privilégiés en tête de chaque groupe : c'est la liste qu'on
		 -- relit pour vérifier qui peut livrer.
		 order by (r.project_id is not null), coalesce(p.label, ''),
		     case r.role
		     when 'admin' then 0 when 'releaser' then 1
		     when 'operator' then 2 else 3 end,
		     coalesce(r.username, r.subject)`)
	if err != nil {
		return nil, fmt.Errorf("liste des rôles: %w", err)
	}
	defer rows.Close()

	var roles []UserRole
	for rows.Next() {
		var role UserRole
		if err := rows.Scan(&role.Subject, &role.Username, &role.Role,
			&role.GrantedBy, &role.GrantedAt, &role.LastSeen, &role.Sessions,
			&role.ProjectID, &role.ProjectSlug, &role.ProjectLabel); err != nil {
			return nil, fmt.Errorf("liste des rôles: %w", err)
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

// CountAdmins rend le nombre d'administrateurs.
func (db *DB) CountAdmins(ctx context.Context) (int, error) {
	var count int
	// Les globaux seuls : `admin` n'existe pas ailleurs — la base l'impose — mais
	// la condition est explicite plutôt que déduite d'une contrainte.
	err := db.pool.QueryRow(ctx,
		`select count(*) from user_roles where role = 'admin' and project_id is null`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("décompte des administrateurs: %w", err)
	}
	return count, nil
}

// RevokeRole retire le rôle d'un utilisateur et ferme ses sessions.
//
// Les deux en une transaction, et c'est le point important : retirer un rôle
// sans fermer les sessions laisse l'utilisateur exercer l'ancien jusqu'à
// expiration. Une rétrogradation qui ne prend pas effet est pire que pas de
// rétrogradation du tout — on croit avoir agi.
//
// Refuse le retrait du dernier administrateur. Sans ce garde-fou, un
// administrateur peut se retirer lui-même et laisser le dashboard sans personne
// pour attribuer quoi que ce soit : la seule sortie serait de modifier la
// configuration d'amorçage et de redémarrer.
func (db *DB) RevokeRole(ctx context.Context, subject string) error {
	return db.RevokeRoleIn(ctx, subject, nil)
}

// RevokeRoleIn retire le rôle d'une portée : celui d'un projet, ou le global.
//
// La portée est obligatoire dans le raisonnement même quand elle est nulle : un
// retrait qui effacerait toutes les lignes d'un sujet supprimerait, en voulant
// lui retirer un projet, le rôle global qui vaut partout ailleurs.
func (db *DB) RevokeRoleIn(ctx context.Context, subject string, projectID *int64) error {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("retrait du rôle de %q: %w", subject, err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var role string
	var err2 error
	if projectID == nil {
		err2 = transaction.QueryRow(ctx,
			`select role from user_roles where subject = $1 and project_id is null for update`,
			subject).Scan(&role)
	} else {
		err2 = transaction.QueryRow(ctx,
			`select role from user_roles where subject = $1 and project_id = $2 for update`,
			subject, *projectID).Scan(&role)
	}
	if err2 != nil {
		return ErrRoleNotFound
	}

	if role == "admin" {
		var admins int
		if err := transaction.QueryRow(ctx,
			// `admin` n'existe qu'en global — la base l'impose — mais la
			// condition est explicite : compter par mégarde un rôle de projet
			// laisserait retirer le dernier administrateur réel.
			`select count(*) from user_roles where role = 'admin' and project_id is null`).Scan(&admins); err != nil {
			return fmt.Errorf("retrait du rôle de %q: %w", subject, err)
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}

	if projectID == nil {
		_, err = transaction.Exec(ctx,
			`delete from user_roles where subject = $1 and project_id is null`, subject)
	} else {
		_, err = transaction.Exec(ctx,
			`delete from user_roles where subject = $1 and project_id = $2`, subject, *projectID)
	}
	if err != nil {
		return fmt.Errorf("retrait du rôle de %q: %w", subject, err)
	}
	if _, err := transaction.Exec(ctx, `delete from sessions where subject = $1`, subject); err != nil {
		return fmt.Errorf("fermeture des sessions de %q: %w", subject, err)
	}

	return transaction.Commit(ctx)
}

// SetRole attribue un rôle et ferme les sessions de l'intéressé.
//
// Les sessions sont fermées quel que soit le sens du changement. Une promotion
// pourrait s'en passer — le rôle est relu à chaque requête —, mais distinguer les
// deux cas demanderait de comparer les rangs ici, et l'oubli se paierait sur une
// rétrogradation, c'est-à-dire du mauvais côté.
//
// Refuse la rétrogradation du dernier administrateur, pour la même raison que
// [DB.RevokeRole].
func (db *DB) SetRole(ctx context.Context, subject, username, role, grantedBy string) error {
	return db.SetRoleIn(ctx, subject, username, role, nil, grantedBy)
}

// SetRoleIn attribue un rôle dans un projet, ou partout quand il est nil.
func (db *DB) SetRoleIn(
	ctx context.Context, subject, username, role string, projectID *int64, grantedBy string,
) error {
	transaction, err := db.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("attribution du rôle à %q: %w", subject, err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	// `admin` gouverne les comptes, les dépôts et la synchronisation — rien de
	// tout cela n'appartient à un projet. La base le refuse aussi ; le dire ici
	// rend le message lisible.
	if role == "admin" && projectID != nil {
		return fmt.Errorf("le rôle %q ne s'attribue pas par projet", role)
	}

	// Le rôle courant **de la même portée**. Lire le rôle global en accordant un
	// rôle de projet ferait croire à une rétrogradation qui n'a pas lieu, et
	// déclencherait la garde du dernier administrateur sur une attribution qui ne
	// touche pas au global.
	var current string
	if projectID == nil {
		err = transaction.QueryRow(ctx,
			`select role from user_roles where subject = $1 and project_id is null for update`,
			subject).Scan(&current)
	} else {
		err = transaction.QueryRow(ctx,
			`select role from user_roles where subject = $1 and project_id = $2 for update`,
			subject, *projectID).Scan(&current)
	}
	known := err == nil

	if known && current == "admin" && role != "admin" {
		var admins int
		if err := transaction.QueryRow(ctx,
			// `admin` n'existe qu'en global — la base l'impose — mais la
			// condition est explicite : compter par mégarde un rôle de projet
			// laisserait retirer le dernier administrateur réel.
			`select count(*) from user_roles where role = 'admin' and project_id is null`).Scan(&admins); err != nil {
			return fmt.Errorf("attribution du rôle à %q: %w", subject, err)
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}

	// La clause vise l'index partiel de la portée visée : les deux unicités sont
	// portées par des index partiels distincts, et `on conflict` exige de nommer
	// celui qu'il vise.
	conflict := "(subject) where project_id is null"
	if projectID != nil {
		conflict = "(subject, project_id) where project_id is not null"
	}

	_, err = transaction.Exec(ctx,
		`insert into user_roles (subject, username, role, project_id, granted_by, granted_at)
		 values ($1, nullif($2, ''), $3, $4, $5, now())
		 on conflict `+conflict+` do update
		 set role = excluded.role, granted_by = excluded.granted_by,
		     granted_at = excluded.granted_at,
		     username = coalesce(excluded.username, user_roles.username)`,
		subject, username, role, projectID, grantedBy)
	if err != nil {
		return fmt.Errorf("attribution du rôle à %q: %w", subject, err)
	}

	// Le rôle est relu à chaque requête, mais la session porte l'identité : la
	// fermer force une reconnexion et rend le changement immédiat et visible.
	if _, err := transaction.Exec(ctx, `delete from sessions where subject = $1`, subject); err != nil {
		return fmt.Errorf("fermeture des sessions de %q: %w", subject, err)
	}

	return transaction.Commit(ctx)
}
