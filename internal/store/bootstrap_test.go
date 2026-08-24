package store

import (
	"context"
	"testing"
)

/*
 * L'amorçage.
 *
 * Ce qui doit être tenu : une étape appliquée n'est jamais rejouée, même si la
 * configuration a changé. C'est ce qui rend la base seule maîtresse — un dépôt
 * retiré de l'interface ne doit pas revenir au redéploiement suivant, et un rôle
 * retiré à quelqu'un ne doit pas se réattribuer.
 */

func spec() BootstrapSpec {
	return BootstrapSpec{
		Grants: []Grant{
			{Identifier: "vcarroy", Role: "admin"},
			{Identifier: "alice", Role: "releaser"},
		},
		Projects: []Project{
			{
				Repository: "CNOUS/mse", Label: "Application",
				ForgeURL: "https://forge.example.fr", BuildContext: ".",
				CreatedBy: "configuration",
			},
		},
	}
}

func TestLamorcageNeSappliqueQuUneFois(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	report, err := db.Bootstrap(ctx, spec())
	if err != nil {
		t.Fatalf("amorçage: %v", err)
	}
	if report.GrantsApplied != 2 || report.ProjectsApplied != 1 {
		t.Fatalf("rapport = %+v", report)
	}
	if len(report.Skipped) != 0 {
		t.Errorf("étapes ignorées au premier passage: %v", report.Skipped)
	}

	// La base prend la main : un administrateur retire un dépôt et une
	// déclaration.
	projects, _ := db.ListProjects(ctx)
	if err := db.DeleteProject(ctx, projects[0].ID); err != nil {
		t.Fatalf("suppression: %v", err)
	}

	// Le redéploiement rejoue l'amorçage. Rien ne doit revenir.
	report, err = db.Bootstrap(ctx, spec())
	if err != nil {
		t.Fatalf("second amorçage: %v", err)
	}
	if report.GrantsApplied != 0 || report.ProjectsApplied != 0 {
		t.Errorf("rapport = %+v: une étape a été rejouée", report)
	}
	if len(report.Skipped) != 2 {
		t.Errorf("étapes ignorées = %v, attendu les deux", report.Skipped)
	}

	remaining, _ := db.ListProjects(ctx)
	if len(remaining) != 0 {
		t.Errorf("%d dépôt(s) ressuscités par l'amorçage", len(remaining))
	}
}

// Une déclaration ajoutée après coup à la configuration n'a plus d'effet : c'est
// le prix de « la base fait foi », et il vaut mieux qu'une variable
// d'environnement qui redonne un rôle indéfiniment.
func TestUneDeclarationAjouteeApresCoupNestPasReprise(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.Bootstrap(ctx, spec()); err != nil {
		t.Fatalf("amorçage: %v", err)
	}

	étendu := spec()
	étendu.Grants = append(étendu.Grants, Grant{Identifier: "bob", Role: "operator"})
	if _, err := db.Bootstrap(ctx, étendu); err != nil {
		t.Fatalf("second amorçage: %v", err)
	}

	pending, err := db.PendingGrants(ctx)
	if err != nil {
		t.Fatalf("déclarations en attente: %v", err)
	}
	for _, grant := range pending {
		if grant.Identifier == "bob" {
			t.Error("une déclaration ajoutée après l'amorçage a été reprise")
		}
	}
	if len(pending) != 2 {
		t.Errorf("%d déclaration(s) en attente, attendu les 2 initiales", len(pending))
	}
}

// Les déclarations en attente sont ce qui reste visible d'un identifiant mal
// orthographié : l'utilisateur se connecte sans permission, et rien d'autre ne
// le dit.
func TestLesDeclarationsEnAttenteSeListent(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.Bootstrap(ctx, spec()); err != nil {
		t.Fatalf("amorçage: %v", err)
	}

	pending, err := db.PendingGrants(ctx)
	if err != nil {
		t.Fatalf("déclarations: %v", err)
	}
	if len(pending) != 2 || pending[0].Identifier != "alice" || pending[0].Role != "releaser" {
		t.Errorf("déclarations = %+v", pending)
	}
}
