package store

import (
	"context"
	"errors"
	"testing"
)

// redmineVersion dépose une version réelle, comme le ferait la synchronisation.
func redmineVersion(t *testing.T, db *DB, redmineID int64, name, status string) int64 {
	t.Helper()
	id, err := db.UpsertRedmineVersion(context.Background(), RedmineVersion{
		ProjectID: testProjectID,
		RedmineID: redmineID, Name: name,
		Normalized: testNames.Normalize(name),
		Status:     status,
	})
	if err != nil {
		t.Fatalf("version %s: %v", name, err)
	}
	return id
}

func issueOf(t *testing.T, db *DB, redmineID int64, versionRedmineID int64, tracker, status, bucket string, doneRatio int, closed bool) {
	t.Helper()
	if err := db.UpsertRedmineIssue(context.Background(), RedmineIssue{
		ProjectID: testProjectID,
		RedmineID: redmineID, VersionRedmineID: &versionRedmineID,
		Tracker: tracker, Subject: "sujet", StatusName: status, StatusBucket: bucket,
		DoneRatio: doneRatio, IsClosed: closed,
	}); err != nil {
		t.Fatalf("demande %d: %v", redmineID, err)
	}
}

// releaseOn rattache une release à une version, par la voie normale.
func releaseOn(t *testing.T, db *DB, versionID int64, tag string) {
	t.Helper()
	if _, err := db.CreateRelease(context.Background(), Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: tag, Kind: "pre", State: "ready",
		GitRef: "abc", ImageVersion: tag, CreatedBy: "vcarroy",
	}); err != nil {
		t.Fatalf("release %s: %v", tag, err)
	}
}

// Le cas qui justifie tout le mécanisme : une release créée avant que Redmine ne
// soit connu doit se retrouver rattachée à la version réelle, et non rester sur une
// provisoire que personne ne regarde.
func TestUneVersionProvisoireEstAdopteeParLaVersionReelle(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	// Une release est créée alors que la synchronisation n'a jamais tourné.
	pendingID, err := db.EnsureVersion(ctx, testProjectID, "3.2.0")
	if err != nil {
		t.Fatalf("version provisoire: %v", err)
	}
	releaseOn(t, db, pendingID, "3.2.0-rc1")

	// La synchronisation découvre ensuite la version, sous son nom réel préfixé.
	redmineVersion(t, db, 805, "v3.2.0", "open")

	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 1 || report[0].AdoptedInto != "v3.2.0" || report[0].Releases != 1 {
		t.Fatalf("rapport d'adoption: %+v", report)
	}

	// La provisoire a disparu, et la release a suivi.
	pendings, err := db.PendingVersions(ctx)
	if err != nil {
		t.Fatalf("provisoires: %v", err)
	}
	if len(pendings) != 0 {
		t.Errorf("provisoires subsistantes: %v", pendings)
	}

	summaries, err := db.ListVersionSummaries(ctx)
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("%d versions, attendu 1", len(summaries))
	}
	if summaries[0].Name != "v3.2.0" || summaries[0].Releases != 1 {
		t.Errorf("version après adoption: %+v", summaries[0])
	}
	if summaries[0].Pending {
		t.Error("la version réelle ne doit pas être marquée provisoire")
	}
}

// Le cas réel : `MSE 3.1.2` est close — ancien système de versionnement — et `v3.1.2`
// ouverte. Une release visant ce numéro vise la version active, et l'adoption doit
// donc aboutir plutôt que déclarer une ambiguïté.
func TestUneVersionCloseHomonymeNeBloquePasLAdoption(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	pendingID, err := db.EnsureVersion(ctx, testProjectID, "3.1.2")
	if err != nil {
		t.Fatalf("version provisoire: %v", err)
	}
	releaseOn(t, db, pendingID, "3.1.2")

	redmineVersion(t, db, 803, "MSE 3.1.2", "closed")
	redmineVersion(t, db, 804, "v3.1.2", "open")

	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 1 {
		t.Fatalf("rapport: %+v", report)
	}
	if report[0].Ambiguous {
		t.Errorf("une seule version active porte ce numéro : pas d'ambiguïté attendue, %+v", report[0])
	}
	if report[0].AdoptedInto != "v3.1.2" {
		t.Errorf("adoptée par %q, attendu la version active v3.1.2", report[0].AdoptedInto)
	}

	// La provisoire a disparu et la release suit la version ouverte.
	pendings, err := db.PendingVersions(ctx)
	if err != nil {
		t.Fatalf("provisoires: %v", err)
	}
	if len(pendings) != 0 {
		t.Errorf("provisoires subsistantes: %v", pendings)
	}
}

// `locked` n'est pas `closed` : une version verrouillée n'accepte plus de nouvelle
// demande mais n'est pas terminée, et reste donc la candidate.
func TestUneVersionVerrouilleeResteCandidate(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	pendingID, _ := db.EnsureVersion(ctx, testProjectID, "3.1.3")
	releaseOn(t, db, pendingID, "3.1.3")

	redmineVersion(t, db, 810, "MSE 3.1.3", "closed")
	redmineVersion(t, db, 811, "v3.1.3", "locked")

	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 1 || report[0].AdoptedInto != "v3.1.3" {
		t.Errorf("rapport: %+v", report)
	}
}

