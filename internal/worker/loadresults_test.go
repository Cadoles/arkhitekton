package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/store"
)

type fakeLoadStore struct {
	saved      []store.LoadResult
	measures   [][]store.BenchMeasure
	exclusive  bool
	concurrent int
	known      map[string]bool
}

func (f *fakeLoadStore) SaveLoadResult(
	_ context.Context, result store.LoadResult, measures []store.BenchMeasure,
) (int64, error) {
	f.saved = append(f.saved, result)
	f.measures = append(f.measures, measures)
	// Le faux doit modéliser la base : après enregistrement, le scénario existe.
	// Sans cela `TestIngestIsIdempotent` passait pour une mauvaise raison — la
	// garde n'était jamais réellement franchie.
	if f.known == nil {
		f.known = map[string]bool{}
	}
	f.known[result.Scenario] = true
	return int64(len(f.saved)), nil
}

func (f *fakeLoadStore) StartConditions(context.Context, int64) (bool, int, error) {
	return f.exclusive, f.concurrent, nil
}

func (f *fakeLoadStore) LoadResultExists(_ context.Context, _ int64, scenario string) (bool, error) {
	return f.known[scenario], nil
}

type fakeObjects map[string]string

func (f fakeObjects) List(_ context.Context, prefix string) ([]ObjectRef, error) {
	var refs []ObjectRef
	for key := range f {
		if strings.HasPrefix(key, prefix) {
			refs = append(refs, ObjectRef{Key: key, Size: int64(len(f[key]))})
		}
	}
	return refs, nil
}

func (f fakeObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	content, found := f[key]
	if !found {
		return nil, errors.New("objet absent")
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

const summaryJSON = `{"metrics":{
  "http_req_duration":{"values":{"med":98,"p(95)":310.2}},
  "http_reqs":{"values":{"count":24000,"rate":400}},
  "vus_max":{"values":{"value":200}}
}}`

func newIngester(objects fakeObjects) (*fakeLoadStore, *LoadIngester) {
	releases := &fakeLoadStore{exclusive: true, concurrent: 0, known: map[string]bool{}}
	return releases, NewLoadIngester(releases, objects, slog.New(slog.DiscardHandler))
}

func TestIngestSavesOneCampaignPerScenario(t *testing.T) {
	objects := fakeObjects{
		"raw/load-test-1/k6/panier-summary.json":    summaryJSON,
		"raw/load-test-1/k6/recherche-summary.json": summaryJSON,
		// Ni un résumé, ni du JSON : ignoré sans faire échouer le reste.
		"raw/load-test-1/k6/console.log": "bruit",
	}
	releases, ingester := newIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1", JobID: 9}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}

	if len(releases.saved) != 2 {
		t.Fatalf("%d campagne(s), attendu 2", len(releases.saved))
	}
	scenarios := map[string]bool{}
	for _, result := range releases.saved {
		scenarios[result.Scenario] = true
		// Les conditions constatées voyagent avec la campagne : sans elles, la
		// mesure n'est qu'un nombre.
		if !result.Exclusive || result.ConcurrentJobs != 0 {
			t.Errorf("conditions = %+v", result)
		}
	}
	if !scenarios["panier"] || !scenarios["recherche"] {
		t.Errorf("scénarios = %v", scenarios)
	}
}

// L'observateur revoit un run terminé au redémarrage. Réenregistrer produirait
// des points d'historique en double, c'est-à-dire une tendance fausse.
func TestIngestIsIdempotent(t *testing.T) {
	objects := fakeObjects{"raw/load-test-1/k6/panier-summary.json": summaryJSON}
	releases, ingester := newIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1"}); err != nil {
		t.Fatalf("première ingestion: %v", err)
	}
	releases.known["panier"] = true

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1"}); err != nil {
		t.Fatalf("seconde ingestion: %v", err)
	}
	if len(releases.saved) != 1 {
		t.Errorf("%d campagne(s) enregistrée(s), attendu 1", len(releases.saved))
	}
}

