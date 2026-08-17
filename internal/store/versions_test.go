package store

import (
	"context"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/release"
)

// redmineVersion dépose une version réelle, comme le ferait la synchronisation.
func redmineVersion(t *testing.T, db *DB, redmineID int64, name, status string) int64 {
	t.Helper()
	id, err := db.UpsertRedmineVersion(context.Background(), RedmineVersion{
		RedmineID: redmineID, Name: name,
		Normalized: release.NormalizeVersionName(name),
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
	pendingID, err := db.EnsureVersion(ctx, "3.2.0")
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

	pendingID, err := db.EnsureVersion(ctx, "3.1.2")
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

	pendingID, _ := db.EnsureVersion(ctx, "3.1.3")
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

	pendingID, err := db.EnsureVersion(ctx, "3.1.2")
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

		pendingID, _ := db.EnsureVersion(ctx, "3.1.2")
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

	if _, err := db.EnsureVersion(ctx, "9.9.9"); err != nil {
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
			RedmineID: int64(100 + i), Tracker: "Évolution", Subject: "s",
			StatusName: "x", StatusBucket: bucket,
		}); err != nil {
			t.Errorf("seau %q refusé par la base: %v", bucket, err)
		}
	}

	if err := db.UpsertRedmineIssue(ctx, RedmineIssue{
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

	id, err := db.EnsureVersion(ctx, "3.2.0")
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

	id, err := db.EnsureVersion(ctx, "3.1.2")
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

	if _, err := db.EnsureVersion(ctx, "3.1.2"); err != nil {
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
