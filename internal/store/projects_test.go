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
	db := migratedBare(t)
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
	db := migratedBare(t)

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
	db := migratedBare(t)
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
	db := migratedBare(t)
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
	db := migratedBare(t)
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
	db := migratedBare(t)
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
	db := migratedBare(t)
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
	db := migratedBare(t)
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
	db := migratedBare(t)
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
	db := migratedBare(t)
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

// La référence complète est ce qui désigne une image. Un nom court seul
// ressemble à une référence tirable sans l'être.
func TestLaReferenceCombineLeRegistreEtLimage(t *testing.T) {
	project := Project{ImageRegistry: "reg.example.fr/mse"}

	if got := project.Reference(Target{Stage: "cli", Image: "portal-cli"}); got != "reg.example.fr/mse/portal-cli" {
		t.Errorf("référence = %q", got)
	}
	// Les barres de fin ne doivent pas se redoubler : `projet//portal` n'est pas
	// la même image que `projet/portal` pour un registre.
	sloppy := Project{ImageRegistry: "reg.example.fr/mse/"}
	if got := sloppy.Reference(Target{Image: "portal"}); got != "reg.example.fr/mse/portal" {
		t.Errorf("référence = %q", got)
	}
	// Vide plutôt que tronquée : mieux vaut ne rien afficher qu'un nom qui ne
	// désigne rien.
	if got := project.Reference(Target{Stage: "web"}); got != "" {
		t.Errorf("référence sans image = %q", got)
	}
	if got := (Project{}).Reference(Target{Image: "portal"}); got != "" {
		t.Errorf("référence sans registre = %q", got)
	}
}

// Un Dockerfile à image unique passe par la même liste, avec une entrée sans
// cible : c'est le cas qui n'avait aucun nom d'image du tout.
func TestUneImageUniqueSeDeclareSansCible(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id, err := db.CreateProject(ctx, Project{
		Repository: "vcarroy/outil", Label: "Outil",
		ForgeURL: "https://forge.example.fr", Dockerfile: "Dockerfile",
		BuildContext: ".", ImageRegistry: "reg.example.fr/mse",
		MultiStage: false,
		Targets:    []Target{{Stage: "", Image: "outil"}},
		CreatedBy:  "vcarroy",
	})
	if err != nil {
		t.Fatalf("déclaration: %v", err)
	}

	read, _ := db.GetProject(ctx, id)
	if len(read.Targets) != 1 || read.Targets[0].Image != "outil" {
		t.Fatalf("étapes = %+v", read.Targets)
	}
	if got := read.Reference(read.Targets[0]); got != "reg.example.fr/mse/outil" {
		t.Errorf("référence = %q", got)
	}
}

// Deux formes de dépôt coexistent, et aucune n'est un cas particulier de l'autre.
//
// Le modèle ne savait dire que « telle **étape** du Dockerfile du projet produit
// telle image » — la forme du dépôt de MSE, prise pour une vérité générale.
// `hydra-dispatcher-mse-theme` porte `Dockerfile.dev` et `Dockerfile.prod` à sa
// racine : deux fichiers, deux images, aucune étape.
func TestUneEtapePeutNommerSonPropreDockerfile(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	// La forme MSE : un Dockerfile, des étapes.
	mse := Project{
		Slug: "mse", Repository: "vcarroy/mse", Label: "MSE",
		ForgeURL: "https://forge.example", Dockerfile: "Dockerfile", BuildContext: ".",
		MultiStage: true,
		Targets: []Target{
			{Stage: "web", Image: "portal"},
			{Stage: "cli", Image: "portal-cli"},
		},
	}
	if _, err := db.CreateProject(ctx, mse); err != nil {
		t.Fatalf("projet à étapes: %v", err)
	}

	// La forme thème : un Dockerfile par image, aucune étape.
	theme := Project{
		Slug: "theme", Repository: "CNOUS/hydra-dispatcher-mse-theme", Label: "Thème",
		ForgeURL: "https://forge.example", BuildContext: ".", MultiStage: true,
		Targets: []Target{
			{Image: "hydra-dispatcher-mse-theme", Dockerfile: "Dockerfile.prod"},
			{Image: "hydra-dispatcher-mse-theme-dev", Dockerfile: "Dockerfile.dev"},
		},
	}
	id, err := db.CreateProject(ctx, theme)
	if err != nil {
		t.Fatalf("projet à Dockerfiles: %v", err)
	}

	stored, err := db.GetProject(ctx, id)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if len(stored.Buildable()) != 2 {
		t.Fatalf("%d étape(s) constructible(s), attendu 2: %+v", len(stored.Buildable()), stored.Targets)
	}
	// Le plus précis gagne : l'étape nomme son fichier, le projet n'en a aucun.
	if got := stored.DockerfileFor(stored.Targets[0]); got != "Dockerfile.prod" {
		t.Errorf("Dockerfile de la première étape = %q", got)
	}

	// Et l'inverse : une étape muette retombe sur celui du projet.
	if got := (Project{Dockerfile: "Dockerfile"}).DockerfileFor(Target{Stage: "web"}); got != "Dockerfile" {
		t.Errorf("repli sur le Dockerfile du projet = %q", got)
	}
}

// Une étape sans nom d'image ne publie rien : elle est écartée à l'écriture, et
// la base la refuserait de toute façon.
func TestUneEtapeSansImageEstEcartee(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	id, err := db.CreateProject(ctx, Project{
		Slug: "vide", Repository: "x/vide", Label: "Vide",
		ForgeURL: "https://forge.example", Dockerfile: "Dockerfile", BuildContext: ".",
		Targets: []Target{{Stage: "web"}, {Stage: "cli", Image: "utile"}},
	})
	if err != nil {
		t.Fatalf("création: %v", err)
	}

	stored, err := db.GetProject(ctx, id)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if len(stored.Targets) != 1 || stored.Targets[0].Image != "utile" {
		t.Errorf("étapes conservées = %+v", stored.Targets)
	}
}
