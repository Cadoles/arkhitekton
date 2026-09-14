package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/event"
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

// fakeRedmineStore ne porte plus que la projection.
//
// Cinq méthodes ont disparu de cette fixture au passage — projets suivis,
// réglages du plugin, curseur, ses deux issues. Elles vivent dans `multiStore`,
// qui pilote la boucle. C'est la mesure la plus concrète de ce que la migration a
// déplacé.
type fakeRedmineStore struct {
	versions  []store.TrackedVersion
	issues    []store.TrackedIssue
	mapping   map[string]string
	state     store.SyncState
	adoptions []store.Adoption
	adopted   int
	// locales attribue une clé locale par identité, comme le fait l'unicité du
	// magasin réel. Voir le commentaire d'`UpsertRedmineVersion` ci-dessous.
	locales map[string]int64
}

// UpsertRedmineVersion rend une clé locale **par identité**, et c'est ce qui fait
// de cette fixture un modèle fidèle.
//
// Elle rendait l'identifiant de l'outil réinterprété en entier. La version 805 de
// deux projets recevait donc la même clé locale — impossible en base, où
// l'unicité porte sur `(project_id, tool, external_id)`. Le fait `version.created`
// étant désormais dédupliqué sur cette clé, la fixture aurait fait passer pour
// partagé un fait que le magasin réel distingue.
func (f *fakeRedmineStore) UpsertRedmineVersion(_ context.Context, version store.TrackedVersion) (int64, error) {
	f.versions = append(f.versions, version)

	if f.locales == nil {
		f.locales = map[string]int64{}
	}
	identite := fmt.Sprintf("%d/%s/%s", version.ProjectID, version.Tool, version.ExternalID)
	if id, connue := f.locales[identite]; connue {
		return id, nil
	}
	id := int64(len(f.locales) + 1)
	f.locales[identite] = id
	return id, nil
}

func (f *fakeRedmineStore) UpsertRedmineIssue(_ context.Context, issue store.TrackedIssue) error {
	f.issues = append(f.issues, issue)
	return nil
}

func (f *fakeRedmineStore) AdoptPendingVersions(context.Context) ([]store.Adoption, error) {
	f.adopted++
	return f.adoptions, nil
}

func (f *fakeRedmineStore) StatusMapping(context.Context, int64) (map[string]string, error) {
	return f.mapping, nil
}

// openerDeTest ouvre en retirant le préfixe de scellement : de quoi vérifier que
// la valeur descellée est bien celle qui atteint le client, sans faire dépendre
// ces tests d'un vrai chiffrement.
type openerDeTest struct{ err error }

func (o openerDeTest) Open(sealed string) (string, error) {
	if o.err != nil {
		return "", o.err
	}
	return strings.TrimPrefix(sealed, "scellé:"), nil
}

func (o openerDeTest) Configured() bool { return true }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newSync(t *testing.T, client *fakeRedmine, db *fakeRedmineStore) *TrackerSync {
	t.Helper()
	if db.mapping == nil {
		db.mapping = map[string]string{"En cours": "doing", "Fermé": "done", "Rejeté": "rejected"}
	}
	connect := func(TrackerConnection) (TrackerReader, error) { return client, nil }
	return NewTrackerSync(db, connect, quietLogger())
}

// passe exécute une passe de projection, comme la sonde le ferait.
//
// Elle remplace l'ancien `Sync` : la boucle est partie dans `probes.go`, et ce
// qui reste ici est la projection seule. Les assertions n'ont pas changé — c'est
// tout l'intérêt.
func passe(t *testing.T, client *fakeRedmine, db *fakeRedmineStore) ([]event.Event, string, error) {
	t.Helper()
	sync := newSync(t, client, db)
	return sync.Observe(context.Background(), 1, "mse-dev", client, db.state.Cursor)
}