// L'indétermination réelle subsiste quand deux versions actives portent le même
// numéro : là, trancher rattacherait des releases à la mauvaise version.
func TestDeuxVersionsActivesHomonymesRestentIndecidables(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	pendingID, err := db.EnsureVersion(ctx, testProjectID, "3.1.2")
	if err != nil {
		t.Fatalf("version provisoire: %v", err)
	}
	releaseOn(t, db, pendingID, "3.1.2")

	redmineVersion(t, db, 803, "MSE 3.1.2", "open")
	redmineVersion(t, db, 804, "v3.1.2", "open")

	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 1 || !report[0].Ambiguous {
		t.Fatalf("rapport: %+v", report)
	}
	if len(report[0].Candidates) != 2 {
		t.Errorf("candidates: %v", report[0].Candidates)
	}

	// La provisoire subsiste, donc reste signalée.
	pendings, err := db.PendingVersions(ctx)
	if err != nil {
		t.Fatalf("provisoires: %v", err)
	}
	if len(pendings) != 1 || pendings[0] != "3.1.2" {
		t.Errorf("provisoires: %v", pendings)
	}

	ambiguous, err := db.AmbiguousVersions(ctx)
	if err != nil {
		t.Fatalf("ambiguïtés: %v", err)
	}
	if len(ambiguous) != 1 || ambiguous[0].Normalized != "3.1.2" {
		t.Errorf("ambiguïtés: %+v", ambiguous)
	}
}

// L'adoption ne doit pas dépendre de l'ordre de lecture des versions.
func TestLAdoptionNeDependPasDeLOrdreDeLecture(t *testing.T) {
	for _, ordre := range [][]string{{"MSE 3.1.2", "v3.1.2"}, {"v3.1.2", "MSE 3.1.2"}} {
		db := migrated(t)
		ctx := context.Background()

		pendingID, _ := db.EnsureVersion(ctx, testProjectID, "3.1.2")
		releaseOn(t, db, pendingID, "3.1.2")

		// Les deux actives : c'est le seul cas encore indécidable, et donc le seul
		// où l'ordre de lecture pourrait faire une différence.
		for i, name := range ordre {
			redmineVersion(t, db, int64(900+i), name, "open")
		}

		report, err := db.AdoptPendingVersions(ctx)
		if err != nil {
			t.Fatalf("%v: adoption: %v", ordre, err)
		}
		if len(report) != 1 || !report[0].Ambiguous {
			t.Errorf("ordre %v: l'ambiguïté doit être constatée quel que soit l'ordre, rapport %+v",
				ordre, report)
		}
	}
}

// Une version provisoire que Redmine ne connaît pas reste visible : c'est le
// symptôme d'une faute de frappe dans un tag, et le taire laisserait un écran
// afficher « en attente de synchronisation » indéfiniment.
func TestUneProvisoireSansCorrespondanceResteSignalee(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if _, err := db.EnsureVersion(ctx, testProjectID, "9.9.9"); err != nil {
		t.Fatalf("provisoire: %v", err)
	}
	redmineVersion(t, db, 805, "v3.2.0", "open")

	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 0 {
		t.Errorf("aucune adoption attendue: %+v", report)
	}

	pendings, err := db.PendingVersions(ctx)
	if err != nil {
		t.Fatalf("provisoires: %v", err)
	}
	if len(pendings) != 1 || pendings[0] != "9.9.9" {
		t.Errorf("provisoires: %v", pendings)
	}
}

// Les rejetées sortent du calcul : les compter au dénominateur ferait baisser la
// complétion d'une version parce qu'on y a renoncé à une demande (doc 04 §4.5).
func TestLesDemandesRejeteesSortentDuCalcul(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Évolution", "Fermé", "done", 100, true)
	issueOf(t, db, 2, 805, "Évolution", "En cours", "doing", 50, false)
	issueOf(t, db, 3, 805, "Évolution", "Rejeté", "rejected", 0, true)

	summary, err := db.ListVersionSummaries(ctx)
	if err != nil {
		t.Fatalf("versions: %v", err)
	}
	if len(summary) != 1 {
		t.Fatalf("%d versions", len(summary))
	}

	// Deux comptées sur trois demandes, une terminée.
	if summary[0].Counted != 2 || summary[0].Done != 1 {
		t.Errorf("comptes: counted=%d done=%d, attendu 2 et 1",
			summary[0].Counted, summary[0].Done)
	}
	// La pondérée porte sur les comptées : 100 + 50, et non 150 sur trois.
	if summary[0].WeightedDone != 150 {
		t.Errorf("pondération %d, attendu 150", summary[0].WeightedDone)
	}
}

// Les anomalies sont comptées à part et n'entrent pas dans la complétion : une
// version peut être fonctionnellement complète et en porter.
func TestLesAnomaliesSontCompteesAPart(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Évolution", "Fermé", "done", 100, true)
	// Les deux trackers d'anomalie de l'instance. Compter sur l'égalité à
	// « Anomalie » en manquerait une sur deux.
	issueOf(t, db, 2, 805, "Anomalie", "Nouveau", "todo", 0, false)
	issueOf(t, db, 3, 805, "Anomalie (SDN4)", "Nouveau", "todo", 0, false)
	// Une anomalie fermée ne compte pas comme ouverte.
	issueOf(t, db, 4, 805, "Anomalie", "Fermé", "done", 100, true)

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if summary.OpenAnomalies != 2 {
		t.Errorf("%d anomalies ouvertes, attendu 2 (les deux trackers)", summary.OpenAnomalies)
	}

	issues, err := db.ListVersionIssues(ctx, summary.ID)
	if err != nil {
		t.Fatalf("demandes: %v", err)
	}
	anomalies := 0
	for _, issue := range issues {
		if issue.Anomaly {
			anomalies++
		}
	}
	if anomalies != 3 {
		t.Errorf("%d demandes marquées anomalie, attendu 3", anomalies)
	}
}

