package store

import (
	"context"
	"errors"
	"testing"
)

func grantRole(t *testing.T, db *DB, subject, username, role string) {
	t.Helper()
	if err := db.SetRole(context.Background(), subject, username, role, "vcarroy"); err != nil {
		t.Fatalf("attribution à %s: %v", subject, err)
	}
}

func openSession(t *testing.T, db *DB, subject string) {
	t.Helper()
	_, err := db.pool.Exec(context.Background(),
		`insert into sessions (id, subject, username, expires_at)
		 values ($1, $2, $2, now() + interval '1 hour')`, "sess-"+subject, subject)
	if err != nil {
		t.Fatalf("session de %s: %v", subject, err)
	}
}

func sessionCount(t *testing.T, db *DB, subject string) int {
	t.Helper()
	var count int
	if err := db.pool.QueryRow(context.Background(),
		`select count(*) from sessions where subject = $1`, subject).Scan(&count); err != nil {
		t.Fatalf("décompte: %v", err)
	}
	return count
}

// Retirer un rôle sans fermer les sessions laisse l'utilisateur exercer l'ancien
// jusqu'à expiration. Une rétrogradation qui ne prend pas effet est pire que pas
// de rétrogradation : on croit avoir agi.
func TestRevokeClosesTheSessions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	grantRole(t, db, "alice", "alice", "admin")
	grantRole(t, db, "bob", "bob", "operator")
	openSession(t, db, "bob")

	if sessionCount(t, db, "bob") != 1 {
		t.Fatal("la session de départ manque")
	}

	if err := db.RevokeRole(ctx, "bob"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	if sessionCount(t, db, "bob") != 0 {
		t.Error("les sessions doivent être fermées avec le rôle")
	}

	roles, _ := db.ListUserRoles(ctx)
	for _, role := range roles {
		if role.Subject == "bob" {
			t.Error("le rôle subsiste")
		}
	}
}

// Un changement de rôle ferme aussi les sessions, quel que soit son sens :
// distinguer promotion et rétrogradation ferait payer l'oubli du mauvais côté.
func TestSetRoleClosesTheSessions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	grantRole(t, db, "bob", "bob", "operator")
	openSession(t, db, "bob")

	if err := db.SetRole(ctx, "bob", "bob", "viewer", "alice"); err != nil {
		t.Fatalf("rétrogradation: %v", err)
	}
	if sessionCount(t, db, "bob") != 0 {
		t.Error("les sessions doivent être fermées au changement de rôle")
	}
}

// Sans ce garde-fou, un administrateur se retire lui-même et laisse le dashboard
// sans personne pour attribuer quoi que ce soit : la seule sortie serait de
// modifier la configuration d'amorçage et de redémarrer.
func TestTheLastAdminCannotBeRemoved(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	grantRole(t, db, "alice", "alice", "admin")

	if err := db.RevokeRole(ctx, "alice"); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("erreur = %v, attendu ErrLastAdmin", err)
	}
	// Ni par retrait, ni par rétrogradation.
	if err := db.SetRole(ctx, "alice", "alice", "viewer", "alice"); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("rétrogradation = %v, attendu ErrLastAdmin", err)
	}

	// Le rôle est toujours là : le refus n'a rien laissé à moitié fait.
	admins, err := db.CountAdmins(ctx)
	if err != nil || admins != 1 {
		t.Errorf("administrateurs = %d, err = %v", admins, err)
	}
}

// Deux administrateurs : le retrait de l'un est légitime.
func TestPenultimateAdminCanBeRemoved(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	grantRole(t, db, "alice", "alice", "admin")
	grantRole(t, db, "carol", "carol", "admin")

	if err := db.RevokeRole(ctx, "carol"); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	admins, _ := db.CountAdmins(ctx)
	if admins != 1 {
		t.Errorf("administrateurs = %d, attendu 1", admins)
	}
}

func TestRevokeAnUnknownSubject(t *testing.T) {
	db := migrated(t)

	if err := db.RevokeRole(context.Background(), "inconnu"); !errors.Is(err, ErrRoleNotFound) {
		t.Errorf("erreur = %v, attendu ErrRoleNotFound", err)
	}
}

// La liste sert à vérifier qui peut livrer : les plus privilégiés d'abord, sans
// tri manuel.
func TestRolesAreListedByPrivilege(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	grantRole(t, db, "v", "victor", "viewer")
	grantRole(t, db, "a", "alice", "admin")
	grantRole(t, db, "o", "olivia", "operator")
	grantRole(t, db, "r", "remi", "releaser")

	roles, err := db.ListUserRoles(ctx)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	order := make([]string, 0, len(roles))
	for _, role := range roles {
		order = append(order, role.Role)
	}
	expected := []string{"admin", "releaser", "operator", "viewer"}
	for index := range expected {
		if order[index] != expected[index] {
			t.Errorf("ordre = %v, attendu %v", order, expected)
			break
		}
	}
}

// Un rôle attribué à un identifiant erroné ne se voit qu'à ceci : personne ne
// s'en est jamais servi.
func TestNeverSeenRoleIsRecognisable(t *testing.T) {
	db := migrated(t)

	grantRole(t, db, "faute-de-frappe", "", "operator")

	roles, _ := db.ListUserRoles(context.Background())
	for _, role := range roles {
		if role.Subject != "faute-de-frappe" {
			continue
		}
		if role.LastSeen != nil {
			t.Error("un rôle jamais employé ne doit pas porter de date d'usage")
		}
		if role.Sessions != 0 {
			t.Errorf("sessions = %d", role.Sessions)
		}
		return
	}
	t.Error("rôle introuvable")
}