// Les versions avant les demandes : une demande se rattache à sa version par
// son identifiant externe, et l'inverse laisserait sans rattachement toute demande visant une
// version nouvellement créée.
func TestLesVersionsSontDeposeesAvantLesDemandes(t *testing.T) {
	version := "805"
	client := &fakeRedmine{
		versions: []provider.Version{{ID: "805", Name: "v3.2.0", Status: "open"}},
		issues: []provider.Issue{
			{ID: "1", VersionID: &version, Tracker: "Évolution", Status: "En cours"},
		},
	}
	db := &fakeRedmineStore{}

	if _, _, err := passe(t, client, db); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(db.versions) != 1 || db.versions[0].Normalized != "3.2.0" {
		t.Errorf("versions déposées: %+v", db.versions)
	}
	if len(db.issues) != 1 || db.issues[0].VersionExternalID == nil {
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
		{ID: "803", Name: "MSE 3.1.2", Status: "closed"},
		{ID: "805", Name: "v3.2.0", Status: "open"},
		{ID: "801", Name: "MSE3-1.5", Status: "closed"},
	}}
	db := &fakeRedmineStore{}

	if _, _, err := passe(t, client, db); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	expected := map[string]string{
		"v3.2.0": "3.2.0",
		// Seul `v` est retiré (doc 16 §16.3) : les conventions historiques du suivi
		// restent intactes, et ne se rattachent à aucun tag.
		"MSE 3.1.2": "MSE 3.1.2",
		"MSE3-1.5":  "MSE3-1.5",
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
		{ID: "1", Tracker: "Évolution", Status: "Statut Inédit"},
		{ID: "2", Tracker: "Évolution", Status: "En cours"},
	}}
	db := &fakeRedmineStore{}

	if _, _, err := passe(t, client, db); err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if len(db.issues) != 2 {
		t.Fatalf("%d demandes déposées", len(db.issues))
	}
	byID := map[string]store.TrackedIssue{}
	for _, issue := range db.issues {
		byID[issue.ExternalID] = issue
	}
	if byID["1"].StatusBucket != "todo" {
		t.Errorf("statut inconnu projeté en %q, attendu todo", byID["1"].StatusBucket)
	}
	if byID["2"].StatusBucket != "doing" {
		t.Errorf("statut connu projeté en %q", byID["2"].StatusBucket)
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

	// Le curseur est **rendu** plutôt qu'écrit : depuis la migration, c'est la
	// boucle des sondes qui le persiste. Ce que la projection doit tenir, c'est
	// l'instant qu'elle choisit — et il reste celui d'avant la lecture.
	_, cursor, err := sync.Observe(context.Background(), 1, "mse-dev", client, "")
	if err != nil {
		t.Fatalf("synchronisation: %v", err)
	}

	if cursor != debut.Format(time.RFC3339) {
		t.Errorf("curseur %q, attendu %q", cursor, debut.Format(time.RFC3339))
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

	if _, _, err := passe(t, client, db); err != nil {
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

		if _, _, err := passe(t, client, db); err != nil {
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
func TestUnEchecNavancePasLeCurseur(t *testing.T) {
	client := &fakeRedmine{err: errors.New("Redmine injoignable")}
	db := &fakeRedmineStore{}

	facts, cursor, err := newSync(t, client, db).
		Observe(context.Background(), 1, "mse-dev", client, "")

	if err == nil {
		t.Fatal("une panne du suivi doit faire échouer la passe")
	}
	// Aucun curseur rendu : la boucle n'a donc rien à avancer, et la passe
	// suivante reverra la fenêtre non lue. C'est la même garantie qu'avant la
	// migration, obtenue en ne rendant rien plutôt qu'en n'écrivant rien.
	if cursor != "" {
		t.Errorf("curseur rendu malgré l'échec: %q", cursor)
	}
	if len(facts) != 0 {
		t.Errorf("%d faits rendus malgré l'échec", len(facts))
	}
}

// Une panne de Redmine ne doit pas emporter la boucle : c'est un service tiers, et sa
// panne n'a rien à voir avec l'ordonnancement ou l'observation.
//
// # Ce test a changé de sujet, et c'est le signe que la migration a pris
//
// Il pilotait `TrackerSync.Run`, sa propre boucle, sa propre isolation des
// échecs. Celle-ci n'existe plus : c'est `ProbeRunner` qui la tient, pour toutes
// les sources, et `TestUneForgeInjoignableNarretePasLesAutresProjets` l'éprouve.
//
// Ce qui reste à la charge du suivi est ce qui est vérifié ici : **remonter**
// l'erreur, pour que la boucle ait quelque chose à isoler.
func TestUnePanneDeRedmineRemonteALaBoucle(t *testing.T) {
	client := &fakeRedmine{err: errors.New("502 Bad Gateway")}
	db := &fakeRedmineStore{}

	_, _, err := newSync(t, client, db).
		Observe(context.Background(), 1, "mse-dev", client, "")

	if err == nil {
		t.Fatal("la panne doit remonter, sans quoi la boucle croirait la passe réussie")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("message %q — il doit porter ce que le suivi a répondu", err)
	}
}

// Le drapeau de clôture de Redmine est transmis tel quel, indépendamment du seau :
// l'instance déclare `Ré-ouvert` comme clos, et les deux informations sont distinctes.
func TestLeDrapeauDeClotureEstTransmisIndependammentDuSeau(t *testing.T) {
	client := &fakeRedmine{issues: []provider.Issue{
		{ID: "1", Tracker: "Anomalie", Status: "Ré-ouvert", StatusClosed: true},
	}}
	db := &fakeRedmineStore{mapping: map[string]string{"Ré-ouvert": "doing"}}

	if _, _, err := passe(t, client, db); err != nil {
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