// Un statut non projeté tombe dans `todo` et se signale : les statuts Redmine
// changent sans prévenir, et une dérive silencieuse du taux de complétion serait
// invisible.
func TestUnStatutNonProjeteSeSignale(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Évolution", "Statut Inédit", "todo", 0, false)

	unmapped, err := db.UnmappedStatuses(ctx)
	if err != nil {
		t.Fatalf("statuts non projetés: %v", err)
	}
	if len(unmapped) != 1 || unmapped[0].StatusName != "Statut Inédit" || unmapped[0].Issues != 1 {
		t.Errorf("non projetés: %+v", unmapped)
	}

	// Une fois déclaré, il disparaît de la liste et la réapplication le déplace.
	if err := db.SetStatusBucket(ctx, "Statut Inédit", "done"); err != nil {
		t.Fatalf("projection: %v", err)
	}
	moved, err := db.ReprojectIssues(ctx)
	if err != nil {
		t.Fatalf("réapplication: %v", err)
	}
	if moved != 1 {
		t.Errorf("%d demandes déplacées, attendu 1", moved)
	}

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if summary.Done != 1 {
		t.Errorf("la demande devrait être comptée terminée après réapplication: %+v", summary)
	}
}

// Les 31 statuts de l'instance sont projetés par la migration : aucun ne doit tomber
// dans le seau par défaut sans avoir été décidé.
func TestLesStatutsDeLInstanceSontTousProjetes(t *testing.T) {
	db := migrated(t)

	mapping, err := db.StatusMapping(context.Background())
	if err != nil {
		t.Fatalf("projection: %v", err)
	}

	// Relevés sur l'instance réelle par /issue_statuses.json.
	reels := []string{
		"Brouillon", "Nouveau", "Analyse", "Pris en charge", "En attente", "Rejeté",
		"Ready", "En cours", "Développement", "En revue", "Livré DEV", "A livrer usine",
		"Test usine", "Prêt à livrer", "Livré", "Test", "Test en échec", "Testé",
		"Commentaire", "Installation", "Documentation", "Done", "Résolu", "Fermé",
		"Exécuté avec succès", "Non exécuté", "Exécuté en échec", "En Standby",
		"En StandBy", "Ré-ouvert",
	}
	for _, statut := range reels {
		if _, ok := mapping[statut]; !ok {
			t.Errorf("statut %q sans projection déclarée", statut)
		}
	}

	// `Rejeté` est le seul hors périmètre.
	if mapping["Rejeté"] != "rejected" {
		t.Errorf("Rejeté projeté en %q", mapping["Rejeté"])
	}
	// `Ré-ouvert` est déclaré clos par Redmine ; la projection dit l'inverse, et
	// c'est délibéré — une demande réouverte est en cours.
	if mapping["Ré-ouvert"] != "doing" {
		t.Errorf("Ré-ouvert projeté en %q, attendu doing", mapping["Ré-ouvert"])
	}
}

// Le curseur n'avance pas après un échec : l'avancer sauterait la fenêtre non lue, et
// les demandes modifiées pendant celle-ci ne seraient jamais reprises.
func TestUnEchecNAvancePasLeCurseur(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	if err := db.RecordSyncSuccess(ctx, "redmine", "2026-08-01T00:00:00Z"); err != nil {
		t.Fatalf("succès: %v", err)
	}
	if err := db.RecordSyncFailure(ctx, "redmine", "Redmine injoignable"); err != nil {
		t.Fatalf("échec: %v", err)
	}

	state, err := db.GetSyncState(ctx, "redmine")
	if err != nil {
		t.Fatalf("état: %v", err)
	}
	if state.Cursor != "2026-08-01T00:00:00Z" {
		t.Errorf("curseur %q: il ne doit pas bouger après un échec", state.Cursor)
	}
	if state.LastError != "Redmine injoignable" {
		t.Errorf("erreur %q", state.LastError)
	}
	// `last_ok` distinct de `last_run` : l'interface doit pouvoir dire « données
	// vieilles de tant, dernière tentative en échec ».
	if state.LastOK == nil || state.LastRun == nil || !state.LastRun.After(*state.LastOK) {
		t.Errorf("horodatages: run=%v ok=%v", state.LastRun, state.LastOK)
	}
}

// Jamais synchronisé n'est pas une erreur : c'est l'état au premier démarrage, et il
// doit conduire à une lecture complète.
func TestUneSourceJamaisSynchroniseeRendUnEtatVide(t *testing.T) {
	db := migrated(t)

	state, err := db.GetSyncState(context.Background(), "redmine")
	if err != nil {
		t.Fatalf("état: %v", err)
	}
	if state.Cursor != "" || state.LastRun != nil || state.LastOK != nil {
		t.Errorf("état inattendu: %+v", state)
	}
}

func mustVersionID(t *testing.T, db *DB, name string) int64 {
	t.Helper()
	var id int64
	if err := db.pool.QueryRow(context.Background(),
		`select id from versions where name = $1`, name).Scan(&id); err != nil {
		t.Fatalf("version %q: %v", name, err)
	}
	return id
}

