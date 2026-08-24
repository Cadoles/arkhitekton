package store

import (
	"context"
	"errors"
	"testing"
)

/*
 * Les dépôts pilotés.
 *
 * Ce qui mérite d'être tenu ici : l'unicité du dépôt — deux entrées pour le même
 * décriraient la même chose sans que rien ne dise laquelle fait foi — et le
 * constat d'accès, dont l'absence ne doit pas se lire comme un échec.
 */

func project(t *testing.T, db *DB, repository string) int64 {
	t.Helper()

	id, err := db.CreateProject(context.Background(), Project{
		Repository: repository, Label: "Portail", ForgeURL: "https://forge.example.fr",
		Dockerfile: "./misc/k8s/images/portal/Dockerfile", BuildContext: ".",
		MultiStage: true,
		Targets: []Target{
			{Stage: "web", Image: "portal"},
			{Stage: "cli", Image: "portal-cli"},
		},
		CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("déclaration de %s: %v", repository, err)
	}
	return id
}

func TestUnProjetSeDeclareEtSeRelit(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := project(t, db, "CNOUS/mse")

	read, err := db.GetProject(ctx, id)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if read.Repository != "CNOUS/mse" || read.Label != "Portail" {
		t.Errorf("projet = %+v", read)
	}
	if len(read.Targets) != 2 || read.Targets[1].Image != "portal-cli" {
		t.Errorf("étapes = %+v", read.Targets)
	}
	// Aucun test d'accès n'a eu lieu : l'absence de constat doit se distinguer
	// d'un test qui a échoué.
	if read.CheckedAt != nil || read.CheckOK != nil {
		t.Errorf("constat posé sans test: %+v", read)
	}
}

// Deux entrées pour un même dépôt décriraient la même chose, et rien ne dirait
// laquelle fait foi.
func TestUnDepotNeSeDeclarePasDeuxFois(t *testing.T) {
	db := migrated(t)

	project(t, db, "CNOUS/mse")

	_, err := db.CreateProject(context.Background(), Project{
		Repository: "CNOUS/mse", Label: "Doublon", CreatedBy: "vcarroy",
	})
	if !errors.Is(err, ErrProjectExists) {
		t.Errorf("seconde déclaration: %v, attendu ErrProjectExists", err)
	}
}

// Une ligne de formulaire à moitié remplie ne devient pas une étape : elle
// n'aurait rien à construire, et rien à l'écran ne la distinguerait d'une étape
// valide.
//
// Cette fonction éprouvait aussi l'immuabilité du dépôt. Elle a cessé de le
// faire avec elle : les projets amorcés portent le dépôt que la configuration a
// nommé, et le corriger est le premier geste qu'on vient faire sur cet écran.
func TestUneCorrectionEcarteLesEtapesVides(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := project(t, db, "CNOUS/mse")

	if err := db.UpdateProject(ctx, Project{
		ID: id, Repository: "CNOUS/mse", Label: "Portail MSE",
		ForgeURL: "https://forge.example.fr", Dockerfile: "Dockerfile",
		BuildContext: ".", MultiStage: false,
		Targets: []Target{{Stage: "", Image: ""}, {Stage: "web", Image: "portal"}},
	}); err != nil {
		t.Fatalf("mise à jour: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if read.Label != "Portail MSE" || read.MultiStage {
		t.Errorf("projet = %+v", read)
	}
	if len(read.Targets) != 1 || read.Targets[0].Stage != "web" {
		t.Errorf("étapes = %+v", read.Targets)
	}
}

func TestLeConstatDaccesEstConserve(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := project(t, db, "CNOUS/mse")

	if err := db.RecordProjectCheck(ctx, id, false, "404 sur /repos/CNOUS/mse"); err != nil {
		t.Fatalf("constat: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if read.CheckedAt == nil || read.CheckOK == nil || *read.CheckOK {
		t.Fatalf("constat = %+v", read)
	}
	if read.CheckNote == "" {
		t.Error("le motif de l'échec doit être conservé: sans lui, « accès refusé » n'aide pas")
	}

	// Un second test remplace le premier : c'est un constat, pas un journal.
	if err := db.RecordProjectCheck(ctx, id, true, "branche par défaut : main"); err != nil {
		t.Fatalf("second constat: %v", err)
	}
	read, _ = db.GetProject(ctx, id)
	if read.CheckOK == nil || !*read.CheckOK {
		t.Errorf("constat après reprise = %+v", read)
	}
}

func TestUnProjetAbsentSeDistingueDUneErreur(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.GetProject(ctx, 4242); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("lecture d'un projet absent: %v", err)
	}
	if err := db.DeleteProject(ctx, 4242); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("suppression d'un projet absent: %v", err)
	}
	if err := db.RecordProjectCheck(ctx, 4242, true, ""); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("constat sur un projet absent: %v", err)
	}
}

/*
 * L'amorçage depuis la configuration.
 *
 * Il existe parce que la table doit décrire ce que le dashboard fait réellement,
 * pas ce qu'un administrateur aura pensé à y ressaisir. Deux propriétés le
 * rendent sûr : il est rejouable, et il n'écrase rien.
 */
