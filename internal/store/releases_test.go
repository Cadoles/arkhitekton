package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestEnsureVersionCreatesThenReuses(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	// Sans la synchronisation Redmine, la table est vide et aucune release ne
	// pourrait être créée alors que tout le reste de la chaîne fonctionne.
	first, err := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("création: %v", err)
	}

	second, err := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("réutilisation: %v", err)
	}
	if first != second {
		t.Errorf("deux versions créées pour un même nom: %d puis %d", first, second)
	}

	// La ligne provisoire doit être reconnaissable, pour que la synchronisation
	// sache laquelle remplacer.
	var origin, status string
	var externalID *string
	if err := db.Pool().QueryRow(ctx,
		`select origin, external_id, status from versions where id = $1`, first).
		Scan(&origin, &externalID, &status); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if origin != VersionOriginPending {
		t.Errorf("origin = %q, attendu %q pour une version provisoire", origin, VersionOriginPending)
	}
	// Une provisoire n'a rien à désigner dans le suivi : c'est justement ce
	// qu'elle attend.
	if externalID != nil {
		t.Errorf("external_id = %q, attendu nul", *externalID)
	}
	if status != "en-attente-de-synchronisation" {
		t.Errorf("statut = %q", status)
	}
}

func TestCreateAndGetRelease(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, err := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	id, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0-rc1", Kind: "pre", State: "draft",
		GitRef: "abc1234", ImageVersion: "1.8.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("création: %v", err)
	}

	release, err := db.GetRelease(ctx, testProjectID, "1.8.0-rc1")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if release.ID != id || release.Kind != "pre" || release.ImageVersion != "1.8.0-rc1" {
		t.Errorf("release = %+v", release)
	}
}

func TestGetReleaseDistinguishesAbsence(t *testing.T) {
	db := migrated(t)

	if _, err := db.GetRelease(context.Background(), testProjectID, "9.9.9"); !errors.Is(err, ErrReleaseNotFound) {
		t.Errorf("erreur = %v, attendu ErrReleaseNotFound", err)
	}
}

// Le re-tag : la finale porte son propre tag mais l'image de sa RC. C'est cet
// écart que `image_version` absorbe, et sans lequel toute comparaison à
// PortalData échouerait sur une release finale.
func TestFinalReleaseKeepsTheSourceImageVersion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")

	sourceID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0-rc3", Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("RC: %v", err)
	}

	if _, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", SourceReleaseID: &sourceID,
		CreatedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("finale: %v", err)
	}

	final, err := db.GetRelease(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if final.ImageVersion != "1.8.0-rc3" {
		t.Errorf("version d'image = %q, attendu celle de la RC", final.ImageVersion)
	}
	if final.SourceReleaseID == nil || *final.SourceReleaseID != sourceID {
		t.Errorf("release source = %v, attendu %d", final.SourceReleaseID, sourceID)
	}
}

// Une promotion enregistre ce qui a été **demandé**. Pousser le tag ne déploie
// rien : c'est ce qui permettra de distinguer « promu mais pas déployé » de
// « jamais promu ».
func TestRecordPromotion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	releaseID, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0-rc1", Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc1", CreatedBy: "vcarroy",
	})

	if err := db.UpsertEnvironment(ctx, Environment{
		ID: "qualif", Label: "Qualification",
		DeployerSettings: map[string]string{
			"promotionSuffix": "testing", "acceptsPrerelease": "true",
		},
		Rank: 2, Namespace: "mse-dev", Observable: true, Visible: true,
	}); err != nil {
		t.Fatalf("environnement: %v", err)
	}

	if err := db.RecordPromotion(ctx, Promotion{
		ReleaseID: releaseID, EnvironmentID: "qualif", ImageName: "portal",
		Tag: "1.8.0-rc1_testing", Digest: "sha256:abc",
	}); err != nil {
		t.Fatalf("promotion: %v", err)
	}

	promotions, err := db.ListPromotions(ctx, releaseID)
	if err != nil {
		t.Fatalf("liste: %v", err)
	}
	if len(promotions) != 1 || promotions[0].Tag != "1.8.0-rc1_testing" {
		t.Errorf("promotions = %+v", promotions)
	}
}

