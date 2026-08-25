package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

type fakeRedmine struct {
	versions []provider.Version
	issues   []provider.Issue
	queries  []provider.IssueQuery
	err      error
}

func (f *fakeRedmine) ListVersions(context.Context, string) ([]provider.Version, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.versions, nil
}

func (f *fakeRedmine) ListIssues(_ context.Context, query provider.IssueQuery) ([]provider.Issue, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.queries = append(f.queries, query)
	return f.issues, nil
}

type fakeRedmineStore struct {
	versions  []store.RedmineVersion
	issues    []store.RedmineIssue
	mapping   map[string]string
	state     store.SyncState
	successes []string
	failures  []string
	adoptions []store.Adoption
	adopted   int
}

func (f *fakeRedmineStore) UpsertRedmineVersion(_ context.Context, version store.RedmineVersion) (int64, error) {
	f.versions = append(f.versions, version)
	return version.RedmineID, nil
}

func (f *fakeRedmineStore) UpsertRedmineIssue(_ context.Context, issue store.RedmineIssue) error {
	f.issues = append(f.issues, issue)
	return nil
}

func (f *fakeRedmineStore) AdoptPendingVersions(context.Context) ([]store.Adoption, error) {
	f.adopted++
	return f.adoptions, nil
}

func (f *fakeRedmineStore) StatusMapping(context.Context) (map[string]string, error) {
	return f.mapping, nil
}

func (f *fakeRedmineStore) GetSyncState(context.Context, string) (store.SyncState, error) {
	return f.state, nil
}

func (f *fakeRedmineStore) RecordSyncSuccess(_ context.Context, _, cursor string) error {
	f.successes = append(f.successes, cursor)
	return nil
}