// Le seau `client-test` s'intercale entre `doing` et `done`, et **ne compte pas comme
// terminé** : il entre au dénominateur de la complétion, jamais au numérateur.
// Confondre « fini de développer » et « accepté » est exactement ce qu'un taux de
// complétion doit éviter.
func TestLeSeauTestClientCompteAuDenominateurPasAuNumerateur(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Évolution", "Fermé", BucketDone, 100, true)
	issueOf(t, db, 2, 805, "Évolution", "Livré", BucketClientTest, 100, false)
	issueOf(t, db, 3, 805, "Évolution", "En cours", BucketDoing, 30, false)
	issueOf(t, db, 4, 805, "Évolution", "Rejeté", BucketRejected, 0, true)

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	// Trois comptées — tout sauf la rejetée — dont une seule terminée.
	if summary.Counted != 3 {
		t.Errorf("dénominateur %d, attendu 3 : test client doit y figurer", summary.Counted)
	}
	if summary.Done != 1 {
		t.Errorf("numérateur %d, attendu 1 : test client ne doit pas y figurer", summary.Done)
	}
	// La pondérée porte sur les comptées : 100 + 100 + 30.
	if summary.WeightedDone != 230 {
		t.Errorf("pondération %d, attendu 230", summary.WeightedDone)
	}
}

// La contrainte de la base accepte les cinq seaux, et rien d'autre. Un seau inventé
// doit être refusé plutôt que rangé silencieusement.
func TestLaBaseAccepteLesCinqSeauxEtRienDautre(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")

	for i, bucket := range []string{BucketTodo, BucketDoing, BucketClientTest, BucketDone, BucketRejected} {
		if err := db.UpsertRedmineIssue(ctx, RedmineIssue{
			ProjectID: testProjectID,
			RedmineID: int64(100 + i), Tracker: "Évolution", Subject: "s",
			StatusName: "x", StatusBucket: bucket,
		}); err != nil {
			t.Errorf("seau %q refusé par la base: %v", bucket, err)
		}
	}

	if err := db.UpsertRedmineIssue(ctx, RedmineIssue{
		ProjectID: testProjectID,
		RedmineID: 200, Tracker: "Évolution", Subject: "s",
		StatusName: "x", StatusBucket: "invente",
	}); err == nil {
		t.Error("un seau inventé devrait être refusé par la contrainte")
	}
}

// La liste ordonnée est la source unique : les colonnes de l'écran, le sélecteur
// d'administration et la contrainte de la base doivent dire la même chose.
func TestLaListeOrdonneeDesSeauxEstLaSourceUnique(t *testing.T) {
	expected := []string{"todo", "doing", "client-test", "done", "rejected"}

	if len(Buckets) != 4 {
		t.Fatalf("%d seaux d'avancement, attendu 4 (rejected est hors avancement)", len(Buckets))
	}
	// L'ordre porte du sens : c'est celui des colonnes.
	if Buckets[1] != BucketDoing || Buckets[2] != BucketClientTest || Buckets[3] != BucketDone {
		t.Errorf("ordre %v : test client doit s'intercaler entre doing et done", Buckets)
	}

	for _, bucket := range expected {
		if !ValidBucket(bucket) {
			t.Errorf("seau %q devrait être admis", bucket)
		}
	}
	for _, bucket := range []string{"", "invente", "DONE", "client_test"} {
		if ValidBucket(bucket) {
			t.Errorf("seau %q ne devrait pas être admis", bucket)
		}
	}
}

// Le cas constaté sur v3.2.0 : huit anomalies affichées ouvertes, dont cinq au statut
// `Testé` — que Redmine déclare **non clos** alors qu'il est projeté en `done`.
//
// « Ouverte » se lit sur le seau projeté, jamais sur `is_closed`. Employer deux notions
// de « clos » dans le même écran est la faute de fond : la projection est celle que
// l'utilisateur règle et voit.
func TestUneAnomalieTermineeNestPasOuverteMemeSiRedmineLaDitNonClose(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")

	// Les trois réellement ouvertes.
	issueOf(t, db, 1, 805, "Anomalie", "Ready", BucketTodo, 0, false)
	issueOf(t, db, 2, 805, "Anomalie", "En cours", BucketDoing, 20, false)
	issueOf(t, db, 3, 805, "Anomalie", "En revue", BucketDoing, 80, false)

	// Cinq au statut `Testé` : projetées en `done`, mais non closes selon Redmine.
	// C'est précisément la combinaison qui les faisait compter comme ouvertes.
	for i := 0; i < 5; i++ {
		issueOf(t, db, int64(10+i), 805, "Anomalie", "Testé", BucketDone, 100, false)
	}

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if summary.OpenAnomalies != 3 {
		t.Errorf("%d anomalies ouvertes, attendu 3 : les cinq en `done` ne le sont pas",
			summary.OpenAnomalies)
	}
}

// Une anomalie en attente de validation client n'est plus à traiter par l'équipe : elle
// ne compte pas comme ouverte.
func TestUneAnomalieEnTestClientNestPasOuverte(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Anomalie", "En cours", BucketDoing, 0, false)
	issueOf(t, db, 2, 805, "Anomalie", "Livré", BucketClientTest, 100, false)
	issueOf(t, db, 3, 805, "Anomalie", "Rejeté", BucketRejected, 0, true)

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if summary.OpenAnomalies != 1 {
		t.Errorf("%d anomalies ouvertes, attendu 1", summary.OpenAnomalies)
	}
}