func TestManualActionsCarryTheirAuthorAndDate(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	if _, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "1.8.0", Kind: "final", State: "ready",
		GitRef: "abc", ImageVersion: "1.8.0-rc3", CreatedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("release: %v", err)
	}

	if err := db.SetManualActions(ctx, "1.8.0", "- purge du cache\n- bascule du DNS", "vcarroy"); err != nil {
		t.Fatalf("actions: %v", err)
	}

	release, err := db.GetRelease(ctx, testProjectID, "1.8.0")
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if release.ManualActions == "" {
		t.Error("texte perdu")
	}
	// Ce texte finit dans un document contractuel : sa provenance en fait partie.
	if release.ManualActionsUpdatedBy != "vcarroy" || release.ManualActionsUpdatedAt == nil {
		t.Errorf("auteur = %q, date = %v", release.ManualActionsUpdatedBy, release.ManualActionsUpdatedAt)
	}
}

func TestSetManualActionsOnAnUnknownRelease(t *testing.T) {
	db := migrated(t)

	if err := db.SetManualActions(context.Background(), "9.9.9", "texte", "vcarroy"); !errors.Is(err, ErrReleaseNotFound) {
		t.Errorf("erreur = %v, attendu ErrReleaseNotFound", err)
	}
}

// L'historique dit ce que le dashboard a **demandé**. Une promotion qu'une
// ImagePolicy ignore n'apparaît nulle part côté Git, alors qu'elle a bien eu
// lieu — et c'est ce cas-là qu'il faut pouvoir constater.
func TestEnvironmentHistoryFollowsWhatWasRequested(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.UpsertEnvironment(ctx, Environment{
		ID: "qualif", Label: "Qualification",
		DeployerSettings: map[string]string{
			"promotionSuffix": "testing", "acceptsPrerelease": "true",
		},
		Rank: 2, Namespace: "mse-dev", Observable: true, Visible: true,
	}); err != nil {
		t.Fatalf("environnement: %v", err)
	}

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "1.8.0")
	for _, tag := range []string{"1.8.0-rc1", "1.8.0-rc2"} {
		releaseID, err := db.CreateRelease(ctx, Release{
			ProjectID: testProjectID,
			VersionID: versionID, Tag: tag, Kind: "pre", State: "ready",
			GitRef: "abc", ImageVersion: tag, CreatedBy: "vcarroy",
		})
		if err != nil {
			t.Fatalf("release %s: %v", tag, err)
		}
		if err := db.RecordPromotion(ctx, Promotion{
			ReleaseID: releaseID, EnvironmentID: "qualif",
			ImageName: "portal", Tag: tag + "_testing",
		}); err != nil {
			t.Fatalf("promotion %s: %v", tag, err)
		}
	}

	history, err := db.ListEnvironmentHistory(ctx, "qualif", 0)
	if err != nil {
		t.Fatalf("historique: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("%d entrée(s), attendu 2", len(history))
	}
	if history[0].ReleaseTag != "1.8.0-rc2" {
		t.Errorf("première entrée = %q, attendu la plus récente", history[0].ReleaseTag)
	}
}

// buildJob met en file une construction d'image et la conclut.
func buildJob(t *testing.T, db *DB, releaseID int64, image, state string) int64 {
	t.Helper()
	ctx := context.Background()

	id, err := db.Enqueue(ctx, JobRequest{
		Kind:        "build",
		Params:      map[string]string{"image-name": image, "target": "web"},
		RequestedBy: "vcarroy",
		ReleaseID:   &releaseID,
	})
	if err != nil {
		t.Fatalf("mise en file de %s: %v", image, err)
	}
	if state != JobQueued {
		if err := db.FinishJob(ctx, id, state, ""); err != nil {
			t.Fatalf("conclusion de %s: %v", image, err)
		}
	}
	return id
}

/*
 * Le décompte des constructions.
 *
 * Une release en lance plusieurs — le pipeline ne construit qu'une image par
 * exécution — et n'est construite que lorsque toutes ont abouti. Compter en base
 * plutôt que retenir un état en mémoire est ce qui rend la conclusion correcte
 * après un redémarrage du worker : les runs continuent pendant qu'il ne tourne
 * pas.
 */
func TestLeDecompteDesConstructionsAttendToutesLesImages(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 930, "v3.3.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.3.0-rc1", Kind: "pre", State: "building",
		ImageVersion: "3.3.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	buildJob(t, db, releaseID, "portal", JobSucceeded)
	buildJob(t, db, releaseID, "portal-cli", JobSucceeded)
	buildJob(t, db, releaseID, "portal-dev", JobQueued)

	tally, err := db.CountBuilds(ctx, releaseID, "build")
	if err != nil {
		t.Fatalf("décompte: %v", err)
	}
	if tally.Done() {
		t.Errorf("série conclue alors qu'une image reste à construire: %+v", tally)
	}
	if tally.Pending != 1 || tally.Succeeded != 2 {
		t.Errorf("décompte = %+v", tally)
	}

	if err := db.FinishJob(ctx, buildJob(t, db, releaseID, "portal-dev", JobQueued),
		JobSucceeded, ""); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	// Le **dernier** essai de chaque image, et non tous : la première mise en
	// file de `portal-dev` reste `queued` et ne doit plus compter.
	tally, _ = db.CountBuilds(ctx, releaseID, "build")
	if !tally.Done() || tally.Succeeded != 3 {
		t.Errorf("décompte après la dernière image = %+v", tally)
	}
}