// Une campagne à quatre scénarios ne doit pas être perdue en entier parce que
// l'un d'eux a produit un fichier tronqué.
func TestOneBadSummaryDoesNotLoseTheOthers(t *testing.T) {
	objects := fakeObjects{
		"raw/load-test-1/k6/panier-summary.json":  summaryJSON,
		"raw/load-test-1/k6/tronque-summary.json": `{"metr`,
	}
	releases, ingester := newIngester(objects)

	err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1"})
	if err == nil {
		t.Error("l'échec partiel doit être signalé")
	}
	if len(releases.saved) != 1 {
		t.Errorf("%d campagne(s) enregistrée(s), attendu 1", len(releases.saved))
	}
	if !strings.Contains(err.Error(), "tronque") {
		t.Errorf("erreur = %v, attendu le nom du résumé fautif", err)
	}
}

// Le runner a pu échouer avant de publier : le run porte déjà son échec.
func TestIngestWithoutSummariesIsNotAnError(t *testing.T) {
	releases, ingester := newIngester(fakeObjects{})

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1"}); err != nil {
		t.Errorf("erreur = %v, attendu aucune", err)
	}
	if len(releases.saved) != 0 {
		t.Error("rien ne doit être enregistré")
	}
}

// Un run déclenché hors dashboard n'a pas de job : ses résultats valent autant,
// mais on ne peut rien affirmer de ses conditions.
func TestIngestWithoutAJobAssumesNonExclusive(t *testing.T) {
	objects := fakeObjects{"raw/load-test-1/k6/panier-summary.json": summaryJSON}
	releases, ingester := newIngester(objects)
	releases.exclusive = true

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1", JobID: 0}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if releases.saved[0].Exclusive {
		t.Error("sans job, aucune exclusivité ne peut être affirmée")
	}
}

func TestSummariseNamesTheFailedThresholds(t *testing.T) {
	objects := fakeObjects{
		"raw/load-test-1/k6/panier-summary.json": `{"metrics":{
		  "http_req_duration":{"values":{"p(95)":900},"thresholds":{"p(95)<500":{"ok":false}}},
		  "http_reqs":{"values":{"count":100,"rate":10}}
		}}`,
	}
	releases, ingester := newIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-1"}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	// Savoir *quel* seuil a lâché est ce qui décide de la suite.
	if !strings.Contains(releases.saved[0].Summary, "p(95)<500") {
		t.Errorf("résumé = %q", releases.saved[0].Summary)
	}
}

// Le contrat publie `load` comme nom de famille ; les Tasks du MSE écrivent
// sous `k6`, du nom de l'outil. Les deux doivent être lus.
//
// Sans cela, un projet qui suit la convention documentée ne voit rien : l'écran
// de charge reste vide et rien ne dit que le répertoire n'a jamais été ouvert
// (doc 12 §12.9, lot F).
func TestLeRepertoireDeFamillePublieEstLuPourLaCharge(t *testing.T) {
	objects := fakeObjects{
		"raw/load-test-2/load/panier-summary.json": summaryJSON,
	}
	releases, ingester := newIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-2", JobID: 9}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(releases.saved) != 1 {
		t.Fatalf("%d campagne(s) — le répertoire publié par le contrat n'est pas lu",
			len(releases.saved))
	}
}

// Les deux répertoires cohabitent sans se doubler : un run qui publierait sous
// les deux noms produirait deux fois la même campagne, donc une tendance fausse.
func TestLesDeuxRepertoiresDeChargeNeSeDoublentPas(t *testing.T) {
	objects := fakeObjects{
		"raw/load-test-3/k6/panier-summary.json":   summaryJSON,
		"raw/load-test-3/load/panier-summary.json": summaryJSON,
	}
	releases, ingester := newIngester(objects)

	if err := ingester.Ingest(context.Background(), LoadRun{ID: 1, Name: "load-test-3", JobID: 9}); err != nil {
		t.Fatalf("ingestion: %v", err)
	}
	if len(releases.saved) != 1 {
		t.Errorf("%d campagne(s) pour un même scénario — l'historique serait faux",
			len(releases.saved))
	}
}