// L'inverse du même écart : `Ré-ouvert` est déclaré **clos** par l'instance alors qu'il
// est projeté en `doing`. Une anomalie réouverte doit compter comme ouverte.
func TestUneAnomalieReouverteCompteCommeOuverte(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 805, "v3.2.0", "open")
	issueOf(t, db, 1, 805, "Anomalie", "Ré-ouvert", BucketDoing, 60, true)

	summary, err := db.GetVersionSummary(ctx, mustVersionID(t, db, "v3.2.0"))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if summary.OpenAnomalies != 1 {
		t.Errorf("%d anomalies ouvertes, attendu 1 malgré is_closed", summary.OpenAnomalies)
	}
}

// Les seaux ouverts sont todo et doing, et rien d'autre.
func TestSeulsTodoEtDoingSontOuverts(t *testing.T) {
	for _, bucket := range []string{BucketTodo, BucketDoing} {
		if !BucketIsOpen(bucket) {
			t.Errorf("%q devrait être ouvert", bucket)
		}
	}
	for _, bucket := range []string{BucketClientTest, BucketDone, BucketRejected, "invente"} {
		if BucketIsOpen(bucket) {
			t.Errorf("%q ne devrait pas être ouvert", bucket)
		}
	}
}

// Le rattachement immédiat : quand la version Redmine existe déjà, une release taguée
// `3.2.0-rc1` — base `3.2.0` — doit s'accrocher à `v3.2.0` directement. Chercher le nom
// exact seul créait une provisoire fantôme, adoptée cinq minutes plus tard ; entre-temps
// l'écran montrait la release accrochée à une version « inconnue de Redmine » qui
// existait bel et bien.
func TestEnsureVersionRattacheAUneVersionReelleParNomNormalise(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	realID := redmineVersion(t, db, 805, "v3.2.0", "open")

	id, err := db.EnsureVersion(ctx, testProjectID, "3.2.0")
	if err != nil {
		t.Fatalf("rattachement: %v", err)
	}
	if id != realID {
		t.Errorf("rattaché à %d, attendu la version réelle %d", id, realID)
	}

	// Aucune provisoire ne doit être née de ce rattachement.
	pendings, err := db.PendingVersions(ctx)
	if err != nil {
		t.Fatalf("provisoires: %v", err)
	}
	if len(pendings) != 0 {
		t.Errorf("provisoires créées à tort: %v", pendings)
	}
}

// La même préférence que l'adoption : entre l'homonyme close de l'ancien système et la
// version active, la release va à l'active. Les deux chemins — rattachement immédiat et
// adoption différée — doivent désigner la même version, sans quoi le résultat dépendrait
// du moment où la release est créée.
func TestEnsureVersionPrefereLaVersionActiveALHomonymeClose(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 803, "MSE 3.1.2", "closed")
	activeID := redmineVersion(t, db, 804, "v3.1.2", "open")

	id, err := db.EnsureVersion(ctx, testProjectID, "3.1.2")
	if err != nil {
		t.Fatalf("rattachement: %v", err)
	}
	if id != activeID {
		t.Errorf("rattaché à %d, attendu la version active %d", id, activeID)
	}
}

// Deux versions actives homonymes : indécidable, donc provisoire — choisir au hasard
// rattacherait la release à la mauvaise version, et la provisoire reste visible.
func TestEnsureVersionRetombeSurUneProvisoireQuandCestIndecidable(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 803, "MSE 3.1.2", "open")
	redmineVersion(t, db, 804, "v3.1.2", "open")

	if _, err := db.EnsureVersion(ctx, testProjectID, "3.1.2"); err != nil {
		t.Fatalf("rattachement: %v", err)
	}

	pendings, err := db.PendingVersions(ctx)
	if err != nil {
		t.Fatalf("provisoires: %v", err)
	}
	if len(pendings) != 1 || pendings[0] != "3.1.2" {
		t.Errorf("provisoires: %v, attendu la seule 3.1.2", pendings)
	}
}

// Le chaînon qui manquait : la conclusion d'une construction doit pouvoir faire
// avancer la release, et l'écriture doit être conditionnée à l'état lu.
func TestUnJobPorteSaReleaseEtSonEtat(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc1", Kind: "pre", State: "building",
		GitRef: "develop", ImageVersion: "3.2.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	jobID, err := db.Enqueue(ctx, JobRequest{
		Kind: "build", Params: map[string]string{"revision": "develop", "version": "3.2.0-rc1"},
		RequestedBy: "vcarroy", ReleaseID: &releaseID,
	})
	if err != nil {
		t.Fatalf("mise en file: %v", err)
	}

	linked, err := db.JobRelease(ctx, jobID)
	if err != nil {
		t.Fatalf("release du job: %v", err)
	}
	if linked.Kind != "build" || linked.Tag != "3.2.0-rc1" || linked.State != "building" {
		t.Errorf("contexte: %+v", linked)
	}
}

