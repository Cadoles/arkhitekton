package store

import (
	"context"
	"errors"
	"testing"
)

// environmentsFor pose le flux de promotion. Les migrations ne l'amorcent pas :
// le référentiel vient du catalogue au démarrage, pas du schéma.
//
// Les rangs sont volontairement ceux de la production — c'est d'eux que dépend
// l'ordre de lecture vérifié plus bas.
func environmentsFor(t *testing.T, db *DB) {
	t.Helper()
	flow := []Environment{
		{ID: "unstable", Label: "Unstable", PromotionSuffix: "unstable", AcceptsPrerelease: true, Rank: 1, Namespace: "mse-unstable", Observable: true, Visible: true},
		{ID: "qualif", Label: "Qualification", PromotionSuffix: "testing", AcceptsPrerelease: true, Rank: 2, Namespace: "mse-qualif", Observable: true, Visible: true},
		{ID: "preprod", Label: "Préproduction", PromotionSuffix: "preprod", AcceptsPrerelease: true, Rank: 3, Namespace: "mse-preprod", Observable: true, Visible: true},
		{ID: "prod", Label: "Production", PromotionSuffix: "prod", AcceptsPrerelease: false, Rank: 4, Namespace: "mse-prod", Observable: true, Visible: true},
	}
	for _, environment := range flow {
		if err := db.UpsertEnvironment(context.Background(), environment); err != nil {
			t.Fatalf("environnement %s: %v", environment.ID, err)
		}
	}
}

// La propriété qui justifie la clé composée : une release validée en qualif ne
// l'est pas en préproduction. Attachée à l'environnement seul, comme le faisait la
// maquette, la validation survivrait au déploiement suivant.
func TestUneValidationEstPropreAuCoupleReleaseEnvironnement(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	environmentsFor(t, db)

	release := releaseFor(t, db, "1.8.0-rc1")
	autre := releaseFor(t, db, "1.8.0-rc2")

	if err := db.SetSignoff(ctx, release, "qualif", "Client", "pass", "", "vcarroy"); err != nil {
		t.Fatalf("validation: %v", err)
	}

	enPreprod, err := db.EnvironmentSignoffs(ctx, release, "preprod")
	if err != nil {
		t.Fatalf("lecture preprod: %v", err)
	}
	if len(enPreprod) != 0 {
		t.Errorf("la validation de qualif a fui en preprod: %+v", enPreprod)
	}

	surAutreRelease, err := db.EnvironmentSignoffs(ctx, autre, "qualif")
	if err != nil {
		t.Fatalf("lecture autre release: %v", err)
	}
	if len(surAutreRelease) != 0 {
		t.Errorf("la validation a fui sur une autre release: %+v", surAutreRelease)
	}
}

// Se prononcer à nouveau remplace son propre avis : changer d'avis après une
// correction est le cas normal en recette.
func TestSePrononcerANouveauRemplaceSonAvis(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	environmentsFor(t, db)

	release := releaseFor(t, db, "1.8.0-rc1")

	if err := db.SetSignoff(ctx, release, "qualif", "Client", "fail", "panier cassé", "vcarroy"); err != nil {
		t.Fatalf("refus: %v", err)
	}
	if err := db.SetSignoff(ctx, release, "qualif", "Client", "pass", "", "vcarroy"); err != nil {
		t.Fatalf("validation: %v", err)
	}

	signoffs, err := db.EnvironmentSignoffs(ctx, release, "qualif")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(signoffs) != 1 {
		t.Fatalf("%d avis pour un seul rôle", len(signoffs))
	}
	if signoffs[0].Status != "pass" {
		t.Errorf("avis %q, attendu pass", signoffs[0].Status)
	}
	if signoffs[0].Detail != "" {
		t.Errorf("le motif du refus a survécu à la validation: %q", signoffs[0].Detail)
	}
}

// Deux intervenants distincts cohabitent : c'est un tableau d'avis, pas un
// interrupteur.
func TestPlusieursRolesCohabitentSurUnMemeEnvironnement(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	environmentsFor(t, db)

	release := releaseFor(t, db, "1.8.0-rc1")

	if err := db.SetSignoff(ctx, release, "qualif", "Client", "pass", "", "vcarroy"); err != nil {
		t.Fatalf("client: %v", err)
	}
	if err := db.SetSignoff(ctx, release, "qualif", "Recette", "warn", "lenteur au panier", "autre"); err != nil {
		t.Fatalf("recette: %v", err)
	}

	signoffs, err := db.EnvironmentSignoffs(ctx, release, "qualif")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(signoffs) != 2 {
		t.Fatalf("%d avis, attendu 2", len(signoffs))
	}
	// L'ordre est celui des rôles.
	if signoffs[0].Role != "Client" || signoffs[1].Role != "Recette" {
		t.Errorf("ordre inattendu: %s puis %s", signoffs[0].Role, signoffs[1].Role)
	}
}