func (f *fakeRedmineStore) RecordSyncFailure(_ context.Context, _, message string) error {
	f.failures = append(f.failures, message)
	return nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newSync(t *testing.T, client *fakeRedmine, db *fakeRedmineStore) *RedmineSync {
	t.Helper()
	if db.mapping == nil {
		db.mapping = map[string]string{"En cours": "doing", "Fermé": "done", "Rejeté": "rejected"}
	}
	return NewRedmineSync(db, client, "mse-dev", quietLogger())
}

// Les versions avant les demandes : une demande se rattache à sa version par
// `redmine_id`, et l'inverse laisserait sans rattachement toute demande visant une
// version nouvellement créée.
func TestLesVersionsSontDeposeesAvantLesDemandes(t *testing.T) {
	version := int64(805)
	client := &fakeRedmine{
		versions: []provider.Version{{ID: 805, Name: "v3.2.0", Status: "open"}},
		issues: []provider.Issue{
			{ID: 1, VersionID: &version, Tracker: "Évolution", Status: "En cours"},
		},
	}
	db := &fakeRedmineStore{}

	if err := newSync(t, client, db).Sync(context.Background()); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(db.versions) != 1 || db.versions[0].Normalized != "3.2.0" {
		t.Errorf("versions déposées: %+v", db.versions)
	}
	if len(db.issues) != 1 || db.issues[0].VersionRedmineID == nil {
		t.Fatalf("demandes déposées: %+v", db.issues)
	}
	// L'adoption a lieu après le dépôt des versions, et une seule fois.
	if db.adopted != 1 {
		t.Errorf("%d adoptions, attendu 1", db.adopted)
	}
}

// Le nom est normalisé pour le rapprochement, jamais réécrit : l'identité d'une
// version est son identifiant Redmine.
func TestLeNomEstNormalisePourLeRapprochementSansEtreReecrit(t *testing.T) {
	client := &fakeRedmine{versions: []provider.Version{
		{ID: 803, Name: "MSE 3.1.2", Status: "closed"},
		{ID: 805, Name: "v3.2.0", Status: "open"},
		{ID: 801, Name: "MSE3-1.5", Status: "closed"},
	}}
	db := &fakeRedmineStore{}

	if err := newSync(t, client, db).Sync(context.Background()); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	expected := map[string]string{
		"MSE 3.1.2": "3.1.2",
		"v3.2.0":    "3.2.0",
		// Convention historique laissée intacte : rien dans les données ne dit si
		// « MSE3-1.5 » se lit 1.5 ou 3.1.5.
		"MSE3-1.5": "MSE3-1.5",
	}
	for _, version := range db.versions {
		if want := expected[version.Name]; version.Normalized != want {
			t.Errorf("%q normalisé en %q, attendu %q", version.Name, version.Normalized, want)
		}
	}
}

// Un statut inconnu tombe dans `todo` et n'interrompt pas la passe : les statuts de
// l'instance changent sans prévenir, et refuser la demande perdrait le reste.
func TestUnStatutInconnuTombeDansTodoSansInterrompre(t *testing.T) {
	client := &fakeRedmine{issues: []provider.Issue{
		{ID: 1, Tracker: "Évolution", Status: "Statut Inédit"},
		{ID: 2, Tracker: "Évolution", Status: "En cours"},
	}}
	db := &fakeRedmineStore{}

	if err := newSync(t, client, db).Sync(context.Background()); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(db.issues) != 2 {
		t.Fatalf("%d demandes déposées", len(db.issues))
	}
	byID := map[int64]store.RedmineIssue{}
	for _, issue := range db.issues {
		byID[issue.RedmineID] = issue
	}
	if byID[1].StatusBucket != "todo" {
		t.Errorf("statut inconnu projeté en %q, attendu todo", byID[1].StatusBucket)
	}
	if byID[2].StatusBucket != "doing" {
		t.Errorf("statut connu projeté en %q", byID[2].StatusBucket)
	}
}

// Le curseur est pris **avant** la lecture. Le prendre après ferait du temps de
// lecture un angle mort : les demandes modifiées pendant celle-ci ne seraient ni dans
// cette passe ni dans la suivante.
func TestLeCurseurEstPrisAvantLaLecture(t *testing.T) {
	client := &fakeRedmine{}
	db := &fakeRedmineStore{}
	sync := newSync(t, client, db)

	debut := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	sync.now = func() time.Time { return debut }

	if err := sync.Sync(context.Background()); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(db.successes) != 1 {
		t.Fatalf("%d curseurs enregistrés", len(db.successes))
	}
	if got := db.successes[0]; got != debut.Format(time.RFC3339) {
		t.Errorf("curseur %q, attendu %q", got, debut.Format(time.RFC3339))
	}
}

// Une marge de recouvrement est retirée au curseur : Redmine filtre `updated_on` à la
// seconde et son horloge n'est pas la nôtre. Repartir de l'instant exact ferait
// manquer toute demande modifiée dans la même seconde que la fin de la passe
// précédente.
func TestLeCurseurEstRepriseAvecUneMargeDeRecouvrement(t *testing.T) {
	precedent := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	client := &fakeRedmine{}
	db := &fakeRedmineStore{state: store.SyncState{Cursor: precedent.Format(time.RFC3339)}}

	if err := newSync(t, client, db).Sync(context.Background()); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(client.queries) != 1 {
		t.Fatalf("%d requêtes", len(client.queries))
	}
	since := client.queries[0].Since
	if !since.Equal(precedent.Add(-time.Minute)) {
		t.Errorf("reprise à %v, attendu %v (une minute de recouvrement)",
			since, precedent.Add(-time.Minute))
	}
}

// Un curseur illisible conduit à relire tout : coûteux mais correct, là où repartir
// d'un instant erroné perdrait des demandes en silence.
func TestUnCurseurIllisibleFaitToutRelire(t *testing.T) {
	for _, cursor := range []string{"", "pas-une-date", "17/08/2026"} {
		client := &fakeRedmine{}
		db := &fakeRedmineStore{state: store.SyncState{Cursor: cursor}}

		if err := newSync(t, client, db).Sync(context.Background()); err != nil {
			t.Fatalf("curseur %q: %v", cursor, err)
		}
		if len(client.queries) != 1 {
			t.Fatalf("curseur %q: %d requêtes", cursor, len(client.queries))
		}
		if !client.queries[0].Since.IsZero() {
			t.Errorf("curseur %q: reprise à %v, attendu une lecture complète",
				cursor, client.queries[0].Since)
		}
	}
}

// Un échec est consigné et le curseur n'avance pas : l'avancer sauterait la fenêtre
// non lue.
func TestUnEchecEstConsigneSansAvancerLeCurseur(t *testing.T) {
	client := &fakeRedmine{err: errors.New("Redmine injoignable")}
	db := &fakeRedmineStore{}

	newSync(t, client, db).once(context.Background())

	if len(db.failures) != 1 {
		t.Fatalf("%d échecs consignés", len(db.failures))
	}
	if len(db.successes) != 0 {
		t.Errorf("le curseur a avancé malgré l'échec: %v", db.successes)
	}
}

// Une panne de Redmine ne doit pas emporter la boucle : c'est un service tiers, et sa
// panne n'a rien à voir avec l'ordonnancement ou l'observation.
func TestUnePanneDeRedmineNeTuePasLaBoucle(t *testing.T) {
	client := &fakeRedmine{err: errors.New("502 Bad Gateway")}
	db := &fakeRedmineStore{}
	sync := newSync(t, client, db)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sync.Run(ctx) }()

	// Le premier passage immédiat doit avoir consigné l'échec sans rendre la main.
	deadline := time.After(2 * time.Second)
	for {
		if len(db.failures) > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("la boucle s'est arrêtée sur un échec de Redmine: %v", err)
		case <-deadline:
			t.Fatal("aucun échec consigné")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("arrêt sur %v, attendu context.Canceled", err)
	}
}

// Le drapeau de clôture de Redmine est transmis tel quel, indépendamment du seau :
// l'instance déclare `Ré-ouvert` comme clos, et les deux informations sont distinctes.
func TestLeDrapeauDeClotureEstTransmisIndependammentDuSeau(t *testing.T) {
	client := &fakeRedmine{issues: []provider.Issue{
		{ID: 1, Tracker: "Anomalie", Status: "Ré-ouvert", StatusClosed: true},
	}}
	db := &fakeRedmineStore{mapping: map[string]string{"Ré-ouvert": "doing"}}

	if err := newSync(t, client, db).Sync(context.Background()); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(db.issues) != 1 {
		t.Fatalf("%d demandes", len(db.issues))
	}
	if !db.issues[0].IsClosed {
		t.Error("le drapeau de Redmine doit être conservé")
	}
	if db.issues[0].StatusBucket != "doing" {
		t.Errorf("seau %q, attendu doing", db.issues[0].StatusBucket)
	}
}