// Un job sans release n'est pas une erreur : un lancement manuel de tests depuis
// la file n'en désigne aucune, et le traiter comme une panne remplirait les
// journaux de faux problèmes.
func TestUnJobSansReleaseSeDistingueDuneErreur(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	jobID, err := db.Enqueue(ctx, JobRequest{
		Kind: "static-tests", RequestedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("mise en file: %v", err)
	}

	if _, err := db.JobRelease(ctx, jobID); !errors.Is(err, ErrJobWithoutRelease) {
		t.Errorf("erreur %v, attendu ErrJobWithoutRelease", err)
	}
}

// L'écriture est conditionnée à l'état lu : entre la mise en file et la fin du
// run, une reprise manuelle a pu déplacer la release, et l'écraser effacerait
// cette décision au profit d'un pipeline qui n'était plus le dernier mot.
func TestLAvancementNecrasePasUnEtatDeplaceEntreTemps(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc1", Kind: "pre", State: "building",
		GitRef: "develop", ImageVersion: "3.2.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	// Quelqu'un déplace la release pendant que le pipeline tourne.
	if err := db.SetReleaseState(ctx, releaseID, "failed"); err != nil {
		t.Fatalf("déplacement: %v", err)
	}

	// La conclusion arrive avec l'état qu'elle avait lu au départ.
	advanced, err := db.AdvanceReleaseState(ctx, releaseID, "building", "ready")
	if err != nil {
		t.Fatalf("avancement: %v", err)
	}
	if advanced {
		t.Error("l'avancement ne devait pas aboutir sur un état déplacé")
	}

	current, err := db.GetReleaseByID(ctx, releaseID)
	if err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if current.State != "failed" {
		t.Errorf("état %q, attendu failed conservé", current.State)
	}

	// Depuis l'état réel, l'avancement passe.
	advanced, err = db.AdvanceReleaseState(ctx, releaseID, "failed", "building")
	if err != nil {
		t.Fatalf("avancement: %v", err)
	}
	if !advanced {
		t.Error("l'avancement depuis l'état réel devait aboutir")
	}
}

// Une release ne sort de `verifying` que lorsque **toutes** ses vérifications ont
// rendu leur verdict. Sans ce décompte, elle y restait indéfiniment — constaté sur
// 3.2.0-rc1, bloquée alors que ses deux tests avaient conclu.
func TestUneReleaseNeSortDeVerifyingQueQuandToutAConclu(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc1", Kind: "pre", State: "verifying",
		GitRef: "main", GitCommit: "abc123", ImageVersion: "3.2.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}

	kinds := []string{"static-tests", "security-audit"}
	var jobs []int64
	for _, kind := range kinds {
		id, err := db.Enqueue(ctx, JobRequest{
			Kind: kind, RequestedBy: "dashboard:verification",
			ReleaseID: &releaseID,
		})
		if err != nil {
			t.Fatalf("mise en file %s: %v", kind, err)
		}
		jobs = append(jobs, id)
	}

	// Les deux en attente : rien n'est conclu.
	tally, err := db.CountVerifications(ctx, releaseID, kinds, "build")
	if err != nil {
		t.Fatalf("décompte: %v", err)
	}
	if tally.Done() || tally.Pending != 2 {
		t.Fatalf("décompte initial: %+v", tally)
	}

	// La première conclut : toujours pas fini.
	if err := db.FinishJob(ctx, jobs[0], JobSucceeded, ""); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	tally, _ = db.CountVerifications(ctx, releaseID, kinds, "build")
	if tally.Done() {
		t.Error("une vérification sur deux ne conclut pas la release")
	}

	// La seconde échoue : fini, mais avec un échec.
	if err := db.FinishJob(ctx, jobs[1], JobFailed, "clone refusé"); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	tally, _ = db.CountVerifications(ctx, releaseID, kinds, "build")
	if !tally.Done() {
		t.Error("toutes les vérifications ont conclu")
	}
	if tally.Failed != 1 || tally.Succeeded != 1 {
		t.Errorf("décompte final: %+v", tally)
	}
}

// Les jobs d'une autre release, ou d'une autre nature, ne comptent pas.
func TestLeDecompteNeRegardeQueLaBonneRelease(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	mine, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc1", Kind: "pre", State: "verifying",
		ImageVersion: "3.2.0-rc1", CreatedBy: "vcarroy",
	})
	other, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc2", Kind: "pre", State: "verifying",
		ImageVersion: "3.2.0-rc2", CreatedBy: "vcarroy",
	})

	// La construction vient d'abord, comme en production : c'est elle qui borne
	// la série de vérifications qui la suit.
	if _, err := db.Enqueue(ctx, JobRequest{
		Kind: "build", RequestedBy: "x", ReleaseID: &mine,
	}); err != nil {
		t.Fatalf("construction: %v", err)
	}
	if _, err := db.Enqueue(ctx, JobRequest{
		Kind: "static-tests", RequestedBy: "x", ReleaseID: &other,
	}); err != nil {
		t.Fatalf("job d'une autre release: %v", err)
	}

	kinds := []string{"static-tests", "security-audit"}
	tally, err := db.CountVerifications(ctx, mine, kinds, "build")
	if err != nil {
		t.Fatalf("décompte: %v", err)
	}
	if tally.Pending != 0 || tally.Failed != 0 || tally.Succeeded != 0 {
		t.Errorf("décompte: %+v — ni la construction ni la release voisine ne comptent", tally)
	}
	// Et une série vide n'est pas conclue : entre l'écriture de `verifying` et la
	// mise en file des tests, la série l'est momentanément, et un verdict tardif
	// redélivré à cet instant ferait passer la release en `ready` sans qu'aucun
	// test n'ait tourné.
	if tally.Done() {
		t.Error("une série vide ne conclut rien")
	}
}

