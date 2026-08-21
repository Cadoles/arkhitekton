package store

import (
	"context"
	"errors"
	"testing"
)

/*
 * Ces tests existent pour une raison précise : les requêtes de note nommaient une
 * table qui n'existe pas — `redmine_issues` au lieu d'`issues`. Rien ne l'a vu,
 * parce que rien ne les exécutait contre une vraie base. Le défaut n'est apparu
 * qu'à l'écran, en 500, et le message d'erreur ne désignait pas la cause.
 *
 * Une requête SQL ne se relit pas : elle s'exécute.
 */

func noteRelease(t *testing.T, db *DB, versionID int64, tag string) int64 {
	t.Helper()
	id, err := db.CreateRelease(context.Background(), Release{
		VersionID: versionID, Tag: tag, Kind: "pre", State: "ready",
		ImageVersion: tag, CreatedBy: "vcarroy",
	})
	if err != nil {
		t.Fatalf("release %s: %v", tag, err)
	}
	return id
}

// Le parcours complet : rattacher, lire, retirer.
func TestUneDemandeSeRattacheEtSeRetireDUneNote(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")
	issueOf(t, db, 1234, 920, "Évolution", "En revue", BucketDoing, 100, false)

	if err := db.LinkNoteIssue(ctx, releaseID, 1234, NoteSourceAuto, "#42", "vcarroy"); err != nil {
		t.Fatalf("rattachement: %v", err)
	}

	entries, err := db.ListNoteEntries(ctx, releaseID)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entrée(s)", len(entries))
	}
	entry := entries[0]
	// Le contenu vient d'`issues` par jointure : c'est ce qui fait qu'un sujet
	// corrigé dans Redmine apparaît sans qu'on régénère la note.
	if !entry.Known || entry.Subject != "sujet" || entry.Tracker != "Évolution" {
		t.Errorf("entrée jointe: %+v", entry)
	}
	if entry.Source != NoteSourceAuto || entry.PullRequest != "#42" {
		t.Errorf("provenance: %+v", entry)
	}
	if entry.Anomaly {
		t.Error("une évolution n'est pas une anomalie")
	}

	if err := db.UnlinkNoteIssue(ctx, releaseID, 1234); err != nil {
		t.Fatalf("retrait: %v", err)
	}
	if remaining, _ := db.ListNoteEntries(ctx, releaseID); len(remaining) != 0 {
		t.Errorf("%d entrée(s) après retrait", len(remaining))
	}
}

// Une demande ne figure qu'une fois : un second passage de détection la
// doublerait, et la note compterait deux fois la même chose.
func TestUneDemandeNeSeRattachePasDeuxFois(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")

	if err := db.LinkNoteIssue(ctx, releaseID, 1234, NoteSourceAuto, "#42", "vcarroy"); err != nil {
		t.Fatalf("premier rattachement: %v", err)
	}
	err := db.LinkNoteIssue(ctx, releaseID, 1234, NoteSourceManual, "", "vcarroy")
	if !errors.Is(err, ErrNoteAlreadyLinked) {
		t.Errorf("second rattachement: %v, attendu ErrNoteAlreadyLinked", err)
	}
}

// Une demande rattachée que la synchronisation n'a pas encore vue reste listée,
// sans son sujet. La masquer ferait disparaître un ajout manuel, ce qui se lirait
// comme une perte.
func TestUneDemandeInconnueResteListee(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")

	if err := db.LinkNoteIssue(ctx, releaseID, 9999, NoteSourceManual, "", "vcarroy"); err != nil {
		t.Fatalf("rattachement: %v", err)
	}

	entries, err := db.ListNoteEntries(ctx, releaseID)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("%d entrée(s)", len(entries))
	}
	if entries[0].Known {
		t.Error("la demande devrait être marquée inconnue")
	}
}

// Les anomalies sont reconnues au préfixe du tracker : Redmine porte `Anomalie`
// et ses déclinaisons, toutes des corrections.
func TestUneAnomalieEstReconnueAuPrefixeDuTracker(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")
	issueOf(t, db, 1, 920, "Anomalie bloquante", "En revue", BucketDoing, 100, false)

	if err := db.LinkNoteIssue(ctx, releaseID, 1, NoteSourceAuto, "#1", "vcarroy"); err != nil {
		t.Fatalf("rattachement: %v", err)
	}
	entries, _ := db.ListNoteEntries(ctx, releaseID)
	if len(entries) != 1 || !entries[0].Anomaly {
		t.Errorf("entrées: %+v — `Anomalie bloquante` est une correction", entries)
	}
}

// Le vivier de rattachement : les demandes de la version de la release.
func TestLeVivierRendLesDemandesDeLaVersion(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	versionID := redmineVersion(t, db, 920, "v3.2.0", "open")
	releaseID := noteRelease(t, db, versionID, "3.2.0-rc1")

	issueOf(t, db, 1, 920, "Évolution", "En revue", BucketDoing, 100, false)
	issueOf(t, db, 2, 920, "Anomalie", "Terminé", BucketDone, 100, true)
	// Rejetée : elle n'est pas livrée, et la proposer inviterait à annoncer au
	// client ce qui a été abandonné.
	issueOf(t, db, 3, 920, "Évolution", "Rejeté", BucketRejected, 0, true)
	// D'une autre version : hors du vivier.
	otherVersion := redmineVersion(t, db, 921, "v3.3.0", "open")
	_ = otherVersion
	issueOf(t, db, 4, 921, "Évolution", "En revue", BucketDoing, 100, false)

	entries, err := db.ListReleaseVersionIssues(ctx, releaseID)
	if err != nil {
		t.Fatalf("vivier: %v", err)
	}

	got := map[int64]bool{}
	for _, entry := range entries {
		got[entry.RedmineID] = true
	}
	if !got[1] || !got[2] {
		t.Errorf("demandes rendues: %v — la version en porte deux non rejetées", got)
	}
	if got[3] {
		t.Error("une demande rejetée ne doit pas être proposée")
	}
	if got[4] {
		t.Error("une demande d'une autre version ne doit pas être proposée")
	}
}

// Les candidates à la détection : celles dont le statut les rend éligibles.
func TestLesCandidatesSontFiltreesParStatut(t *testing.T) {
	db := migrated(t)
	ctx := context.Background()

	redmineVersion(t, db, 920, "v3.2.0", "open")
	issueOf(t, db, 1, 920, "Évolution", "En revue", BucketDoing, 100, false)
	issueOf(t, db, 2, 920, "Évolution", "En cours", BucketDoing, 50, false)

	candidates, err := db.NoteCandidates(ctx, []string{"En revue"})
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("%d candidate(s): %+v", len(candidates), candidates)
	}
	if _, ok := candidates[1]; !ok {
		t.Error("la demande en revue devrait être candidate")
	}
}