// Un échec suffit à disqualifier la série, même si les autres images passent.
func TestUneImageEnEchecEmpecheLaConclusion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 931, "v3.4.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.4.0-rc1", Kind: "pre", State: "building",
		ImageVersion: "3.4.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	buildJob(t, db, releaseID, "portal", JobSucceeded)
	buildJob(t, db, releaseID, "portal-cli", JobFailed)

	tally, _ := db.CountBuilds(ctx, releaseID, "build")
	if tally.Done() || tally.Failed != 1 {
		t.Errorf("décompte = %+v", tally)
	}

	// La reprise remplace l'essai précédent : une release dont la seconde
	// tentative passe entièrement ne doit pas rester en échec pour un verdict
	// que le dernier essai a remplacé.
	buildJob(t, db, releaseID, "portal-cli", JobSucceeded)
	tally, _ = db.CountBuilds(ctx, releaseID, "build")
	if !tally.Done() || tally.Failed != 0 {
		t.Errorf("décompte après reprise = %+v", tally)
	}
}

// Une série vide n'est pas conclue : entre l'écriture de `building` et la mise
// en file des constructions, il existe un instant où le décompte est nul, et une
// redélivrance tardive y verrait « tout a réussi ».
func TestUneSerieVideNestPasConclue(t *testing.T) {
	db := migrated(t)

	versionID := redmineVersion(t, db, 932, "v3.5.0", "open")
	releaseID, err := db.CreateRelease(context.Background(), Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.5.0-rc1", Kind: "pre", State: "building",
		ImageVersion: "3.5.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	tally, err := db.CountBuilds(context.Background(), releaseID, "build")
	if err != nil {
		t.Fatalf("décompte: %v", err)
	}
	if tally.Done() {
		t.Error("une série vide ne doit pas être conclue")
	}
}

// Une release appartient à son projet, et la base l'exige.
//
// Le gestionnaire de création ne renseignait pas `ProjectID` depuis que la
// migration 0025 a rattaché les releases à leur projet. La valeur zéro d'un
// `int64` n'est pas nulle : c'est l'identifiant 0, qui ne désigne aucun projet,
// et la clé étrangère refusait l'insertion.
//
// Personne ne l'a vu parce que l'échec se lisait « cette release existe peut-être
// déjà » — le gestionnaire traduisait toute erreur de la base en conflit. Le
// message envoyait chercher un doublon pendant que la vraie cause restait
// invisible.
func TestUneReleaseSansProjetEstRefuseeParLaBase(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, err := db.EnsureVersion(ctx, testProjectID, "3.2.1")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	_, err = db.CreateRelease(ctx, Release{
		VersionID: versionID, Tag: "3.2.1-rc1", Kind: "pre", State: "draft",
		ImageVersion: "3.2.1-rc1", CreatedBy: "vcarroy",
	})
	if err == nil {
		t.Fatal("une release sans projet doit être refusée: rien ne dirait à qui elle appartient")
	}

	// Avec son projet, elle passe : c'est la seule différence.
	if _, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.1-rc1", Kind: "pre", State: "draft",
		ImageVersion: "3.2.1-rc1", CreatedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("release avec projet refusée: %v", err)
	}
}

// Le doublon est une violation d'unicité, et non n'importe quelle erreur : c'est
// ce que le gestionnaire doit pouvoir distinguer pour rendre un 409 plutôt qu'un
// 500 muet.
func TestUnTagDejaPrisDansLeProjetEstUnDoublon(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, _ := db.EnsureVersion(ctx, testProjectID, "3.2.1")
	release := Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.1-rc1", Kind: "pre", State: "draft",
		ImageVersion: "3.2.1-rc1", CreatedBy: "vcarroy",
	}

	if _, err := db.CreateRelease(ctx, release); err != nil {
		t.Fatalf("première création: %v", err)
	}

	_, err := db.CreateRelease(ctx, release)
	if err == nil {
		t.Fatal("le même tag deux fois dans un projet doit être refusé")
	}
	if !strings.Contains(err.Error(), "23505") && !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("erreur %v — attendu une violation d'unicité", err)
	}
}