// Une release relancée après un échec traîne les vérifications de ses tentatives
// précédentes. Les compter ferait échouer indéfiniment une release dont la
// seconde série passe entièrement — et le premier geste offert par l'interface
// sur une release en échec est précisément « relancer la construction ».
func TestUneRelanceNeCompteQueSesPropresVerifications(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc1", Kind: "pre", State: "verifying",
		GitRef: "main", GitCommit: "abc123", ImageVersion: "3.2.0-rc1", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	kinds := []string{"static-tests", "security-audit"}

	enqueue := func(kind string) int64 {
		t.Helper()
		id, err := db.Enqueue(ctx, JobRequest{
			Kind: kind, RequestedBy: "dashboard", ReleaseID: &releaseID,
		})
		if err != nil {
			t.Fatalf("mise en file %s: %v", kind, err)
		}
		return id
	}

	// Première série : la construction passe, l'audit échoue.
	enqueue("build")
	first := []int64{enqueue("static-tests"), enqueue("security-audit")}
	if err := db.FinishJob(ctx, first[0], JobSucceeded, ""); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	if err := db.FinishJob(ctx, first[1], JobFailed, "clone refusé"); err != nil {
		t.Fatalf("conclusion: %v", err)
	}

	// Seconde série, après relance : tout passe.
	enqueue("build")
	second := []int64{enqueue("static-tests"), enqueue("security-audit")}
	for _, id := range second {
		if err := db.FinishJob(ctx, id, JobSucceeded, ""); err != nil {
			t.Fatalf("conclusion: %v", err)
		}
	}

	tally, err := db.CountVerifications(ctx, releaseID, kinds, "build")
	if err != nil {
		t.Fatalf("décompte: %v", err)
	}
	if !tally.Done() {
		t.Fatal("la seconde série a conclu")
	}
	if tally.Failed != 0 || tally.Succeeded != 2 {
		t.Errorf("décompte: %+v — l'échec de la première série est encore compté", tally)
	}
}

// Le défaut qui laissait 3.2.0-rc3 en échec alors que ses trois familles étaient
// vertes : le décompte cumulait les essais d'une même famille. Trois échecs
// d'intégration suivis d'une réussite comptaient trois échecs, et la release
// restait en échec pour des verdicts que le dernier essai avait remplacés — donc
// le bouton « Rejouer » ne pouvait rien rattraper.
func TestSeulLeDernierEssaiDUneFamilleCompte(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, err := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc3", Kind: "pre", State: "failed",
		GitRef: "main", GitCommit: "abc123", ImageVersion: "3.2.0-rc3", CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	kinds := []string{"static-tests", "security-audit", "integration-tests"}

	enqueue := func(kind string) int64 {
		t.Helper()
		id, err := db.Enqueue(ctx, JobRequest{
			Kind: kind, RequestedBy: "dashboard", ReleaseID: &releaseID,
		})
		if err != nil {
			t.Fatalf("mise en file %s: %v", kind, err)
		}
		return id
	}
	conclude := func(id int64, state string) {
		t.Helper()
		if err := db.FinishJob(ctx, id, state, ""); err != nil {
			t.Fatalf("conclusion: %v", err)
		}
	}

	enqueue("build")
	conclude(enqueue("static-tests"), JobSucceeded)
	conclude(enqueue("security-audit"), JobSucceeded)

	// L'intégration échoue trois fois, puis passe.
	for i := 0; i < 3; i++ {
		conclude(enqueue("integration-tests"), JobFailed)
	}
	conclude(enqueue("integration-tests"), JobSucceeded)

	tally, err := db.CountVerifications(ctx, releaseID, kinds, "build")
	if err != nil {
		t.Fatalf("décompte: %v", err)
	}
	if !tally.Done() {
		t.Fatal("les trois familles ont conclu")
	}
	if tally.Failed != 0 {
		t.Errorf("%d échec(s) comptés — les essais précédents pèsent encore", tally.Failed)
	}
	if tally.Succeeded != 3 {
		t.Errorf("%d réussite(s), attendu une par famille", tally.Succeeded)
	}
}

// Et le pendant : un dernier essai en échec compte, même précédé de réussites.
func TestUnDernierEssaiEnEchecCompte(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 805, "v3.2.0", "open")
	releaseID, _ := db.CreateRelease(ctx, Release{
		ProjectID: testProjectID,
		VersionID: versionID, Tag: "3.2.0-rc4", Kind: "pre", State: "verifying",
		ImageVersion: "3.2.0-rc4", CreatedBy: "vcarroy",
	})

	enqueue := func(kind string) int64 {
		id, err := db.Enqueue(ctx, JobRequest{
			Kind: kind, RequestedBy: "dashboard", ReleaseID: &releaseID,
		})
		if err != nil {
			t.Fatalf("mise en file: %v", err)
		}
		return id
	}

	enqueue("build")
	first := enqueue("static-tests")
	if err := db.FinishJob(ctx, first, JobSucceeded, ""); err != nil {
		t.Fatalf("conclusion: %v", err)
	}
	second := enqueue("static-tests")
	if err := db.FinishJob(ctx, second, JobFailed, "régression"); err != nil {
		t.Fatalf("conclusion: %v", err)
	}

	tally, err := db.CountVerifications(ctx, releaseID, []string{"static-tests"}, "build")
	if err != nil {
		t.Fatalf("décompte: %v", err)
	}
	if tally.Failed != 1 || tally.Succeeded != 0 {
		t.Errorf("décompte %+v — le dernier essai doit l'emporter", tally)
	}
}