// Retirer n'est pas refuser : l'avis disparaît sans laisser de `fail` qui
// bloquerait la livraison.
func TestRetirerUnAvisLeFaitDisparaitre(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	environmentsFor(t, db)

	release := releaseFor(t, db, "1.8.0-rc1")
	if err := db.SetSignoff(ctx, release, "qualif", "Client", "fail", "erreur de release", "vcarroy"); err != nil {
		t.Fatalf("refus: %v", err)
	}

	if err := db.DeleteSignoff(ctx, release, "qualif", "Client"); err != nil {
		t.Fatalf("retrait: %v", err)
	}

	signoffs, err := db.EnvironmentSignoffs(ctx, release, "qualif")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(signoffs) != 0 {
		t.Errorf("%d avis subsistent après retrait", len(signoffs))
	}
}

// Retirer un avis absent est une erreur nommée, pour que l'API réponde 404 au lieu
// de laisser croire à un retrait qui n'a rien retiré.
func TestRetirerUnAvisAbsentEstUneErreurNommee(t *testing.T) {
	db := migrated(t)
	environmentsFor(t, db)
	release := releaseFor(t, db, "1.8.0-rc1")

	err := db.DeleteSignoff(context.Background(), release, "qualif", "Personne")
	if !errors.Is(err, ErrSignoffNotFound) {
		t.Errorf("erreur %v, attendu ErrSignoffNotFound", err)
	}
}

// L'ordre de lecture est celui du flux de promotion, et non l'ordre d'insertion :
// la lecture naturelle va d'unstable à la production.
func TestLesValidationsSuiventLOrdreDuFluxDePromotion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	environmentsFor(t, db)

	release := releaseFor(t, db, "1.8.0-rc1")

	// Insérées volontairement à l'envers du rang.
	for _, environment := range []string{"prod", "qualif", "unstable"} {
		if err := db.SetSignoff(ctx, release, environment, "Client", "pass", "", "vcarroy"); err != nil {
			t.Fatalf("validation %s: %v", environment, err)
		}
	}

	signoffs, err := db.ListSignoffs(ctx, release)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(signoffs) != 3 {
		t.Fatalf("%d avis, attendu 3", len(signoffs))
	}

	var ranks []string
	for _, signoff := range signoffs {
		ranks = append(ranks, signoff.EnvironmentID)
	}
	if ranks[0] != "unstable" || ranks[2] != "prod" {
		t.Errorf("ordre %v: attendu unstable en premier et prod en dernier", ranks)
	}
}

// Les validations partent avec la release : détachées, elles désigneraient une
// version qui n'existe plus.
func TestLesValidationsPartentAvecLaRelease(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	environmentsFor(t, db)

	release := releaseFor(t, db, "1.8.0-rc1")
	if err := db.SetSignoff(ctx, release, "qualif", "Client", "pass", "", "vcarroy"); err != nil {
		t.Fatalf("validation: %v", err)
	}

	if _, err := db.pool.Exec(ctx, `delete from releases where id = $1`, release); err != nil {
		t.Fatalf("suppression de la release: %v", err)
	}

	var remaining int
	if err := db.pool.QueryRow(ctx, `select count(*) from release_signoffs`).Scan(&remaining); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if remaining != 0 {
		t.Errorf("%d validation(s) orpheline(s)", remaining)
	}
}

// Un environnement inconnu est détecté avant l'insertion, pour répondre 404 avec
// son nom plutôt qu'une erreur 500 de clé étrangère.
func TestUnEnvironnementInconnuEstDetecte(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()
	environmentsFor(t, db)

	exists, err := db.SignoffEnvironmentExists(ctx, "qualif")
	if err != nil {
		t.Fatalf("environnement connu: %v", err)
	}
	if !exists {
		t.Error("qualif devrait être déclaré")
	}

	exists, err = db.SignoffEnvironmentExists(ctx, "nexiste-pas")
	if err != nil {
		t.Fatalf("environnement inconnu: %v", err)
	}
	if exists {
		t.Error("un environnement non déclaré ne doit pas être reconnu")
	}
}