func TestLamorcageEstRejouableEtNecrasePas(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	configured := []Project{
		{
			Repository: "CNOUS/mse", Label: "Application",
			ForgeURL: "https://forge.example.fr", Dockerfile: "Dockerfile",
			BuildContext: ".", MultiStage: true,
			Targets:   []Target{{Stage: "web", Image: "portal"}},
			CreatedBy: "configuration",
		},
		{
			Repository: "CNOUS/mse-kustom", Label: "Kustomizations",
			ForgeURL: "https://forge.example.fr", BuildContext: ".",
			CreatedBy: "configuration",
		},
		// Une URL dont on n'a pas su tirer `owner/nom` : écartée plutôt que
		// déclarée, un dépôt qui ne désigne rien donnerait un test d'accès en
		// échec sans qu'on sache que la faute est en amont.
		{Repository: "", Label: "Illisible"},
	}

	declared, err := db.SeedProjects(ctx, configured)
	if err != nil {
		t.Fatalf("amorçage: %v", err)
	}
	if declared != 2 {
		t.Errorf("%d projet(s) déclarés, attendu 2", declared)
	}

	// Un administrateur corrige un libellé.
	projects, _ := db.ListProjects(ctx)
	var mse Project
	for _, project := range projects {
		if project.Repository == "CNOUS/mse" {
			mse = project
		}
	}
	if err := db.UpdateProject(ctx, Project{
		ID: mse.ID, Repository: mse.Repository, Label: "Portail MSE",
		ForgeURL: mse.ForgeURL, Dockerfile: mse.Dockerfile,
		BuildContext: mse.BuildContext, MultiStage: mse.MultiStage,
		Targets: mse.Targets,
	}); err != nil {
		t.Fatalf("correction: %v", err)
	}

	// Le redémarrage rejoue l'amorçage : il ne doit rien déclarer, et surtout
	// rien réécrire.
	declared, err = db.SeedProjects(ctx, configured)
	if err != nil {
		t.Fatalf("second amorçage: %v", err)
	}
	if declared != 0 {
		t.Errorf("%d projet(s) redéclarés", declared)
	}

	read, _ := db.GetProject(ctx, mse.ID)
	if read.Label != "Portail MSE" {
		t.Errorf("libellé = %q: l'amorçage a écrasé une correction", read.Label)
	}
}

// Le dépôt est modifiable, et c'est le cas d'usage principal : les projets
// amorcés portent celui que la configuration a nommé, et le corriger est
// exactement ce qu'on vient faire.
func TestLeDepotSeCorrigeEtEffaceLeConstat(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := project(t, db, "vcarroy/mse-test-project")
	if err := db.RecordProjectCheck(ctx, id, true, "branche par défaut : main"); err != nil {
		t.Fatalf("constat: %v", err)
	}

	corrected := Project{
		ID: id, Repository: "CNOUS/mse", Label: "Portail",
		ForgeURL: "https://forge.example.fr", Dockerfile: "Dockerfile",
		BuildContext: ".", MultiStage: false,
	}
	if err := db.UpdateProject(ctx, corrected); err != nil {
		t.Fatalf("correction: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if read.Repository != "CNOUS/mse" {
		t.Errorf("dépôt = %q", read.Repository)
	}
	// Le constat portait sur ce qu'on ne pilote plus. Le garder afficherait
	// « accès vérifié » sur un dépôt que personne n'a testé.
	if read.CheckedAt != nil || read.CheckOK != nil || read.CheckNote != "" {
		t.Errorf("constat conservé après changement de dépôt: %+v", read)
	}
}

// Modifier autre chose que le dépôt conserve le constat : le refaire à chaque
// correction de libellé le rendrait inutile.
func TestUneCorrectionSansChangementDeDepotGardeLeConstat(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := project(t, db, "CNOUS/mse")
	if err := db.RecordProjectCheck(ctx, id, true, "branche par défaut : main"); err != nil {
		t.Fatalf("constat: %v", err)
	}

	if err := db.UpdateProject(ctx, Project{
		ID: id, Repository: "CNOUS/mse", Label: "Portail MSE",
		ForgeURL: "https://forge.example.fr", BuildContext: ".",
	}); err != nil {
		t.Fatalf("correction: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if read.CheckedAt == nil || read.CheckOK == nil || !*read.CheckOK {
		t.Errorf("constat perdu sur une correction de libellé: %+v", read)
	}
}

// Deux entrées pour un même dépôt décriraient la même chose : le refus vaut
// aussi pour une correction, et doit se distinguer d'une panne d'écriture.
func TestUneCorrectionVersUnDepotDejaDeclareEstRefusee(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	project(t, db, "CNOUS/mse")
	id := project(t, db, "CNOUS/mse-kustom")

	err := db.UpdateProject(ctx, Project{
		ID: id, Repository: "CNOUS/mse", Label: "Doublon",
		ForgeURL: "https://forge.example.fr", BuildContext: ".",
	})
	if !errors.Is(err, ErrProjectExists) {
		t.Errorf("correction vers un dépôt déclaré: %v, attendu ErrProjectExists", err)
	}
}

// Un dépôt vide n'est jamais une intention : c'est un appelant qui a oublié le
// champ. L'accepter l'effacerait en silence, et le projet deviendrait une ligne
// qui ne désigne rien.
func TestUneCorrectionSansDepotEstRefusee(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	id := project(t, db, "CNOUS/mse")

	if err := db.UpdateProject(ctx, Project{ID: id, Label: "Sans dépôt"}); err == nil {
		t.Fatal("un dépôt vide devrait être refusé")
	}

	read, _ := db.GetProject(ctx, id)
	if read.Repository != "CNOUS/mse" {
		t.Errorf("dépôt = %q: il a été effacé", read.Repository)
	}
}