// Deux projets suivis ensemble portent chacun leur `3.1.2`, et rien ne doit les
// mêler.
//
// C'est la raison d'être de la migration 0025, et la propriété la plus facile à
// perdre sans s'en apercevoir : le rapprochement se fait par nom **normalisé**,
// et une requête qui oublie le projet rattache silencieusement la release de l'un
// à la version de l'autre. Les deux existent, aucune erreur n'est levée, et
// l'écran affiche un périmètre qui n'est pas le sien.
func TestDeuxProjetsNeSeVolentPasLeursVersions(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	var second int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, dockerfile, build_context, created_by)
		 values ('autre', 'vcarroy/autre', 'Autre', 'https://forge.example', 'Dockerfile', '.', 'test')
		 returning id`).Scan(&second); err != nil {
		t.Fatalf("second projet: %v", err)
	}

	// Le même numéro, déclaré des deux côtés. Le même identifiant Redmine aussi :
	// deux projets peuvent suivre deux instances, ou deux projets de la même.
	for _, projectID := range []int64{testProjectID, second} {
		if _, err := db.UpsertRedmineVersion(ctx, RedmineVersion{
			ProjectID: projectID, RedmineID: 900, Name: "v3.1.2",
			Normalized: testNames.Normalize("v3.1.2"), Status: "open",
		}); err != nil {
			t.Fatalf("version du projet %d: %v", projectID, err)
		}
	}

	// Une release du premier projet doit trouver **sa** version.
	versionID, err := db.EnsureVersion(ctx, testProjectID, "3.1.2")
	if err != nil {
		t.Fatalf("rattachement: %v", err)
	}

	var owner int64
	if err := db.Pool().QueryRow(ctx,
		`select project_id from versions where id = $1`, versionID).Scan(&owner); err != nil {
		t.Fatalf("lecture du projet: %v", err)
	}
	if owner != testProjectID {
		t.Errorf("version rattachée au projet %d, attendu %d", owner, testProjectID)
	}

	// Et le tag n'est unique qu'au sein d'un projet : le même `1.0.0` doit
	// pouvoir exister des deux côtés.
	for _, projectID := range []int64{testProjectID, second} {
		version, err := db.EnsureVersion(ctx, projectID, "1.0.0")
		if err != nil {
			t.Fatalf("version 1.0.0 du projet %d: %v", projectID, err)
		}
		if _, err := db.CreateRelease(ctx, Release{
			ProjectID: projectID, VersionID: version, Tag: "1.0.0",
			Kind: "final", State: "draft", ImageVersion: "1.0.0", CreatedBy: "test",
		}); err != nil {
			t.Fatalf("release 1.0.0 du projet %d: %v", projectID, err)
		}
	}

	first, err := db.GetRelease(ctx, testProjectID, "1.0.0")
	if err != nil {
		t.Fatalf("lecture de la release du premier projet: %v", err)
	}
	other, err := db.GetRelease(ctx, second, "1.0.0")
	if err != nil {
		t.Fatalf("lecture de la release du second projet: %v", err)
	}
	if first.ID == other.ID {
		t.Error("les deux projets partagent la même release: l'unicité du tag est restée globale")
	}
}

// Un projet sans outil de suivi produit des versions **définitives**, jamais des
// provisoires.
//
// C'est le cas de `hydra-sql-mse-theme` et `hydra-dispatcher-mse-theme` : ils
// versionnent par tags Git, sans Redmine ni équivalent. Le dashboard leur
// fabriquait une provisoire en attente d'une adoption qui ne serait jamais venue
// — l'état d'anomalie devenu permanent, et un écran qui annonce indéfiniment un
// retard sur un outil inexistant.
func TestUnProjetSansSuiviProduitDesVersionsDefinitives(t *testing.T) {
	db := migratedBare(t)
	ctx := context.Background()

	var sansSuivi int64
	if err := db.Pool().QueryRow(ctx,
		`insert into projects (slug, repository, label, forge_url, build_context, created_by)
		 values ('theme', 'CNOUS/hydra-sql-mse-theme', 'Thème', 'https://forge.example', '.', 'test')
		 returning id`).Scan(&sansSuivi); err != nil {
		t.Fatalf("projet sans suivi: %v", err)
	}

	versionID, err := db.EnsureVersion(ctx, sansSuivi, "1.2.0")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	var origin, status string
	if err := db.Pool().QueryRow(ctx,
		`select origin, status from versions where id = $1`, versionID).Scan(&origin, &status); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if origin != "own" {
		t.Errorf("origine = %q, attendu own", origin)
	}
	// Le statut suit l'origine : une version qui se suffit à elle-même n'attend
	// rien, et l'afficher « en attente » serait faux.
	if status != "open" {
		t.Errorf("statut = %q, attendu open", status)
	}

	// Et l'adoption ne doit pas la regarder : il n'y a rien à rapprocher.
	report, err := db.AdoptPendingVersions(ctx)
	if err != nil {
		t.Fatalf("adoption: %v", err)
	}
	if len(report) != 0 {
		t.Errorf("l'adoption a examiné une version sans suivi: %+v", report)
	}
}

// Et le pendant : un projet suivi produit bien une provisoire, que la
// synchronisation pourra adopter.
func TestUnProjetSuiviProduitUneProvisoire(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID, err := db.EnsureVersion(ctx, testProjectID, "3.2.0")
	if err != nil {
		t.Fatalf("version: %v", err)
	}

	var origin string
	if err := db.Pool().QueryRow(ctx,
		`select origin from versions where id = $1`, versionID).Scan(&origin); err != nil {
		t.Fatalf("relecture: %v", err)
	}
	if origin != "pending" {
		t.Errorf("origine = %q, attendu pending", origin)
	}
}
